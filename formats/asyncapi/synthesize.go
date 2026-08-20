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
	"github.com/openbindings/openbindings-go/synthesize"
)

// BindingSpec identifies the unreleased first AsyncAPI binding candidate.
const BindingSpec = "openbindings.asyncapi@1"

// DefaultSourceName is the key used in the interface's Sources map for the AsyncAPI source.
const DefaultSourceName = "asyncapi"

func synthesizeInterfaceWithDoc(_ context.Context, in *synthesize.SynthesizeInput, doc *document) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, synthesize.ErrNoSources
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
		opKey := synthesize.UniqueKey(synthesize.SanitizeKey(opID), usedKeys)
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
		// invocation is the counterparty. Cyclic-reference hoisting runs on
		// each direction so recursion the artifact declared survives the
		// projection ($defs, decycle.go).
		inputSchema, outputSchema := operationBoundarySchemas(doc, &asyncOp)
		opPointer := "#/operations/" + escapeDefsPointerSegment(opKey)
		if inputSchema != nil {
			obiOp.Input = decycleOperationSchema(inputSchema, doc.raw, opPointer+"/input")
		}
		if outputSchema != nil {
			obiOp.Output = decycleOperationSchema(outputSchema, doc.raw, opPointer+"/output")
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
	data = trimUTF8BOM(data)
	if isJSON(data) {
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("parse AsyncAPI document: %w", err)
		}
	} else {
		var err error
		envelope, err = decodeYAMLObject(data)
		if err != nil {
			return fmt.Errorf("parse AsyncAPI document: %w", err)
		}
	}
	return discriminateEnvelope(envelope)
}

func discriminateEnvelope(envelope map[string]any) error {
	value, ok := envelope["asyncapi"]
	if !ok {
		return fmt.Errorf("not a valid AsyncAPI document (missing 'asyncapi' field)")
	}
	version, ok := value.(string)
	accepted := map[string]bool{
		"2.0.0": true, "2.1.0": true, "2.2.0": true, "2.3.0": true,
		"2.4.0": true, "2.5.0": true, "2.6.0": true,
		"3.0.0": true, "3.1.0": true,
	}
	if !ok || !accepted[version] {
		return fmt.Errorf("unsupported AsyncAPI version %v: openbindings.asyncapi@1 accepts exactly 2.0.0–2.6.0, 3.0.0, and 3.1.0 (ASYNC-P-01)", value)
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
	data = trimUTF8BOM(data)
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

func trimUTF8BOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func operationDescription(op asyncOperation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func resolveMessageRef(doc *document, ref messageRef) *message {
	return resolveMessageRefSeen(doc, ref, map[string]bool{})
}

// messageRefIdentity retains the artifact's deepest declared message address
// for coverage. A 2.x normalization cell points first at a synthetic channel
// message, which can itself be the author's dangling components/messages ref.
func messageRefIdentity(doc *document, ref messageRef) string {
	if message := lookupMessageRef(doc, ref); message != nil && message.Ref != "" {
		return message.Ref
	}
	return ref.Ref
}

func lookupMessageRef(doc *document, ref messageRef) *message {
	if ref.Ref == "" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ref.Ref, "#/"), "/")
	if len(parts) == 3 && parts[0] == "components" && parts[1] == "messages" && doc.Components != nil {
		if value, ok := doc.Components.Messages[unescapeRefToken(parts[2])]; ok {
			return &value
		}
	}
	if len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" {
		if channel, ok := doc.Channels[unescapeRefToken(parts[1])]; ok {
			if value, ok := channel.Messages[unescapeRefToken(parts[3])]; ok {
				return &value
			}
		}
	}
	return nil
}

func resolveMessageRefSeen(doc *document, ref messageRef, seen map[string]bool) *message {
	if ref.Ref == "" {
		return nil
	}
	if seen[ref.Ref] {
		return nil
	}
	seen[ref.Ref] = true

	if msg := lookupMessageRef(doc, ref); msg != nil {
		if msg.Ref != "" {
			return resolveMessageRefSeen(doc, messageRef{Ref: msg.Ref}, seen)
		}
		return msg
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
