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
	"strings"

	asyncapiclient "github.com/openbindings/asyncapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"gopkg.in/yaml.v3"
)

// BindingSpec identifies the current reply-preserving AsyncAPI revision.
const BindingSpec = "openbindings.asyncapi@2"

// LegacyBindingSpec identifies the immutable revision-1 AsyncAPI binding.
const LegacyBindingSpec = "openbindings.asyncapi@1"

func preservesSendReplies(bindingSpec string) bool { return bindingSpec == BindingSpec }

// DefaultSourceName is the key used in the interface's Sources map for the AsyncAPI source.
const DefaultSourceName = "asyncapi"

func synthesizeInterfaceWithDoc(_ context.Context, in *openbindings.SynthesizeInput, doc *document) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	sourceEntry := openbindings.Source{
		BindingSpec: src.BindingSpec,
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

	opIDs := bindableOperationIDs(doc, src.BindingSpec)

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
			payload := operationPayloadSchema(doc, &asyncOp, false)
			if payload != nil {
				obiOp.Output = payload
			}
		case "receive":
			// The application receives; invoking publishes — the operation's
			// messages are the invoker's INPUT, and a declared reply is what
			// the publish's response decodes to.
			inputPayload := operationPayloadSchema(doc, &asyncOp, true)
			if inputPayload != nil {
				obiOp.Input = inputPayload
			}
			if asyncOp.Reply != nil {
				outputPayload := replyPayloadSchema(doc, asyncOp.Reply)
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
	if err := discriminateDocument(data); err != nil {
		return nil, err
	}
	data, err = resolveArtifactReferences(ctx, client, location, data)
	if err != nil {
		return nil, err
	}
	return parseDocument(data)
}

// parseDocument parses AsyncAPI document bytes (JSON or YAML), enforces the
// accepted-line discrimination, and resolves internal references. Pure: no
// I/O. The shared parse tail behind loadDocument and the exported
// ParseDocument.
func parseDocument(data []byte) (*document, error) {
	// Preserve the binding specification's rule-coded refusal at the adapter
	// boundary; the standalone normalizer intentionally speaks artifact-native
	// language and has no dependency on OpenBindings rule identifiers.
	if err := discriminateDocument(data); err != nil {
		return nil, err
	}
	normalized, err := asyncapiclient.NormalizeDocument(data)
	if err != nil {
		return nil, err
	}
	var doc document
	var envelope map[string]any
	if err := json.Unmarshal(normalized, &envelope); err != nil {
		return nil, fmt.Errorf("parse AsyncAPI document: %w", err)
	}
	if err := discriminateEnvelope(envelope); err != nil {
		return nil, err
	}
	infoValue, hasInfo := envelope["info"]
	infoObject, infoIsObject := infoValue.(map[string]any)
	_, hasTitle := infoObject["title"].(string)
	_, hasVersion := infoObject["version"].(string)
	if !hasInfo || !infoIsObject || !hasTitle || !hasVersion {
		return nil, fmt.Errorf("not a valid AsyncAPI document (info.title and info.version are required strings)")
	}

	if err := json.Unmarshal(normalized, &doc); err != nil {
		return nil, fmt.Errorf("parse normalized AsyncAPI document: %w", err)
	}

	resolveRefs(&doc)

	return &doc, nil
}

// discriminateDocument applies ASYNC-P-01 before external-reference
// resolution. An unsupported edition must not trigger network requests for a
// closure this binding will never interpret.
func discriminateDocument(data []byte) error {
	var envelope map[string]any
	if err := yaml.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("parse AsyncAPI document: %w", err)
	}
	return discriminateEnvelope(envelope)
}

func discriminateEnvelope(envelope map[string]any) error {
	value, ok := envelope["asyncapi"]
	if !ok {
		return fmt.Errorf("not a valid AsyncAPI document (missing 'asyncapi' field)")
	}
	version, ok := value.(string)
	if !ok {
		return fmt.Errorf("unsupported AsyncAPI version %v: the supported openbindings.asyncapi revisions accept exactly 3.0.0 (ASYNC-P-01)", value)
	}
	if version != "3.0.0" {
		return fmt.Errorf("unsupported AsyncAPI version %q: the supported openbindings.asyncapi revisions accept exactly 3.0.0 (ASYNC-P-01)", version)
	}
	return nil
}

// absolutizeArtifactLocation lifts a bare filesystem path to the file://
// document address the strict loader accepts — authoring-time operator
// convenience at the SYNTHESIS entries only, the usage family's posture
// (one loader for every lane, no bare-path lane). The invoke/binding lanes
// never absolutize: a document's own bare-path location is a relative
// reference in form and is refused (ASYNC-D-02). Synthesis emits this
// normalized address so the returned source remains invocable.
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

func resolveMessageRef(doc *document, ref messageRef) *message {
	if ref.Ref == "" {
		return nil
	}

	path := strings.TrimPrefix(ref.Ref, "#/")
	parts := strings.Split(path, "/")

	if len(parts) == 3 && parts[0] == "components" && parts[1] == "messages" {
		if doc.Components != nil {
			if msg, ok := doc.Components.Messages[unescapeRefToken(parts[2])]; ok {
				return &msg
			}
		}
	}

	if len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" {
		if ch, ok := doc.Channels[unescapeRefToken(parts[1])]; ok {
			if msg, ok := ch.Messages[unescapeRefToken(parts[3])]; ok {
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
		return unescapeRefToken(parts[len(parts)-1])
	}
	return ""
}

func unescapeRefToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}
