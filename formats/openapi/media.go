package openapi

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// This file implements §9.2 of openbindings.openapi@1 (OAPI-P-04): request
// media selection with its deterministic tiebreaks and pre-dispatch
// refusals, multipart part encoding (including the Base64 boundary encoding
// for binary-signaled parts), urlencoded field serialization, and the
// Accept-header membership rule — plus the §8 declared-media facts (success
// responses, streaming capability) the interaction shape is bounded by.

// normalizeMediaType lowercases a media type and strips its parameters:
// matching throughout §9.2 compares type and subtype, ignoring parameters.
func normalizeMediaType(mt string) string {
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

// isMediaRange reports a media range (*/*, application/*): ranges never
// participate in selection.
func isMediaRange(mt string) bool {
	return strings.Contains(mt, "*")
}

// isJSONMediaType reports application/json or a +json structured-suffix
// type. The argument must already be normalized.
func isJSONMediaType(mt string) bool {
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// Body families, in the §9.2 preference order.
const (
	familyJSON       = "json"
	familyMultipart  = "multipart"
	familyURLEncoded = "urlencoded"
	familyText       = "text"
)

// bodyPlan is the pre-dispatch answer to the request-carriage questions: the
// selected media type, its family, and the flatten mode its schema implies
// (§9.1: object schemas flatten by property name; non-object schemas ride
// the synthetic `body` property, unwrapped at the wire).
type bodyPlan struct {
	declared  bool
	required  bool
	mediaKey  string // the declared content key, verbatim
	mediaType string // normalized type/subtype (the request Content-Type)
	media     *openapi3.MediaType
	family    string
	synthetic bool
	props     map[string]bool // declared top-level body property names (object mode)
}

// planRequestBody selects the request media type from the operation's
// DECLARED requestBody.content per OAPI-P-04's preference order: exact
// application/json, then the lexicographically least +json type, then
// multipart/form-data, then application/x-www-form-urlencoded, then
// text/plain (whose string-value condition is checked after routing, when
// the body value exists). An operation declaring only media types outside
// these families is refused loudly before dispatch: its request carriage is
// undefined in revision 1.
func planRequestBody(op *openapi3.Operation) (*bodyPlan, error) {
	if !hasRequestBody(op) {
		return &bodyPlan{}, nil
	}
	rb := op.RequestBody.Value
	if len(rb.Content) == 0 {
		// A requestBody with no content declares no carriage; treat as no
		// declared body (degenerate artifact — the OAS requires content).
		return &bodyPlan{}, nil
	}

	type candidate struct {
		key        string
		normalized string
	}
	var exactJSON, multipartFD, urlEncoded, textPlain *candidate
	var plusJSON []candidate
	var declared []string
	for key := range rb.Content {
		n := normalizeMediaType(key)
		declared = append(declared, n)
		if isMediaRange(n) {
			continue // ranges never participate in selection
		}
		c := candidate{key: key, normalized: n}
		switch {
		case n == "application/json":
			exactJSON = &c
		case strings.HasSuffix(n, "+json"):
			plusJSON = append(plusJSON, c)
		case n == "multipart/form-data":
			multipartFD = &c
		case n == "application/x-www-form-urlencoded":
			urlEncoded = &c
		case n == "text/plain":
			textPlain = &c
		}
	}

	var chosen *candidate
	var family string
	switch {
	case exactJSON != nil:
		chosen, family = exactJSON, familyJSON
	case len(plusJSON) > 0:
		sort.Slice(plusJSON, func(i, j int) bool { return plusJSON[i].normalized < plusJSON[j].normalized })
		chosen, family = &plusJSON[0], familyJSON
	case multipartFD != nil:
		chosen, family = multipartFD, familyMultipart
	case urlEncoded != nil:
		chosen, family = urlEncoded, familyURLEncoded
	case textPlain != nil:
		chosen, family = textPlain, familyText
	default:
		sort.Strings(declared)
		return nil, fmt.Errorf("request body declares only media types outside the families openbindings.openapi@1 defines a request carriage for (declared: %s)", strings.Join(declared, ", "))
	}

	plan := &bodyPlan{
		declared:  true,
		required:  rb.Required,
		mediaKey:  chosen.key,
		mediaType: chosen.normalized,
		media:     rb.Content[chosen.key],
		family:    family,
	}

	// Flatten mode. text/plain bodies are scalar by nature: they always ride
	// the synthetic `body` property. JSON-family bodies are synthetic when
	// the declared schema's type is non-object (array or scalar, §9.1);
	// multipart and urlencoded bodies are field maps by construction.
	switch family {
	case familyText:
		plan.synthetic = true
	case familyJSON:
		if schema := mediaSchema(plan.media); schema != nil && schema.Type != nil {
			types := schema.Type.Slice()
			if len(types) > 0 && !schema.Type.Is("object") {
				plan.synthetic = true
			}
		}
	}
	if !plan.synthetic {
		plan.props = mediaSchemaProps(plan.media)
	}
	return plan, nil
}

func mediaSchema(media *openapi3.MediaType) *openapi3.Schema {
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

// mediaSchemaProps returns the selected media schema's declared top-level
// property names — the body half of the flattened model's collision rule.
func mediaSchemaProps(media *openapi3.MediaType) map[string]bool {
	schema := mediaSchema(media)
	if schema == nil || len(schema.Properties) == 0 {
		return nil
	}
	props := make(map[string]bool, len(schema.Properties))
	for name := range schema.Properties {
		props[name] = true
	}
	return props
}

// buildRequestBody produces the wire body for the selected media type. A nil
// reader with an empty content type means no body is sent (§9.1's
// remaining-body rule: with a JSON-family selection and every input field
// consumed by parameters, the body is {} if the request body is declared
// required and omitted otherwise).
func buildRequestBody(doc *openapi3.T, plan *bodyPlan, routed *routedInput) (io.Reader, string, error) {
	if plan == nil || !plan.declared {
		return nil, "", nil
	}
	switch plan.family {
	case familyJSON:
		if plan.synthetic {
			if !routed.bodySet {
				// A supplied input missing the synthetic body member is sent
				// as-is (the server's declared validation is the authority):
				// no body rides the wire.
				return nil, "", nil
			}
			b, err := json.Marshal(routed.bodyValue)
			if err != nil {
				return nil, "", err
			}
			return bytes.NewReader(b), plan.mediaType, nil
		}
		if len(routed.bodyFields) == 0 {
			if plan.required {
				return strings.NewReader("{}"), plan.mediaType, nil
			}
			return nil, "", nil
		}
		b, err := json.Marshal(routed.bodyFields)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(b), plan.mediaType, nil
	case familyMultipart:
		if len(routed.bodyFields) == 0 && !plan.required {
			return nil, "", nil
		}
		return buildMultipartBody(doc, plan.media, routed.bodyFields)
	case familyURLEncoded:
		if len(routed.bodyFields) == 0 && !plan.required {
			return nil, "", nil
		}
		body, err := buildURLEncodedBody(plan.media, routed.bodyFields)
		if err != nil {
			return nil, "", err
		}
		return strings.NewReader(body), plan.mediaType, nil
	case familyText:
		if !routed.bodySet {
			return nil, "", nil
		}
		s, ok := routed.bodyValue.(string)
		if !ok {
			// The selection condition failed: text/plain is selected only
			// when the body value is a string (OAPI-P-04).
			return nil, "", fmt.Errorf("request media text/plain was selected but the body value is %T, not a string", routed.bodyValue)
		}
		return strings.NewReader(s), plan.mediaType, nil
	}
	return nil, "", fmt.Errorf("unknown body family %q", plan.family)
}

// ---------------------------------------------------------------------------
// Multipart (OAPI-P-04's part-encoding rules)
// ---------------------------------------------------------------------------

// buildMultipartBody encodes body fields as multipart/form-data. A part is
// binary-signaled per the artifact's edition — 3.0.x by `format: binary`,
// 3.1.x by a string schema carrying contentMediaType/contentEncoding — and a
// binary-signaled part's bytes come from the caller's string value: decoded
// per the schema's declared contentEncoding where one is declared, and by
// Base64 where the artifact signals binary without declaring an encoding
// (the specification's boundary encoding for bytes). Parts that are not
// binary-signaled serialize per the artifact's `encoding` object where
// present, else per the OAS's per-type part defaults (objects as
// application/json parts, primitives as text fields). Nothing here is
// decided by the value's bytes; the artifact's declarations decide. Fields
// are written in sorted order for a deterministic body.
func buildMultipartBody(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any) (io.Reader, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	is30 := isOpenAPI30(majorMinor(doc.OpenAPI))

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	schema := mediaSchema(media)
	for _, name := range names {
		value := fields[name]
		var propSchema *openapi3.Schema
		if schema != nil {
			if ref, ok := schema.Properties[name]; ok && ref != nil {
				propSchema = ref.Value
			}
		}
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}

		// A declared array expands into repeated parts of the same name,
		// each element encoded per the items schema (the multipart way to
		// carry arrays — including arrays of files).
		if propSchema != nil && propSchema.Type.Is("array") {
			if arr, ok := asArray(value); ok {
				var items *openapi3.Schema
				if propSchema.Items != nil {
					items = propSchema.Items.Value
				}
				for _, elem := range arr {
					if err := writeMultipartPart(writer, name, elem, items, enc, is30); err != nil {
						return nil, "", err
					}
				}
				continue
			}
		}
		if err := writeMultipartPart(writer, name, value, propSchema, enc, is30); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	return &buf, writer.FormDataContentType(), nil
}

func writeMultipartPart(writer *multipart.Writer, name string, value any, schema *openapi3.Schema, enc *openapi3.Encoding, is30 bool) error {
	if binarySignaled(schema, is30) {
		data, err := binaryPartBytes(name, value, declaredContentEncoding(schema, is30))
		if err != nil {
			return err
		}
		ct := ""
		if enc != nil && enc.ContentType != "" {
			ct = enc.ContentType
		} else if cmt := schemaExtensionString(schema, "contentMediaType"); cmt != "" {
			ct = cmt
		} else {
			ct = "application/octet-stream"
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		h.Set("Content-Type", ct)
		part, err := writer.CreatePart(h)
		if err != nil {
			return fmt.Errorf("create part %q: %w", name, err)
		}
		if _, err := part.Write(data); err != nil {
			return fmt.Errorf("write part %q: %w", name, err)
		}
		return nil
	}

	// The encoding object's contentType, where declared, decides the part's
	// serialization; else the OAS per-type part defaults apply.
	if enc != nil && enc.ContentType != "" {
		ct := normalizeMediaType(enc.ContentType)
		var body []byte
		if isJSONMediaType(ct) {
			b, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("part %q: %w", name, err)
			}
			body = b
		} else if s, ok := value.(string); ok {
			body = []byte(s)
		} else if s, err := primitiveString(value); err == nil {
			body = []byte(s)
		} else {
			b, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("part %q: %w", name, err)
			}
			body = b
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, name))
		h.Set("Content-Type", enc.ContentType)
		part, err := writer.CreatePart(h)
		if err != nil {
			return fmt.Errorf("create part %q: %w", name, err)
		}
		if _, err := part.Write(body); err != nil {
			return fmt.Errorf("write part %q: %w", name, err)
		}
		return nil
	}

	// Per-type defaults: objects (and undeclared complex values) ride as
	// application/json parts; primitives as plain form fields.
	if isComplexPartValue(value, schema) {
		b, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("part %q: %w", name, err)
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, name))
		h.Set("Content-Type", "application/json")
		part, err := writer.CreatePart(h)
		if err != nil {
			return fmt.Errorf("create part %q: %w", name, err)
		}
		if _, err := part.Write(b); err != nil {
			return fmt.Errorf("write part %q: %w", name, err)
		}
		return nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return fmt.Errorf("part %q: %w", name, err)
	}
	if err := writer.WriteField(name, s); err != nil {
		return fmt.Errorf("write field %q: %w", name, err)
	}
	return nil
}

// isComplexPartValue decides object-vs-primitive part encoding: by the
// declared schema type where one exists, by the JSON value's own shape for
// undeclared passthrough fields (a declaration-free field has no artifact
// answer; a JSON value's TYPE is structure, not byte-sniffing).
func isComplexPartValue(value any, schema *openapi3.Schema) bool {
	if schema != nil && schema.Type != nil && len(schema.Type.Slice()) > 0 {
		return schema.Type.Is("object") || schema.Type.Is("array")
	}
	if _, ok := asObject(value); ok {
		return true
	}
	if _, ok := asArray(value); ok {
		return true
	}
	return false
}

// binarySignaled applies the edition rule: 3.0.x signals binary with
// `format: binary`; 3.1.x with a string schema carrying contentMediaType
// or contentEncoding.
func binarySignaled(schema *openapi3.Schema, is30 bool) bool {
	if schema == nil {
		return false
	}
	if is30 {
		return schema.Format == "binary"
	}
	if !schema.Type.Is("string") {
		return false
	}
	return schemaExtensionString(schema, "contentMediaType") != "" || schemaExtensionString(schema, "contentEncoding") != ""
}

// declaredContentEncoding returns the 3.1 schema's declared contentEncoding
// (3.0 has no equivalent keyword; its binary signal carries no encoding).
func declaredContentEncoding(schema *openapi3.Schema, is30 bool) string {
	if is30 {
		return ""
	}
	return schemaExtensionString(schema, "contentEncoding")
}

// schemaExtensionString reads a 3.1 keyword kin-openapi carries in the
// schema's extensions map (contentMediaType, contentEncoding).
func schemaExtensionString(schema *openapi3.Schema, key string) string {
	if schema == nil || schema.Extensions == nil {
		return ""
	}
	s, _ := schema.Extensions[key].(string)
	return s
}

// binaryPartBytes decodes a binary-signaled part's bytes from the caller's
// string value: per the declared contentEncoding when one is declared,
// Base64 otherwise (the boundary encoding for bytes — the operation value
// domain is JSON). A Go []byte value passes through raw as an in-process
// convenience (it cannot have arrived as JSON).
func binaryPartBytes(name string, value any, contentEncoding string) ([]byte, error) {
	if raw, ok := value.([]byte); ok {
		return raw, nil
	}
	s, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("binary part %q: the value must be a string carrying the encoded bytes, got %T", name, value)
	}
	switch strings.ToLower(contentEncoding) {
	case "", "base64":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			if b2, err2 := base64.RawStdEncoding.DecodeString(s); err2 == nil {
				return b2, nil
			}
			return nil, fmt.Errorf("binary part %q: invalid base64: %w", name, err)
		}
		return b, nil
	case "base64url":
		b, err := base64.URLEncoding.DecodeString(s)
		if err != nil {
			if b2, err2 := base64.RawURLEncoding.DecodeString(s); err2 == nil {
				return b2, nil
			}
			return nil, fmt.Errorf("binary part %q: invalid base64url: %w", name, err)
		}
		return b, nil
	case "base16", "hex":
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("binary part %q: invalid base16: %w", name, err)
		}
		return b, nil
	case "base32":
		b, err := base32.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("binary part %q: invalid base32: %w", name, err)
		}
		return b, nil
	case "quoted-printable":
		b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(s)))
		if err != nil {
			return nil, fmt.Errorf("binary part %q: invalid quoted-printable: %w", name, err)
		}
		return b, nil
	case "binary", "7bit", "8bit":
		return []byte(s), nil
	default:
		return nil, fmt.Errorf("binary part %q: unsupported contentEncoding %q", name, contentEncoding)
	}
}

// ---------------------------------------------------------------------------
// application/x-www-form-urlencoded
// ---------------------------------------------------------------------------

// buildURLEncodedBody serializes body fields per the OAS `encoding` rules:
// each field's style/explode/allowReserved come from the media type's
// encoding object where present, defaulting to form/explode=true. Fields are
// serialized with the same expansions as query parameters and joined in
// sorted-name order for a deterministic body.
func buildURLEncodedBody(media *openapi3.MediaType, fields map[string]any) (string, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var units []string
	for _, name := range names {
		style, explode, allowReserved := openapi3.SerializationForm, true, false
		if media != nil {
			if enc := media.Encoding[name]; enc != nil {
				sm := enc.SerializationMethod()
				style, explode = sm.Style, sm.Explode
				allowReserved = enc.AllowReserved
			}
		}
		u, err := serializeQueryValue(name, fields[name], style, explode, allowReserved)
		if err != nil {
			return "", fmt.Errorf("form field %q: %w", name, err)
		}
		units = append(units, u...)
	}
	return strings.Join(units, "&"), nil
}

// ---------------------------------------------------------------------------
// Success responses, Accept, streaming capability (§8, §9.2)
// ---------------------------------------------------------------------------

// isSuccessResponseKey reports the §8 definition of a success response
// entry: a 2xx status literal or the `2XX` range. The `default` entry never
// participates in shape or media determination.
func isSuccessResponseKey(key string) bool {
	if key == "2XX" {
		return true
	}
	return len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
}

// successMediaTypes returns the declared CONCRETE media types of the
// operation's success responses, normalized, deduplicated, and sorted
// (membership is normative, ordering is not). Media ranges are excluded:
// they are not concrete.
func successMediaTypes(op *openapi3.Operation) []string {
	if op == nil || op.Responses == nil {
		return nil
	}
	seen := map[string]bool{}
	for key, ref := range op.Responses.Map() {
		if !isSuccessResponseKey(key) || ref == nil || ref.Value == nil {
			continue
		}
		for mt := range ref.Value.Content {
			n := normalizeMediaType(mt)
			if n == "" || isMediaRange(n) {
				continue
			}
			seen[n] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for mt := range seen {
		out = append(out, mt)
	}
	sort.Strings(out)
	return out
}

// acceptHeader advertises the declared concrete media types of the
// operation's success responses; absent any declaration, application/json
// (§9.2).
func acceptHeader(op *openapi3.Operation) string {
	types := successMediaTypes(op)
	if len(types) == 0 {
		return "application/json"
	}
	return strings.Join(types, ", ")
}

// isStreamingCapable reports the §8 static capability: an operation is
// streaming-capable iff text/event-stream appears among the declared media
// types of its success responses.
func isStreamingCapable(op *openapi3.Operation) bool {
	for _, mt := range successMediaTypes(op) {
		if mt == "text/event-stream" {
			return true
		}
	}
	return false
}
