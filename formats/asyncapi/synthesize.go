package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"gopkg.in/yaml.v3"
)

const BindingSpec = "openbindings.asyncapi@1"

// DefaultSourceName is the key used in the interface's Sources map for the AsyncAPI source.
const DefaultSourceName = "asyncapi"

func synthesizeInterfaceWithDoc(_ context.Context, in *openbindings.SynthesizeInput, doc *document) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	sourceEntry := openbindings.Source{
		BindingSpec: BindingSpec,
	}
	if src.Location != "" {
		sourceEntry.Location = src.Location
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         doc.Info.Title,
		Version:      doc.Info.Version,
		Description:  doc.Info.Description,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}

	usedKeys := map[string]bool{}

	opIDs := make([]string, 0, len(doc.Operations))
	for opID := range doc.Operations {
		opIDs = append(opIDs, opID)
	}
	sort.Strings(opIDs)

	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		opKey := openbindings.UniqueKey(openbindings.SanitizeKey(opID), usedKeys)
		usedKeys[opKey] = true

		obiOp := openbindings.Operation{
			Description: operationDescription(asyncOp),
		}

		if len(asyncOp.Tags) > 0 {
			for _, tag := range asyncOp.Tags {
				obiOp.Tags = append(obiOp.Tags, tag.Name)
			}
		}

		// Schema direction follows the complementary perspective
		// (ASYNC-P-02): the artifact describes the application, the
		// invocation is the counterparty.
		switch asyncOp.Action {
		case "send":
			// The application sends; invoking subscribes — the operation's
			// messages are the invoker's OUTPUT.
			payload := resolveOperationPayload(doc, asyncOp)
			if payload != nil {
				obiOp.Output = payload
			}
		case "receive":
			// The application receives; invoking publishes — the operation's
			// messages are the invoker's INPUT, and a declared reply is what
			// the publish's response decodes to.
			inputPayload := resolveOperationPayload(doc, asyncOp)
			if inputPayload != nil {
				obiOp.Input = inputPayload
			}
			if asyncOp.Reply != nil {
				outputPayload := resolveReplyPayload(doc, asyncOp.Reply)
				if outputPayload != nil {
					obiOp.Output = outputPayload
				}
			}
		}

		iface.Operations[opKey] = obiOp

		ref := operationRef(opID)
		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       ref,
		}
	}

	return &iface, nil
}

// Note: the OBI no longer carries a security section. Context requirements
// are derived at invocation time from the AsyncAPI doc's security
// declarations and surfaced via CONTEXT_REQUIRED negotiation (see
// requiredContext in invoke.go and Invoker.PrepareBinding).

func loadDocument(ctx context.Context, client *http.Client, location string, content json.RawMessage) (*document, error) {
	data, err := sourceToBytes(ctx, client, location, content)
	if err != nil {
		return nil, err
	}

	var doc document

	if isJSON(data) {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse AsyncAPI JSON: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse AsyncAPI YAML: %w", err)
		}
	}

	// ASYNC-P-01: the artifact's own `asyncapi` field discriminates the
	// accepted line — 3.0.x ONLY. A later 3.x line is adopted by compatible
	// revision of the binding specification, never sight-unseen.
	if !strings.HasPrefix(doc.AsyncAPI, "3.0.") {
		return nil, fmt.Errorf("unsupported AsyncAPI version %q: openbindings.asyncapi@1 accepts the 3.0.x line only (ASYNC-P-01)", doc.AsyncAPI)
	}

	resolveRefs(&doc)

	return &doc, nil
}

// absolutizeArtifactLocation lifts a bare filesystem path to the file://
// document address the strict loader accepts — authoring-time operator
// convenience at the SYNTHESIS entries only, the usage family's posture
// (one loader for every lane, no bare-path lane). The invoke/binding lanes
// never absolutize: a document's own bare-path location is a relative
// reference in form and is refused (ASYNC-D-02). Emission is untouched —
// what a synthesized document may carry as its location is the
// embed-default ruling's territory, not this helper's.
func absolutizeArtifactLocation(location string) (string, error) {
	if location == "" || strings.Contains(location, "://") {
		return location, nil
	}
	abs := location
	if !filepath.IsAbs(location) {
		var err error
		abs, err = filepath.Abs(location)
		if err != nil {
			return "", fmt.Errorf("resolve AsyncAPI artifact path: %w", err)
		}
	}
	return "file://" + abs, nil
}

// validateDocumentAddress checks ASYNC-D-02's location grammar offline,
// without dereferencing: `location`, when present, is an absolute URI
// addressing the AsyncAPI document itself. A bare filesystem path is a
// relative reference in form (core OBI-D-05) and is refused — a local
// artifact is addressed as file:// or embedded as the source's content.
func validateDocumentAddress(location string) error {
	u, err := url.Parse(location)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return fmt.Errorf("asyncapi location %q is not an absolute URI addressing the document (ASYNC-D-02): a local artifact is addressed as file:// or embedded as the source's content", location)
	}
	return nil
}

func sourceToBytes(ctx context.Context, client *http.Client, location string, content json.RawMessage) ([]byte, error) {
	if content != nil {
		return openbindings.ContentToBytes(content)
	}
	if location == "" {
		return nil, fmt.Errorf("source must have location or content")
	}
	if err := validateDocumentAddress(location); err != nil {
		return nil, err
	}
	if openbindings.IsHTTPURL(location) {
		req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", location, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", location, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("fetch %q: HTTP %d", location, resp.StatusCode)
		}
		// Deliberately fixed: an artifact-fetch guard on the source document,
		// not a delivery unit — BindingInvocationArgs.MaxDeliveryUnitBytes
		// does not apply here.
		return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	}
	u, _ := url.Parse(location)
	if u.Scheme != "file" {
		return nil, fmt.Errorf("asyncapi location scheme %q is not dereferenced by this processor (supported: file, http, https)", u.Scheme)
	}
	return os.ReadFile(u.Path)
}

func isJSON(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func operationDescription(op asyncOperation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func resolveOperationPayload(doc *document, op asyncOperation) map[string]any {
	if len(op.Messages) > 0 {
		msg := resolveMessageRef(doc, op.Messages[0])
		if msg != nil && msg.Payload != nil {
			return msg.Payload
		}
	}

	channelName := extractRefName(op.Channel.Ref)
	if channelName == "" {
		return nil
	}
	channel, ok := doc.Channels[channelName]
	if !ok {
		return nil
	}

	for _, msg := range channel.Messages {
		if msg.Payload != nil {
			return msg.Payload
		}
	}

	return nil
}

func resolveReplyPayload(doc *document, reply *operationReply) map[string]any {
	if reply == nil {
		return nil
	}

	if len(reply.Messages) > 0 {
		msg := resolveMessageRef(doc, reply.Messages[0])
		if msg != nil && msg.Payload != nil {
			return msg.Payload
		}
	}

	return nil
}

func resolveMessageRef(doc *document, ref messageRef) *message {
	if ref.Ref == "" {
		return nil
	}

	path := strings.TrimPrefix(ref.Ref, "#/")
	parts := strings.Split(path, "/")

	if len(parts) == 3 && parts[0] == "components" && parts[1] == "messages" {
		if doc.Components != nil {
			if msg, ok := doc.Components.Messages[parts[2]]; ok {
				return &msg
			}
		}
	}

	if len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" {
		if ch, ok := doc.Channels[parts[1]]; ok {
			if msg, ok := ch.Messages[parts[3]]; ok {
				return &msg
			}
		}
	}

	return nil
}

func extractRefName(ref string) string {
	if ref == "" {
		return ""
	}
	path := strings.TrimPrefix(ref, "#/")
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
