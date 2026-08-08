package openapi

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
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

type parsedMediaType struct {
	base      string
	params    map[string]string
	canonical string
	identity  string
}

func parseMediaType(raw string) (parsedMediaType, error) {
	base, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return parsedMediaType{}, fmt.Errorf("invalid media type %q: %w", raw, err)
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" || isMediaRange(base) {
		return parsedMediaType{}, fmt.Errorf("media type %q is not concrete", raw)
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	normalizedParams := make(map[string]string, len(params))
	identity := base
	for _, key := range keys {
		value := params[key]
		normalizedParams[key] = value
		identity += "\x00" + key + "=" + value
	}
	canonical := mime.FormatMediaType(base, normalizedParams)
	if canonical == "" {
		return parsedMediaType{}, fmt.Errorf("invalid media type %q", raw)
	}
	return parsedMediaType{base: base, params: normalizedParams, canonical: canonical, identity: identity}, nil
}

// degenerateMediaError is §9.2's degenerate media/schema combination
// refusal (OAPI-P-04): the selected request media type has no OAS-defined
// wire form for the declared body schema. A distinct type so synthesis
// (synthesize.go) can surface the same fact as the
// openapi.media_schema_mismatch warning without re-deriving the selection.
type degenerateMediaError struct{ msg string }

func (e *degenerateMediaError) Error() string { return e.msg }

// planRequestBody returns the reference SDK's first declaration-sorted
// candidate. Runtime invocation uses planRequestBodies and applies
// candidate-specific admissibility after reading the caller value.
func planRequestBody(op *openapi3.Operation) (*bodyPlan, error) {
	plans, err := planRequestBodies(op)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return &bodyPlan{}, nil
	}
	return plans[0], nil
}

// planRequestBodies preserves the artifact's concrete supported candidate
// set. Sorting is a nonnormative reference-SDK policy only; the binding
// specification gives the declarations no preference order.
func planRequestBodies(op *openapi3.Operation) ([]*bodyPlan, error) {
	if !hasRequestBody(op) {
		return nil, nil
	}
	rb := op.RequestBody.Value
	if len(rb.Content) == 0 {
		return nil, nil
	}

	type candidate struct {
		key    string
		parsed parsedMediaType
		family string
	}
	var candidates []candidate
	var declared []string
	identities := map[string]string{}
	for key := range rb.Content {
		parsed, err := parseMediaType(key)
		if err != nil {
			declared = append(declared, key)
			continue
		}
		declared = append(declared, parsed.canonical)
		if previous, exists := identities[parsed.identity]; exists {
			return nil, fmt.Errorf("request content declarations %q and %q denote the same parsed media type (OAPI-P-04 normalized collision)", previous, key)
		}
		identities[parsed.identity] = key
		family := ""
		switch {
		case isJSONMediaType(parsed.base):
			family = familyJSON
		case parsed.base == "multipart/form-data":
			family = familyMultipart
		case parsed.base == "application/x-www-form-urlencoded":
			family = familyURLEncoded
		case parsed.base == "text/plain":
			family = familyText
		}
		if family != "" {
			candidates = append(candidates, candidate{key: key, parsed: parsed, family: family})
		}
	}
	if len(candidates) == 0 {
		sort.Strings(declared)
		return nil, fmt.Errorf("request body declares only media types outside the families openbindings.openapi@1 defines a request carriage for (declared: %s)", strings.Join(declared, ", "))
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].parsed.identity < candidates[j].parsed.identity })
	plans := make([]*bodyPlan, 0, len(candidates))
	var rejected []string
	for _, candidate := range candidates {
		plan, err := buildBodyPlan(rb, candidate.key, candidate.parsed, candidate.family)
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil, &degenerateMediaError{msg: strings.Join(rejected, "; ")}
	}
	return plans, nil
}

func buildBodyPlan(rb *openapi3.RequestBody, key string, parsed parsedMediaType, family string) (*bodyPlan, error) {
	plan := &bodyPlan{declared: true, required: rb.Required, mediaKey: key, mediaType: parsed.canonical, media: rb.Content[key], family: family}
	schema := mediaSchema(plan.media)
	flattens, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return nil, err
	}
	switch family {
	case familyJSON:
		plan.synthetic = schema != nil && !flattens
	case familyMultipart, familyURLEncoded:
		if schema != nil && !flattens {
			return nil, fmt.Errorf("request media candidate %s has a non-object body schema and is inadmissible", plan.mediaType)
		}
	case familyText:
		if schema != nil && flattens {
			return nil, fmt.Errorf("request media candidate text/plain has an object body schema and is inadmissible")
		}
		plan.synthetic = true
	}
	if !plan.synthetic {
		plan.props = props
	}
	return plan, nil
}

func mediaSchema(media *openapi3.MediaType) *openapi3.Schema {
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

func resolvedBodyShape(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) (bool, map[string]bool, error) {
	if schema == nil {
		return true, nil, nil // an absent schema is an open object declaration
	}
	if seen[schema] {
		return false, nil, nil
	}
	seen[schema] = true
	defer delete(seen, schema)
	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || schema.Not != nil {
		return false, nil, fmt.Errorf("conditional/combinatorial request schema has no single declaration-defined flattened surface in openbindings.openapi@1 revision 1")
	}
	props := map[string]bool{}
	object := schema.Type.Is("object") || schema.Properties != nil
	for name := range schema.Properties {
		props[name] = true
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		memberObject, memberProps, err := resolvedBodyShape(member.Value, seen)
		if err != nil {
			return false, nil, err
		}
		object = object || memberObject
		for name := range memberProps {
			props[name] = true
		}
	}
	if len(props) == 0 {
		props = nil
	}
	return object, props, nil
}

func candidateCollides(params openapi3.Parameters, plan *bodyPlan) bool {
	if plan == nil || !plan.declared {
		return false
	}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		name := ref.Value.Name
		if (plan.synthetic && name == syntheticBodyProperty) || (!plan.synthetic && plan.props[name]) {
			return true
		}
	}
	return false
}

func configuredRequestPlans(plans []*bodyPlan, bindCtx map[string]any) []*bodyPlan {
	cfg := openbindings.ContextConfiguration(bindCtx)
	raw, configured := cfg["requestMedia"]
	if !configured || raw == nil {
		return plans
	}
	wanted, ok := raw.(string)
	if !ok {
		return nil
	}
	parsedWanted, err := parseMediaType(wanted)
	if err != nil {
		return nil
	}
	for _, plan := range plans {
		parsed, err := parseMediaType(plan.mediaKey)
		if err == nil && parsed.identity == parsedWanted.identity {
			return []*bodyPlan{plan}
		}
	}
	return nil
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

type governingResponseMatch struct {
	key      string
	response *openapi3.Response
}

// governingResponse applies the OAS exact → class-range → default lookup for
// one actual status. The default entry may therefore govern a successful 2xx
// status when neither a literal nor 2XX entry exists. The matched key remains
// part of the result so failure completion can preserve which artifact
// declaration described the native response.
func governingResponse(op *openapi3.Operation, status int) *governingResponseMatch {
	if op == nil || op.Responses == nil {
		return nil
	}
	responses := op.Responses.Map()
	for _, key := range []string{fmt.Sprintf("%d", status), fmt.Sprintf("%dXX", status/100), "default"} {
		if ref := responses[key]; ref != nil && ref.Value != nil {
			return &governingResponseMatch{key: key, response: ref.Value}
		}
	}
	return nil
}

// governingResponseMedia selects the one concrete declaration in the
// governing Response Object whose type/subtype and declared parameters are
// a subset of the actual Content-Type. Greatest parameter specificity wins;
// a tie is ambiguous and loud.
func governingResponseMedia(response *openapi3.Response, actual string) (parsedMediaType, error) {
	if response == nil || len(response.Content) == 0 {
		return parsedMediaType{}, fmt.Errorf("the governing response declares no concrete media")
	}
	parsedActual, err := parseMediaType(actual)
	if err != nil {
		return parsedMediaType{}, fmt.Errorf("response Content-Type: %w", err)
	}
	identities := map[string]string{}
	bestSpecificity := -1
	var matches []parsedMediaType
	for key := range response.Content {
		declared, err := parseMediaType(key)
		if err != nil {
			continue
		}
		if previous, exists := identities[declared.identity]; exists {
			return parsedMediaType{}, fmt.Errorf("response content declarations %q and %q denote the same parsed media type", previous, key)
		}
		identities[declared.identity] = key
		if declared.base != parsedActual.base {
			continue
		}
		matchesParams := true
		for name, value := range declared.params {
			if parsedActual.params[name] != value {
				matchesParams = false
				break
			}
		}
		if !matchesParams {
			continue
		}
		specificity := len(declared.params)
		if specificity > bestSpecificity {
			bestSpecificity, matches = specificity, []parsedMediaType{declared}
		} else if specificity == bestSpecificity {
			matches = append(matches, declared)
		}
	}
	if len(matches) == 0 {
		return parsedMediaType{}, fmt.Errorf("response Content-Type %q matches no concrete media in the governing response", actual)
	}
	if len(matches) != 1 {
		return parsedMediaType{}, fmt.Errorf("response Content-Type %q ambiguously matches %d equally specific declarations", actual, len(matches))
	}
	return matches[0], nil
}

// successMediaTypes returns the declared concrete media types that may govern
// a successful response: literal 2xx, 2XX, and default declarations. Members
// retain declaration parameters; ordering is an implementation convention.
func successMediaTypes(op *openapi3.Operation) []string {
	if op == nil || op.Responses == nil {
		return nil
	}
	seen := map[string]bool{}
	identities := map[string]bool{}
	responses := op.Responses.Map()
	_, hasRange := responses["2XX"]
	exactSuccesses := 0
	for key := range responses {
		if len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9' {
			exactSuccesses++
		}
	}
	for key, ref := range responses {
		defaultCanGovernSuccess := key == "default" && !hasRange && exactSuccesses < 100
		if !(isSuccessResponseKey(key) || defaultCanGovernSuccess) || ref == nil || ref.Value == nil {
			continue
		}
		for mt := range ref.Value.Content {
			parsed, err := parseMediaType(mt)
			if err != nil {
				continue
			}
			if identities[parsed.identity] {
				continue
			}
			identities[parsed.identity] = true
			seen[parsed.canonical] = true
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
// operation's success responses. Empty membership means header omission.
func acceptHeader(op *openapi3.Operation) string {
	types := successMediaTypes(op)
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
