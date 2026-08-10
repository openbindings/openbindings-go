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
	"net/url"
	"sort"
	"strings"

	"github.com/dlclark/regexp2"
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

// isMediaRange reports a media range (*/*, application/*). Revisions 1 and 2
// exclude ranges. Revision 3 can select one only after requestMedia supplies
// the concrete type that will actually ride the wire.
func isMediaRange(mt string) bool {
	parts := strings.Split(mt, "/")
	return len(parts) == 2 && parts[1] == "*" && parts[0] != ""
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
	familyOctets     = "octets"
)

// bodyPlan is the pre-dispatch answer to the request-carriage questions: the
// selected media type, its family, and the flatten mode its schema implies
// (§9.1: object schemas flatten by property name; non-object schemas ride
// the synthetic `body` property, unwrapped at the wire).
type bodyPlan struct {
	declared    bool
	required    bool
	mediaKey    string // the declared content key, verbatim
	mediaType   string // normalized type/subtype (the request Content-Type)
	media       *openapi3.MediaType
	family      string
	synthetic   bool
	wholeObject bool            // complete body rides under one protocol-neutral application field
	props       map[string]bool // declared top-level body property names (object mode)
	mediaRange  bool            // mediaKey is a revision-3 media-range declaration
	rawBoundary bool            // caller string is Base64 boundary carriage for raw bytes
	bindingSpec string
	oas30       bool
}

type parsedMediaType struct {
	base             string
	params           map[string]string
	renderedParams   map[string]string
	canonical        string
	identity         string
	semanticIdentity string
	rangeSpecificity int // 2 exact, 1 type/*, 0 */*
}

func parseMediaType(raw string) (parsedMediaType, error) {
	return parseLegacyMediaIdentity(raw, false)
}

func parseMediaDeclaration(raw string) (parsedMediaType, error) {
	return parseHTTPMediaIdentity(raw, true)
}

func parseRevision3MediaType(raw string) (parsedMediaType, error) {
	return parseHTTPMediaIdentity(raw, false)
}

func parseLegacyMediaIdentity(raw string, allowRange bool) (parsedMediaType, error) {
	base, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return parsedMediaType{}, fmt.Errorf("invalid media type %q: %w", raw, err)
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" {
		return parsedMediaType{}, fmt.Errorf("invalid media type %q", raw)
	}
	rangeSpecificity := 2
	if strings.Contains(base, "*") {
		parts := strings.Split(base, "/")
		validRange := len(parts) == 2 && parts[0] != "" &&
			((parts[0] == "*" && parts[1] == "*") || (parts[0] != "*" && parts[1] == "*"))
		if !allowRange || !validRange {
			return parsedMediaType{}, fmt.Errorf("media type %q is not concrete", raw)
		}
		if parts[0] == "*" {
			rangeSpecificity = 0
		} else {
			rangeSpecificity = 1
		}
	}
	if !allowRange && rangeSpecificity != 2 {
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
	return parsedMediaType{base: base, params: normalizedParams, renderedParams: normalizedParams, canonical: canonical, identity: identity, semanticIdentity: identity, rangeSpecificity: rangeSpecificity}, nil
}

func parseHTTPMediaIdentity(raw string, allowRange bool) (parsedMediaType, error) {
	parts, err := splitHTTPMediaType(raw)
	if err != nil {
		return parsedMediaType{}, err
	}
	base := strings.ToLower(strings.Trim(parts[0], " \t"))
	baseParts := strings.Split(base, "/")
	if len(baseParts) != 2 || !httpToken(baseParts[0]) || !httpToken(baseParts[1]) {
		return parsedMediaType{}, fmt.Errorf("media type %q has an invalid type/subtype", raw)
	}
	structuralRange := baseParts[0] == "*" || baseParts[1] == "*"
	rangeSpecificity := 2
	if structuralRange {
		validRange := baseParts[1] == "*" && (baseParts[0] != "*" || base == "*/*")
		if !allowRange || !validRange {
			return parsedMediaType{}, fmt.Errorf("media type %q is not concrete", raw)
		}
		if base == "*/*" {
			rangeSpecificity = 0
		} else {
			rangeSpecificity = 1
		}
	}

	params := make(map[string]string, len(parts)-1)
	rendered := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		part = strings.TrimLeft(part, " \t")
		if strings.Trim(part, " \t") == "" {
			continue
		}
		equals := strings.IndexByte(part, '=')
		if equals <= 0 {
			return parsedMediaType{}, fmt.Errorf("invalid media-type parameter in %q", raw)
		}
		namePart := part[:equals]
		rawValue := part[equals+1:]
		if namePart != strings.TrimRight(namePart, " \t") || strings.HasPrefix(rawValue, " ") || strings.HasPrefix(rawValue, "\t") {
			return parsedMediaType{}, fmt.Errorf("invalid whitespace around media-type parameter '=' in %q", raw)
		}
		name := strings.ToLower(namePart)
		if !httpToken(name) {
			return parsedMediaType{}, fmt.Errorf("invalid media-type parameter name in %q", raw)
		}
		if _, duplicate := params[name]; duplicate {
			return parsedMediaType{}, fmt.Errorf("duplicate media-type parameter %q", name)
		}
		value, err := parseHTTPParameterValue(strings.TrimRight(rawValue, " \t"))
		if err != nil {
			return parsedMediaType{}, fmt.Errorf("invalid media-type parameter %q in %q: %w", name, raw, err)
		}
		rendered[name] = value
		params[name] = mediaParameterIdentityValue(name, value)
	}
	keys := sortedMediaParameterKeys(params)
	identity := base
	for _, key := range keys {
		identity += "\x00" + key + "=" + params[key]
	}
	canonical := formatHTTPMediaType(base, rendered)
	return parsedMediaType{
		base: base, params: params, renderedParams: rendered, canonical: canonical,
		identity: identity, semanticIdentity: identity, rangeSpecificity: rangeSpecificity,
	}, nil
}

func splitHTTPMediaType(raw string) ([]string, error) {
	parts := make([]string, 0, 4)
	start, quoted, escaped := 0, false, false
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		switch {
		case escaped:
			escaped = false
		case quoted && char == '\\':
			escaped = true
		case char == '"':
			quoted = !quoted
		case char == ';' && !quoted:
			parts = append(parts, raw[start:index])
			start = index + 1
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoted media-type parameter in %q", raw)
	}
	parts = append(parts, raw[start:])
	return parts, nil
}

func parseHTTPParameterValue(raw string) (string, error) {
	if !strings.HasPrefix(raw, `"`) {
		if !httpToken(raw) {
			return "", fmt.Errorf("invalid unquoted value")
		}
		return raw, nil
	}
	if len(raw) < 2 || !strings.HasSuffix(raw, `"`) {
		return "", fmt.Errorf("invalid quoted value")
	}
	inner := raw[1 : len(raw)-1]
	var out strings.Builder
	out.Grow(len(inner))
	for index := 0; index < len(inner); index++ {
		char := inner[index]
		if char == '\\' {
			index++
			if index >= len(inner) || !httpQuotedPairByte(inner[index]) {
				return "", fmt.Errorf("invalid quoted-pair")
			}
			out.WriteByte(inner[index])
			continue
		}
		if !httpQuotedTextByte(char) {
			return "", fmt.Errorf("invalid quoted-string byte")
		}
		out.WriteByte(char)
	}
	return out.String(), nil
}

func httpToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if !httpTokenByte(value[index]) {
			return false
		}
	}
	return true
}

func httpTokenByte(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
		strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char))
}

func httpQuotedTextByte(char byte) bool {
	return char == '\t' || char == ' ' || char == '!' || char >= '#' && char <= '[' || char >= ']' && char <= '~' || char >= 0x80
}

func httpQuotedPairByte(char byte) bool {
	return char == '\t' || char == ' ' || char >= '!' && char <= '~' || char >= 0x80
}

func formatHTTPMediaType(base string, params map[string]string) string {
	var out strings.Builder
	out.WriteString(base)
	for _, key := range sortedMediaParameterKeys(params) {
		out.WriteString("; ")
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(formatHTTPParameter(params[key]))
	}
	return out.String()
}

func formatHTTPParameter(value string) string {
	if httpToken(value) {
		return value
	}
	var out strings.Builder
	out.WriteByte('"')
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' || value[index] == '"' {
			out.WriteByte('\\')
		}
		out.WriteByte(value[index])
	}
	out.WriteByte('"')
	return out.String()
}

func sortedMediaParameterKeys(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type mediaParameterSemantics struct {
	normalize func(string) (string, bool)
	equal     func(string, string) bool
}

func mediaParameterSemanticsFor(name string) (mediaParameterSemantics, bool) {
	switch strings.ToLower(name) {
	case "charset":
		return mediaParameterSemantics{
			normalize: func(value string) (string, bool) { return strings.ToLower(value), true },
			equal:     strings.EqualFold,
		}, true
	case "type":
		return mediaParameterSemantics{normalize: func(value string) (string, bool) {
			parsed, err := parseRevision3MediaType(value)
			if err != nil {
				return "", false
			}
			return parsed.semanticIdentity, true
		}, equal: func(left, right string) bool {
			leftParsed, leftErr := parseRevision3MediaType(left)
			rightParsed, rightErr := parseRevision3MediaType(right)
			return leftErr == nil && rightErr == nil && leftParsed.semanticIdentity == rightParsed.semanticIdentity
		}}, true
	default:
		return mediaParameterSemantics{}, false
	}
}

// Known parameter names use their registered semantics; every unknown name
// remains bytewise after HTTP quoted-string unescaping. This is deliberately
// an internal, extensible seam rather than a universal vocabulary claim.
func mediaParameterIdentityValue(name, value string) string {
	if semantics, known := mediaParameterSemanticsFor(name); known {
		if normalized, ok := semantics.normalize(value); ok {
			return normalized
		}
	}
	return value
}

func mediaParameterValuesEqual(name, left, right string) bool {
	if semantics, known := mediaParameterSemanticsFor(name); known {
		if _, leftOK := semantics.normalize(left); leftOK {
			if _, rightOK := semantics.normalize(right); rightOK {
				return semantics.equal(left, right)
			}
		}
	}
	return left == right
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
	return planRequestBodiesFor(nil, op, BindingSpecV2)
}

// planRequestBodiesFor preserves the immutable revision-1/2 candidate set and
// adds revision-3's declaration-led raw-octet and media-range candidates.
// Range plans are synthesis/runtime skeletons: their concrete family and
// emitted Content-Type are materialized from requestMedia at invocation.
func planRequestBodiesFor(doc *openapi3.T, op *openapi3.Operation, bindingSpec string) ([]*bodyPlan, error) {
	if !hasRequestBody(op) {
		return nil, nil
	}
	rb := op.RequestBody.Value
	if len(rb.Content) == 0 {
		return nil, nil
	}

	type candidate struct {
		key         string
		parsed      parsedMediaType
		family      string
		rawBoundary bool
		mediaRange  bool
	}
	var candidates []candidate
	var declared []string
	var rejected []string
	identities := map[string]string{}
	for key := range rb.Content {
		var parsed parsedMediaType
		var err error
		if hasMediaFidelity(bindingSpec) {
			parsed, err = parseMediaDeclaration(key)
		} else {
			parsed, err = parseMediaType(key)
		}
		if err != nil {
			declared = append(declared, key)
			continue
		}
		declared = append(declared, parsed.canonical)
		identity := parsed.identity
		if hasMediaFidelity(bindingSpec) {
			identity = parsed.semanticIdentity
		}
		if previous, exists := identities[identity]; exists {
			return nil, fmt.Errorf("request content declarations %q and %q denote the same parsed media type (OAPI-P-04 normalized collision)", previous, key)
		}
		identities[identity] = key
		media := rb.Content[key]
		if parsed.rangeSpecificity < 2 {
			family := representativeRangeFamily(parsed)
			if family != "" {
				candidates = append(candidates, candidate{key: key, parsed: parsed, family: family, mediaRange: true})
			}
			continue
		}
		family, rawBoundary, carriageErr := exactRequestFamily(doc, parsed, media, bindingSpec)
		if carriageErr != nil {
			rejected = append(rejected, fmt.Sprintf("request media candidate %s is inadmissible: %v", parsed.canonical, carriageErr))
			continue
		}
		if family != "" {
			candidates = append(candidates, candidate{key: key, parsed: parsed, family: family, rawBoundary: rawBoundary})
		}
	}
	if len(candidates) == 0 {
		if len(rejected) > 0 {
			return nil, &degenerateMediaError{msg: strings.Join(rejected, "; ")}
		}
		sort.Strings(declared)
		return nil, fmt.Errorf("request body declares only media types outside the families openbindings.openapi@1 defines a request carriage for (declared: %s)", strings.Join(declared, ", "))
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].parsed.identity < candidates[j].parsed.identity })
	plans := make([]*bodyPlan, 0, len(candidates))
	for _, candidate := range candidates {
		media := rb.Content[candidate.key]
		wholeJSON := hasWholeJSONCarriage(bindingSpec) && !candidate.mediaRange && candidate.family == familyJSON &&
			requiresWholeJSONCarriage(mediaSchema(media), map[*openapi3.Schema]bool{})
		var plan *bodyPlan
		var err error
		if wholeJSON {
			plan = &bodyPlan{
				declared:    true,
				required:    rb.Required,
				mediaKey:    candidate.key,
				mediaType:   candidate.parsed.canonical,
				media:       media,
				family:      familyJSON,
				wholeObject: true,
			}
		} else {
			plan, err = buildBodyPlanFromMedia(rb, candidate.key, candidate.parsed, candidate.family, media)
		}
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		plan.mediaRange = candidate.mediaRange
		plan.rawBoundary = candidate.rawBoundary
		plan.bindingSpec = bindingSpec
		plan.oas30 = doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
		applyRevision3BodyShape(plan)
		if hasDynamicObjectCarriage(bindingSpec) {
			applyDynamicObjectShape(plan)
		}
		if candidate.mediaRange {
			// A range is never a Content-Type. The representative family exists
			// only to project its declaration-shaped operation surface.
			plan.mediaType = ""
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil, &degenerateMediaError{msg: strings.Join(rejected, "; ")}
	}
	return plans, nil
}

// requiresWholeJSONCarriage identifies top-level JSON Schema applicators
// whose complete validation or possible object surface cannot survive a
// projection to finitely named body properties. Only allOf is traversed:
// applicators inside a named property do not change the top-level route.
func requiresWholeJSONCarriage(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || schema.Not != nil ||
		schema.If != nil || schema.Then != nil || schema.Else != nil || len(schema.DependentSchemas) > 0 ||
		explicitDynamicAdditionalProperties(schema.UnevaluatedProperties) {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && requiresWholeJSONCarriage(member.Value, seen) {
			return true
		}
	}
	return false
}

func applyDynamicObjectShape(plan *bodyPlan) {
	if plan == nil || plan.synthetic || !hasExplicitDynamicProperties(mediaSchema(plan.media), map[*openapi3.Schema]bool{}) {
		return
	}
	plan.wholeObject = true
	plan.props = nil
}

func hasExplicitDynamicProperties(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if len(schema.PatternProperties) > 0 || explicitDynamicAdditionalProperties(schema.AdditionalProperties) {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && hasExplicitDynamicProperties(member.Value, seen) {
			return true
		}
	}
	return false
}

func explicitDynamicAdditionalProperties(additional openapi3.AdditionalProperties) bool {
	if additional.Has != nil {
		return *additional.Has
	}
	if additional.Schema == nil || additional.Schema.Value == nil {
		return false
	}
	if literal, boolean := booleanSchemaLiteral(additional.Schema.Value); boolean {
		return literal
	}
	return true
}

func buildBodyPlan(rb *openapi3.RequestBody, key string, parsed parsedMediaType, family string) (*bodyPlan, error) {
	return buildBodyPlanFromMedia(rb, key, parsed, family, rb.Content[key])
}

func buildBodyPlanFromMedia(rb *openapi3.RequestBody, key string, parsed parsedMediaType, family string, media *openapi3.MediaType) (*bodyPlan, error) {
	plan := &bodyPlan{declared: true, required: rb.Required, mediaKey: key, mediaType: parsed.canonical, media: media, family: family}
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
	case familyOctets:
		plan.synthetic = true
	}
	if !plan.synthetic {
		plan.props = props
	}
	return plan, nil
}

func applyRevision3BodyShape(plan *bodyPlan) {
	if plan == nil || !hasMediaFidelity(plan.bindingSpec) || mediaSchema(plan.media) != nil {
		return
	}
	if plan.family == familyJSON {
		// No schema constrains the application value to an object. A whole-body
		// route preserves scalar, array, and object JSON values alike; treating
		// absence as an open object would lose two of those three shapes.
		plan.synthetic = true
		plan.props = nil
	}
}

func exactRequestFamily(doc *openapi3.T, parsed parsedMediaType, media *openapi3.MediaType, bindingSpec string) (string, bool, error) {
	switch {
	case isJSONMediaType(parsed.base):
		return familyJSON, false, nil
	case parsed.base == "multipart/form-data":
		if hasMediaFidelity(bindingSpec) {
			if err := validateRevision3MultipartMedia(doc, media); err != nil {
				return "", false, err
			}
		}
		return familyMultipart, false, nil
	case parsed.base == "application/x-www-form-urlencoded":
		if hasMediaFidelity(bindingSpec) && mediaSchema(media) == nil {
			return "", false, fmt.Errorf("schema-omitted form media has no revision-3 caller route")
		}
		if hasMediaFidelity(bindingSpec) {
			if err := validateRevision3URLEncodedMedia(doc, media); err != nil {
				return "", false, err
			}
		}
		return familyURLEncoded, false, nil
	case parsed.base == "text/plain":
		if hasMediaFidelity(bindingSpec) {
			if err := supportedTextCharset(parsed); err != nil {
				return "", false, err
			}
		}
		return familyText, false, nil
	}
	if !hasMediaFidelity(bindingSpec) || doc == nil {
		return "", false, nil
	}
	eligible, rawBoundary, err := octetRequestCarriage(doc, media)
	if err != nil {
		return "", false, err
	}
	if eligible {
		return familyOctets, rawBoundary, nil
	}
	return "", false, nil
}

func validateRevision3URLEncodedMedia(doc *openapi3.T, media *openapi3.MediaType) error {
	schema := mediaSchema(media)
	if schema == nil {
		return fmt.Errorf("schema-omitted urlencoded media has no revision-3 caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for name := range props {
		propertySchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		if literal, boolean := booleanSchemaLiteral(propertySchema); boolean {
			if !literal {
				continue
			}
			return fmt.Errorf("urlencoded property %q has an unconstrained boolean schema with no revision-3 octet boundary", name)
		}
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		openapiVersion := ""
		if doc != nil {
			openapiVersion = doc.OpenAPI
		}
		if legacyOpenAPIFormEncoding(openapiVersion) || encodingUsesSerialization(enc) {
			if err := validateMultipartSerializationMethod(name, propertySchema, enc); err != nil {
				return err
			}
			continue
		}
		contentType, err := revision3PartContentType(propertySchema, enc, is30)
		if err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
		if _, err := revision3PropertyCarriage(propertySchema, contentType, is30, false); err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
	}
	return nil
}

func validateRevision3MultipartMedia(doc *openapi3.T, media *openapi3.MediaType) error {
	schema := mediaSchema(media)
	if schema == nil {
		return fmt.Errorf("schema-omitted multipart media has no revision-3 caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for name := range props {
		partSchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		if literal, boolean := booleanSchemaLiteral(partSchema); boolean {
			if !literal {
				continue // an unsatisfiable property has no admissible runtime value
			}
			return fmt.Errorf("multipart part %q has an unconstrained boolean schema with no revision-3 octet boundary", name)
		}
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if enc != nil && len(enc.Headers) > 0 {
			return fmt.Errorf("multipart part %q declares encoding.headers, but this binding revision defines no caller source for dynamic part-header values", name)
		}
		if encodingUsesSerialization(enc) {
			if err := validateMultipartSerializationMethod(name, partSchema, enc); err != nil {
				return err
			}
			continue
		}
		contentSchema := partSchema
		if schemaTypeIs(partSchema, "array", map[*openapi3.Schema]bool{}) {
			contentSchema = resolvedMultipartItems(partSchema, map[*openapi3.Schema]bool{})
			if schemaTypeIs(contentSchema, "array", map[*openapi3.Schema]bool{}) {
				return fmt.Errorf("multipart part %q has nested array items with no revision-3 repeated-part mapping", name)
			}
		}
		contentType, err := revision3PartContentType(contentSchema, enc, is30)
		if err != nil {
			return fmt.Errorf("multipart part %q: %w", name, err)
		}
		if _, err := revision3PropertyCarriage(contentSchema, contentType, is30, true); err != nil {
			return fmt.Errorf("multipart part %q: %w", name, err)
		}
	}
	return nil
}

func encodingUsesSerialization(enc *openapi3.Encoding) bool {
	if enc == nil {
		return false
	}
	return enc.Style != "" || enc.Explode != nil || enc.AllowReserved
}

// OAS 3.0.0 through 3.0.3 apply the Encoding Object's form/explode defaults
// to urlencoded properties even when no Encoding Object is written. OAS
// 3.0.4 added the content-based interpretation as a compatibility
// recommendation, and the 3.1 line uses it when all three RFC6570 controls
// are absent. Revision 3 incorporates each accepted edition's own immutable
// text, so an older artifact must retain its older default.
func legacyOpenAPIFormEncoding(version string) bool {
	switch version {
	case "3.0.0", "3.0.1", "3.0.2", "3.0.3":
		return true
	default:
		return false
	}
}

func validateMultipartSerializationMethod(name string, schema *openapi3.Schema, enc *openapi3.Encoding) error {
	method := revision3EncodingSerializationMethod(enc)
	switch method.Style {
	case openapi3.SerializationForm:
		return nil
	case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
		if method.Explode || !schemaTypeIs(schema, "array", map[*openapi3.Schema]bool{}) {
			return fmt.Errorf("multipart part %q style %q is defined only for arrays with explode=false", name, method.Style)
		}
		return nil
	case openapi3.SerializationDeepObject:
		if !method.Explode || !schemaTypeIs(schema, "object", map[*openapi3.Schema]bool{}) {
			return fmt.Errorf("multipart part %q style deepObject is defined only for objects with explode=true", name)
		}
		return nil
	default:
		return fmt.Errorf("multipart part %q declares unsupported encoding style %q", name, method.Style)
	}
}

func revision3EncodingSerializationMethod(enc *openapi3.Encoding) openapi3.SerializationMethod {
	style := openapi3.SerializationForm
	if enc != nil && enc.Style != "" {
		style = enc.Style
	}
	explode := style == openapi3.SerializationForm
	if enc != nil && enc.Explode != nil {
		explode = *enc.Explode
	}
	return openapi3.SerializationMethod{Style: style, Explode: explode}
}

func revision3PartContentType(schema *openapi3.Schema, enc *openapi3.Encoding, is30 bool) (parsedMediaType, error) {
	if schema == nil {
		return parsedMediaType{}, fmt.Errorf("an absent part schema defaults to application/octet-stream, but this binding revision defines no JSON-to-octet boundary")
	}
	if _, boolean := booleanSchemaLiteral(schema); boolean {
		return parsedMediaType{}, fmt.Errorf("an unconstrained boolean part schema has no revision-3 octet boundary")
	}
	contentEncoding, encodingConflict := resolvedSchemaKeywordString(schema, "contentEncoding")
	if encodingConflict {
		return parsedMediaType{}, fmt.Errorf("resolved schema declares conflicting contentEncoding values")
	}
	if contentEncoding != "" && !httpToken(contentEncoding) {
		return parsedMediaType{}, fmt.Errorf("contentEncoding %q is not a valid HTTP token", contentEncoding)
	}
	contentMediaType, mediaConflict := resolvedSchemaKeywordString(schema, "contentMediaType")
	if mediaConflict {
		return parsedMediaType{}, fmt.Errorf("resolved schema declares conflicting contentMediaType values")
	}
	if !is30 && (contentEncoding != "" || contentMediaType != "") && !schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) {
		return parsedMediaType{}, fmt.Errorf("contentEncoding/contentMediaType requires a resolved string schema")
	}

	declared := ""
	if enc != nil && enc.ContentType != "" {
		members, err := splitHTTPList(enc.ContentType)
		if err != nil {
			return parsedMediaType{}, fmt.Errorf("invalid encoding.contentType: %w", err)
		}
		if len(members) != 1 {
			return parsedMediaType{}, fmt.Errorf("encoding.contentType has %d members; this binding revision defines no per-part member selection rule", len(members))
		}
		declared = members[0]
	} else {
		var ok bool
		declared, ok = defaultRevision3PartContentType(schema, is30)
		if !ok {
			return parsedMediaType{}, fmt.Errorf("typeless part schema defaults to application/octet-stream, but this binding revision defines no JSON-to-octet boundary")
		}
	}
	parsed, err := parseRevision3MediaType(declared)
	if err != nil {
		return parsedMediaType{}, fmt.Errorf("part Content-Type %q is not one supported concrete media type: %w", declared, err)
	}
	if err := supportedTextCharset(parsed); err != nil {
		return parsedMediaType{}, err
	}
	return parsed, nil
}

func defaultRevision3PartContentType(schema *openapi3.Schema, is30 bool) (string, bool) {
	switch {
	case schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && is30 && binarySignaled(schema, true):
		return "application/octet-stream", true
	case schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && !is30 && schemaHasContentEncoding(schema):
		return "application/octet-stream", true
	case schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}),
		schemaTypeIs(schema, "number", map[*openapi3.Schema]bool{}),
		schemaTypeIs(schema, "integer", map[*openapi3.Schema]bool{}),
		schemaTypeIs(schema, "boolean", map[*openapi3.Schema]bool{}):
		return "text/plain", true
	case schemaTypeIs(schema, "object", map[*openapi3.Schema]bool{}), schemaTypeIs(schema, "array", map[*openapi3.Schema]bool{}):
		return "application/json", true
	default:
		return "", false
	}
}

type revision3PropertyCarriageMode uint8

const (
	revision3PropertyJSON revision3PropertyCarriageMode = iota
	revision3PropertyText
	revision3PropertyRaw30
	revision3PropertyEncoded31
)

func revision3PropertyCarriage(schema *openapi3.Schema, contentType parsedMediaType, is30, allowRaw30 bool) (revision3PropertyCarriageMode, error) {
	if !is30 && schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && schemaHasContentEncoding(schema) {
		return revision3PropertyEncoded31, nil
	}
	if isJSONMediaType(contentType.base) {
		return revision3PropertyJSON, nil
	}
	if is30 && schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && binarySignaled(schema, true) {
		if allowRaw30 {
			return revision3PropertyRaw30, nil
		}
		return 0, fmt.Errorf("OAS 3.0 binary has no revision-3 urlencoded octet boundary")
	}
	if contentType.base == "text/plain" {
		if schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "number", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "integer", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "boolean", map[*openapi3.Schema]bool{}) {
			return revision3PropertyText, nil
		}
	}
	return 0, fmt.Errorf("Content-Type %q has no revision-3 native property serializer", contentType.canonical)
}

func schemaHasContentEncoding(schema *openapi3.Schema) bool {
	value, conflict := resolvedSchemaKeywordString(schema, "contentEncoding")
	return !conflict && value != ""
}

func splitHTTPList(raw string) ([]string, error) {
	var members []string
	start, quoted, escaped := 0, false, false
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		switch {
		case escaped:
			escaped = false
		case quoted && char == '\\':
			escaped = true
		case char == '"':
			quoted = !quoted
		case char == ',' && !quoted:
			member := strings.Trim(raw[start:index], " \t")
			if member == "" {
				return nil, fmt.Errorf("empty list member")
			}
			members = append(members, member)
			start = index + 1
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	member := strings.Trim(raw[start:], " \t")
	if member == "" {
		return nil, fmt.Errorf("empty list member")
	}
	return append(members, member), nil
}

func supportedTextCharset(parsed parsedMediaType) error {
	charset, present := parsed.params["charset"]
	if !present {
		return nil
	}
	switch strings.ToLower(charset) {
	case "utf-8", "utf8", "us-ascii", "ascii", "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		return nil
	default:
		return fmt.Errorf("charset %q is not supported for request encoding", charset)
	}
}

func encodeTextString(value string, parsed parsedMediaType) ([]byte, error) {
	charset := "utf-8"
	if declared, present := parsed.params["charset"]; present {
		charset = strings.ToLower(declared)
	}
	switch charset {
	case "utf-8", "utf8":
		return []byte(value), nil
	case "us-ascii", "ascii":
		out := make([]byte, 0, len(value))
		for _, char := range value {
			if char > 0x7f {
				return nil, fmt.Errorf("value contains U+%04X, which is outside US-ASCII", char)
			}
			out = append(out, byte(char))
		}
		return out, nil
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		out := make([]byte, 0, len(value))
		for _, char := range value {
			if char > 0xff {
				return nil, fmt.Errorf("value contains U+%04X, which is outside ISO-8859-1", char)
			}
			out = append(out, byte(char))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("charset %q is not supported for request encoding", charset)
	}
}

func octetRequestCarriage(doc *openapi3.T, media *openapi3.MediaType) (bool, bool, error) {
	if doc == nil {
		return false, false, nil
	}
	schema := mediaSchema(media)
	if isOpenAPI30(majorMinor(doc.OpenAPI)) {
		if schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && binarySignaled(schema, true) {
			return true, true, nil
		}
		return false, false, nil
	}
	// OAS 3.1's schema-omitted, exact non-JSON declaration is the raw shape.
	// Its JSON-domain caller boundary is Base64. Conversely a declared
	// contentEncoding describes the application string itself; those encoded
	// characters ride the wire unchanged.
	if schema == nil {
		return true, true, nil
	}
	encoding, conflict := resolvedSchemaKeywordString(schema, "contentEncoding")
	if conflict {
		return false, false, fmt.Errorf("resolved schema declares conflicting contentEncoding values")
	}
	if schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && encoding != "" {
		return true, false, nil
	}
	return false, false, nil
}

func representativeRangeFamily(parsed parsedMediaType) string {
	// Every admitted wildcard range contains a JSON-family concrete subtype:
	// application/* contains application/json, and every type/* (including
	// */*) contains an x+json subtype. This gives the range a conservative
	// declaration-shaped skeleton without claiming that every selection is
	// raw, text, or JSON; runtime still materializes the configured lane.
	if parsed.rangeSpecificity < 2 {
		return familyJSON
	}
	return ""
}

func mediaSchema(media *openapi3.MediaType) *openapi3.Schema {
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

// responseUsesRawBoundary reports whether revision 4 carries the exact
// response octets as canonical Base64 at the protocol-independent operation
// boundary. JSON, text, and SSE retain their application-value lanes.
func responseUsesRawBoundary(doc *openapi3.T, media *openapi3.MediaType, actualContentType string) bool {
	actual, err := parseRevision3MediaType(actualContentType)
	if err != nil || isJSONMediaType(actual.base) || strings.HasPrefix(actual.base, "text/") {
		return false
	}
	if doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI)) {
		return binarySignaled(mediaSchema(media), true)
	}
	return media != nil && media.Schema == nil
}

func booleanSchemaLiteral(schema *openapi3.Schema) (bool, bool) {
	if schema == nil {
		return false, false
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false, false
	}
	var raw map[string]any
	if json.Unmarshal(encoded, &raw) != nil {
		return false, false
	}
	return structuralBooleanSchemaLiteral(raw)
}

func structuralBooleanSchemaLiteral(schema map[string]any) (bool, bool) {
	if len(schema) != 1 {
		return false, false
	}
	for keyword, literal := range map[string]bool{"anyOf": true, "allOf": false} {
		members, ok := schema[keyword].([]any)
		if !ok || len(members) != 2 {
			continue
		}
		first, firstOK := members[0].(map[string]any)
		second, secondOK := members[1].(map[string]any)
		if !firstOK || len(first) != 0 || !secondOK || len(second) != 1 {
			continue
		}
		not, notOK := second["not"].(map[string]any)
		if notOK && len(not) == 0 {
			return literal, true
		}
	}
	return false, false
}

func resolvedBodyShape(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) (bool, map[string]bool, error) {
	if schema == nil {
		return true, nil, nil // an absent schema is an open object declaration
	}
	if _, booleanSchema := booleanSchemaLiteral(schema); booleanSchema {
		return false, nil, nil
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
		if ((plan.synthetic || plan.wholeObject) && name == syntheticBodyProperty) ||
			(!plan.synthetic && !plan.wholeObject && plan.props[name]) {
			return true
		}
	}
	return false
}

func configuredRequestPlans(plans []*bodyPlan, bindCtx map[string]any) []*bodyPlan {
	selected, _ := configuredRequestPlansFor(nil, nil, plans, bindCtx, BindingSpecV2)
	return selected
}

func configuredRequestPlansFor(doc *openapi3.T, op *openapi3.Operation, plans []*bodyPlan, bindCtx map[string]any, bindingSpec string) ([]*bodyPlan, error) {
	cfg := openbindings.ContextConfiguration(bindCtx)
	raw, configured := cfg["requestMedia"]
	if !configured || raw == nil {
		if !hasMediaFidelity(bindingSpec) {
			return plans, nil
		}
		concrete := make([]*bodyPlan, 0, len(plans))
		hasRange := false
		for _, plan := range plans {
			if plan.mediaRange {
				hasRange = true
				continue
			}
			concrete = append(concrete, plan)
		}
		if len(concrete) == 0 && hasRange {
			if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
				return nil, fmt.Errorf("OpenAPI request media range requires configuration.requestMedia before this supplied optional body can be dispatched")
			}
			return nil, &configRequired{
				point:       "requestMedia",
				key:         "value",
				description: "OpenAPI request media range requires a concrete requestMedia selection",
			}
		}
		return concrete, nil
	}
	wanted, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("configuration.requestMedia must be a concrete media-type string")
	}
	var parsedWanted parsedMediaType
	var err error
	if hasMediaFidelity(bindingSpec) {
		parsedWanted, err = parseRevision3MediaType(wanted)
	} else {
		parsedWanted, err = parseMediaType(wanted)
	}
	if err != nil {
		return nil, fmt.Errorf("configuration.requestMedia: %w", err)
	}
	if hasMediaFidelity(bindingSpec) {
		return selectRevision3RequestPlan(doc, op, plans, parsedWanted, bindingSpec)
	}
	for _, plan := range plans {
		parsed, err := parseMediaType(plan.mediaKey)
		if err == nil && parsed.identity == parsedWanted.identity {
			return []*bodyPlan{plan}, nil
		}
	}
	return nil, nil
}

func selectRevision3RequestPlan(doc *openapi3.T, op *openapi3.Operation, plans []*bodyPlan, wanted parsedMediaType, bindingSpecs ...string) ([]*bodyPlan, error) {
	bindingSpec := BindingSpecV3
	if len(bindingSpecs) > 0 {
		bindingSpec = bindingSpecs[0]
	}
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil, nil
	}
	type match struct {
		key         string
		declaration parsedMediaType
	}
	var best []match
	bestSpecificity, bestParams := -1, -1
	keys := make([]string, 0, len(op.RequestBody.Value.Content))
	for key := range op.RequestBody.Value.Content {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		declaration, err := parseMediaDeclaration(key)
		if err != nil || !requestMediaDeclarationMatches(declaration, wanted) {
			continue
		}
		specificity, parameterCount := declaration.rangeSpecificity, len(declaration.params)
		switch {
		case specificity > bestSpecificity || (specificity == bestSpecificity && parameterCount > bestParams):
			bestSpecificity, bestParams = specificity, parameterCount
			best = []match{{key: key, declaration: declaration}}
		case specificity == bestSpecificity && parameterCount == bestParams:
			best = append(best, match{key: key, declaration: declaration})
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("configured requestMedia %q matches no declared request media", wanted.canonical)
	}
	if len(best) > 1 {
		matched := make([]string, len(best))
		for i, candidate := range best {
			matched[i] = candidate.declaration.canonical
		}
		return nil, fmt.Errorf("configured requestMedia %q ambiguously matches equally specific declarations %s", wanted.canonical, strings.Join(matched, ", "))
	}
	selected := best[0]
	var skeleton *bodyPlan
	for _, plan := range plans {
		if plan.mediaKey == selected.key {
			skeleton = plan
			break
		}
	}
	if skeleton == nil {
		return nil, fmt.Errorf("configured requestMedia %q selects declaration %q, which has no revision-3 carriage", wanted.canonical, selected.key)
	}
	if !skeleton.mediaRange {
		copy := *skeleton
		copy.mediaType = wanted.canonical
		return []*bodyPlan{&copy}, nil
	}
	family, rawBoundary, carriageErr := exactRequestFamily(doc, wanted, op.RequestBody.Value.Content[selected.key], bindingSpec)
	if carriageErr != nil {
		return nil, carriageErr
	}
	if family == "" {
		return nil, fmt.Errorf("configured requestMedia %q selects range %q but no existing request carriage family supports that concrete type", wanted.canonical, selected.key)
	}
	plan, err := buildBodyPlanFromMedia(op.RequestBody.Value, selected.key, wanted, family, op.RequestBody.Value.Content[selected.key])
	if err != nil {
		return nil, err
	}
	plan.mediaRange = true
	plan.mediaType = wanted.canonical
	plan.rawBoundary = rawBoundary
	plan.bindingSpec = bindingSpec
	plan.oas30 = doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	applyRevision3BodyShape(plan)
	if hasDynamicObjectCarriage(bindingSpec) {
		applyDynamicObjectShape(plan)
	}
	if plan.synthetic != skeleton.synthetic || plan.wholeObject != skeleton.wholeObject {
		return nil, fmt.Errorf("configured requestMedia %q selects range %q with no single revision-3 routed body shape", wanted.canonical, selected.key)
	}
	return []*bodyPlan{plan}, nil
}

func requestMediaDeclarationMatches(declaration, concrete parsedMediaType) bool {
	if declaration.rangeSpecificity == 2 {
		if declaration.base != concrete.base {
			return false
		}
	} else if !mediaRangeBaseMatches(declaration.base, concrete.base) {
		return false
	}
	for key, value := range declaration.params {
		actual, present := concrete.params[key]
		if !present || !mediaParameterValuesEqual(key, actual, value) {
			return false
		}
	}
	return true
}

func mediaRangeBaseMatches(declarationBase, concreteBase string) bool {
	if declarationBase == concreteBase {
		return true
	}
	parts := strings.Split(declarationBase, "/")
	concrete := strings.Split(concreteBase, "/")
	if len(parts) != 2 || len(concrete) != 2 {
		return false
	}
	return parts[1] == "*" && (parts[0] == "*" || parts[0] == concrete[0])
}

func requestMediaUnconfigured(bindCtx map[string]any) bool {
	raw, configured := openbindings.ContextConfiguration(bindCtx)["requestMedia"]
	return !configured || raw == nil
}

func onlyRangePlans(plans []*bodyPlan) bool {
	hasRange := false
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if !plan.mediaRange {
			return false
		}
		hasRange = true
	}
	return hasRange
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
		if plan.synthetic || plan.wholeObject {
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
		fields, err := objectBodyFields(plan, routed)
		if err != nil {
			return nil, "", err
		}
		if len(fields) == 0 && !plan.required {
			return nil, "", nil
		}
		return buildMultipartBodyForMediaType(doc, plan.media, fields, plan.bindingSpec, plan.mediaType)
	case familyURLEncoded:
		fields, err := objectBodyFields(plan, routed)
		if err != nil {
			return nil, "", err
		}
		if len(fields) == 0 && !plan.required {
			return nil, "", nil
		}
		body, err := buildURLEncodedBodyForRevision(doc, plan.media, fields, plan.bindingSpec)
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
		if hasMediaFidelity(plan.bindingSpec) {
			parsed, err := parseRevision3MediaType(plan.mediaType)
			if err != nil {
				return nil, "", err
			}
			encoded, err := encodeTextString(s, parsed)
			if err != nil {
				return nil, "", fmt.Errorf("request media %s: %w", plan.mediaType, err)
			}
			return bytes.NewReader(encoded), plan.mediaType, nil
		}
		return strings.NewReader(s), plan.mediaType, nil
	case familyOctets:
		if !routed.bodySet {
			return nil, "", nil
		}
		s, ok := routed.bodyValue.(string)
		if !ok {
			return nil, "", fmt.Errorf("request media %s requires a string body value, got %T", plan.mediaType, routed.bodyValue)
		}
		if !plan.rawBoundary {
			// OAS 3.1 contentEncoding describes an encoded application
			// string. The representation rides unchanged; decoding here would
			// replace the artifact's semantics with a binding convention.
			return strings.NewReader(s), plan.mediaType, nil
		}
		data, err := canonicalBase64BoundaryBytes(syntheticBodyProperty, s)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(data), plan.mediaType, nil
	}
	return nil, "", fmt.Errorf("unknown body family %q", plan.family)
}

// ---------------------------------------------------------------------------
// Multipart (OAPI-P-04's part-encoding rules)
// ---------------------------------------------------------------------------

// buildMultipartBody encodes body fields as multipart/form-data. Revisions 1
// and 2 retain their legacy edition-aware binary decoder. Revision 3 instead
// treats only OAS 3.0 format:binary as a canonical Base64 raw-byte boundary;
// OAS 3.1 contentEncoding/contentMediaType strings ride as artifact text.
// Other parts follow the artifact's encoding object or OAS per-type defaults.
// Fields are written in sorted order for a deterministic body.
func buildMultipartBody(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any) (io.Reader, string, error) {
	return buildMultipartBodyForRevision(doc, media, fields, BindingSpecV2)
}

func buildMultipartBodyForRevision(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any, bindingSpec string) (io.Reader, string, error) {
	return buildMultipartBodyForMediaType(doc, media, fields, bindingSpec, "multipart/form-data")
}

func buildMultipartBodyForMediaType(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any, bindingSpec, selectedMediaType string) (io.Reader, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	var selected parsedMediaType
	if hasMediaFidelity(bindingSpec) {
		var err error
		selected, err = parseRevision3MediaType(selectedMediaType)
		if err != nil {
			return nil, "", err
		}
		if selected.base != "multipart/form-data" {
			return nil, "", fmt.Errorf("multipart encoder received concrete media type %q", selectedMediaType)
		}
		if boundary, present := selected.renderedParams["boundary"]; present {
			if err := writer.SetBoundary(boundary); err != nil {
				return nil, "", fmt.Errorf("invalid multipart boundary: %w", err)
			}
		}
	}
	is30 := isOpenAPI30(majorMinor(doc.OpenAPI))

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	schema := mediaSchema(media)
	for _, name := range names {
		value := fields[name]
		propSchema := resolvedMultipartPropertyFor(
			schema,
			name,
			map[*openapi3.Schema]bool{},
			hasDynamicObjectCarriage(bindingSpec),
		)
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if hasMediaFidelity(bindingSpec) && enc != nil && len(enc.Headers) > 0 {
			return nil, "", fmt.Errorf("multipart part %q declares encoding.headers, but this binding revision defines no caller source for dynamic part-header values", name)
		}
		if hasMediaFidelity(bindingSpec) && encodingUsesSerialization(enc) {
			units, err := serializeMultipartValue(name, value, enc)
			if err != nil {
				return nil, "", fmt.Errorf("multipart part %q: %w", name, err)
			}
			for _, unit := range units {
				if err := writeMultipartSerializedField(writer, unit.name, unit.value); err != nil {
					return nil, "", err
				}
			}
			continue
		}

		// A declared array expands into repeated parts when no explicit
		// RFC6570-style branch governs the property. Each item derives its own
		// content type and raw/text boundary from the resolved items schema.
		// each element encoded per the items schema (the multipart way to
		// carry arrays — including arrays of files).
		if schemaTypeIs(propSchema, "array", map[*openapi3.Schema]bool{}) {
			if arr, ok := asArray(value); ok {
				items := resolvedMultipartItems(propSchema, map[*openapi3.Schema]bool{})
				for _, elem := range arr {
					if err := writeMultipartPart(writer, name, elem, items, enc, is30, bindingSpec); err != nil {
						return nil, "", err
					}
				}
				continue
			}
		}
		if err := writeMultipartPart(writer, name, value, propSchema, enc, is30, bindingSpec); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	if !hasMediaFidelity(bindingSpec) {
		return &buf, writer.FormDataContentType(), nil
	}
	params := make(map[string]string, len(selected.renderedParams)+1)
	for key, value := range selected.renderedParams {
		params[key] = value
	}
	if _, present := params["boundary"]; !present {
		params["boundary"] = writer.Boundary()
	}
	return &buf, formatHTTPMediaType(selected.base, params), nil
}

type multipartSerializedUnit struct {
	name  string
	value string
}

func serializeMultipartValue(name string, value any, enc *openapi3.Encoding) ([]multipartSerializedUnit, error) {
	method := revision3EncodingSerializationMethod(enc)
	switch method.Style {
	case openapi3.SerializationForm:
		return multipartFormUnits(name, value, method.Explode, ",")
	case openapi3.SerializationSpaceDelimited:
		return multipartFormUnits(name, value, method.Explode, " ")
	case openapi3.SerializationPipeDelimited:
		return multipartFormUnits(name, value, method.Explode, "|")
	case openapi3.SerializationDeepObject:
		object, ok := asObject(value)
		if !ok {
			return nil, fmt.Errorf("style deepObject is defined for objects only, got %T", value)
		}
		pairs, pairErr := objectPairs(object)
		if pairErr != nil {
			return nil, pairErr
		}
		units := make([]multipartSerializedUnit, 0, len(pairs))
		for _, pair := range pairs {
			units = append(units, multipartSerializedUnit{name: name + "[" + pair[0] + "]", value: pair[1]})
		}
		return units, nil
	default:
		return nil, fmt.Errorf("style %q is not supported", method.Style)
	}
}

func objectBodyFields(plan *bodyPlan, routed *routedInput) (map[string]any, error) {
	if !plan.wholeObject {
		return routed.bodyFields, nil
	}
	if !routed.bodySet {
		return map[string]any{}, nil
	}
	fields, ok := toStringAnyMap(routed.bodyValue)
	if !ok {
		return nil, fmt.Errorf("request media %s requires the whole body value to be an object", plan.mediaType)
	}
	return fields, nil
}

func multipartFormUnits(name string, value any, explode bool, delimiter string) ([]multipartSerializedUnit, error) {
	if array, ok := asArray(value); ok {
		values, err := arrayStrings(array)
		if err != nil {
			return nil, err
		}
		if !explode {
			return []multipartSerializedUnit{{name: name, value: strings.Join(values, delimiter)}}, nil
		}
		units := make([]multipartSerializedUnit, len(values))
		for index, item := range values {
			units[index] = multipartSerializedUnit{name: name, value: item}
		}
		return units, nil
	}
	if object, ok := asObject(value); ok {
		pairs, err := objectPairs(object)
		if err != nil {
			return nil, err
		}
		if explode {
			units := make([]multipartSerializedUnit, len(pairs))
			for index, pair := range pairs {
				units[index] = multipartSerializedUnit{name: pair[0], value: pair[1]}
			}
			return units, nil
		}
		return []multipartSerializedUnit{{name: name, value: strings.Join(flattenPairs(pairs), delimiter)}}, nil
	}
	text, err := primitiveString(value)
	if err != nil {
		return nil, err
	}
	return []multipartSerializedUnit{{name: name, value: text}}, nil
}

func writeMultipartSerializedField(writer *multipart.Writer, name, value string) error {
	if err := writer.WriteField(name, value); err != nil {
		return fmt.Errorf("write field %q: %w", name, err)
	}
	return nil
}

func resolvedMultipartProperty(schema *openapi3.Schema, name string, seen map[*openapi3.Schema]bool) *openapi3.Schema {
	return resolvedMultipartPropertyFor(schema, name, seen, false)
}

func resolvedMultipartPropertyFor(schema *openapi3.Schema, name string, seen map[*openapi3.Schema]bool, dynamic bool) *openapi3.Schema {
	if schema == nil || seen[schema] {
		return nil
	}
	seen[schema] = true
	defer delete(seen, schema)

	var matches []*openapi3.Schema
	exact := false
	if ref := schema.Properties[name]; ref != nil && ref.Value != nil {
		exact = true
		matches = append(matches, ref.Value)
	}
	patternMatched := false
	if dynamic {
		patterns := make([]string, 0, len(schema.PatternProperties))
		for pattern := range schema.PatternProperties {
			patterns = append(patterns, pattern)
		}
		sort.Strings(patterns)
		for _, pattern := range patterns {
			re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
			if err != nil {
				continue // document validation owns invalid JSON Schema patterns
			}
			matched, err := re.MatchString(name)
			if err != nil || !matched {
				continue
			}
			patternMatched = true
			if ref := schema.PatternProperties[pattern]; ref != nil && ref.Value != nil {
				matches = append(matches, ref.Value)
			}
		}
		if !exact && !patternMatched {
			switch {
			case schema.AdditionalProperties.Schema != nil && schema.AdditionalProperties.Schema.Value != nil:
				if literal, boolean := booleanSchemaLiteral(schema.AdditionalProperties.Schema.Value); !boolean {
					matches = append(matches, schema.AdditionalProperties.Schema.Value)
				} else if literal {
					matches = append(matches, &openapi3.Schema{})
				}
			case schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has:
				matches = append(matches, &openapi3.Schema{})
			}
		}
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		if match := resolvedMultipartPropertyFor(member.Value, name, seen, dynamic); match != nil {
			matches = append(matches, match)
		}
	}
	return allOfSchema(matches)
}

func resolvedMultipartItems(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) *openapi3.Schema {
	if schema == nil || seen[schema] {
		return nil
	}
	seen[schema] = true
	defer delete(seen, schema)

	var matches []*openapi3.Schema
	if schema.Items != nil && schema.Items.Value != nil {
		matches = append(matches, schema.Items.Value)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		if match := resolvedMultipartItems(member.Value, seen); match != nil {
			matches = append(matches, match)
		}
	}
	return allOfSchema(matches)
}

func allOfSchema(matches []*openapi3.Schema) *openapi3.Schema {
	switch len(matches) {
	case 0:
		return nil
	case 1:
		return matches[0]
	default:
		combined := &openapi3.Schema{}
		for _, match := range matches {
			combined.AllOf = append(combined.AllOf, &openapi3.SchemaRef{Value: match})
		}
		return combined
	}
}

func schemaTypeIs(schema *openapi3.Schema, want string, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if schema.Type.Is(want) {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && schemaTypeIs(member.Value, want, seen) {
			return true
		}
	}
	return false
}

func writeMultipartPart(writer *multipart.Writer, name string, value any, schema *openapi3.Schema, enc *openapi3.Encoding, is30 bool, bindingSpec string) error {
	revision3 := hasMediaFidelity(bindingSpec)
	if revision3 {
		return writeRevision3MultipartPart(writer, name, value, schema, enc, is30)
	}
	legacyBinary := !revision3 && binarySignaled(schema, is30)
	revision3Raw := revision3 && is30 && schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && binarySignaled(schema, true)
	if legacyBinary || revision3Raw {
		var data []byte
		var err error
		if revision3Raw {
			if text, ok := value.(string); ok {
				data, err = canonicalBase64BoundaryBytes(name, text)
			} else {
				err = fmt.Errorf("binary part %q: the value must be a string carrying the encoded bytes, got %T", name, value)
			}
		} else {
			data, err = binaryPartBytes(name, value, declaredContentEncoding(schema, is30))
		}
		if err != nil {
			return err
		}
		ct := ""
		if enc != nil && enc.ContentType != "" {
			ct = enc.ContentType
		} else if cmt := schemaExtensionString(schema, "contentMediaType", map[*openapi3.Schema]bool{}); cmt != "" {
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

	if revision3 && !is30 {
		if _, conflict := resolvedSchemaKeywordString(schema, "contentEncoding"); conflict {
			return fmt.Errorf("multipart part %q: resolved schema declares conflicting contentEncoding values", name)
		}
	}

	// The encoding object's contentType, where declared, decides the part's
	// serialization; else the OAS per-type part defaults apply.
	declaredPartContentType := ""
	if enc != nil && enc.ContentType != "" {
		declaredPartContentType = enc.ContentType
	} else if revision3 && !is30 {
		declaredPartContentType, _ = resolvedSchemaKeywordString(schema, "contentMediaType")
	}
	if declaredPartContentType != "" {
		ct := normalizeMediaType(declaredPartContentType)
		var body []byte
		if revision3 && !is30 && schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) {
			// OAS 3.1's contentEncoding/contentMediaType annotate the
			// application string. They neither create an OpenBindings raw-byte
			// boundary nor imply JSON re-serialization of its characters.
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("part %q: the artifact-declared encoded content must be a string, got %T", name, value)
			}
			body = []byte(s)
		} else if isJSONMediaType(ct) {
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
		h.Set("Content-Type", declaredPartContentType)
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

func writeRevision3MultipartPart(writer *multipart.Writer, name string, value any, schema *openapi3.Schema, enc *openapi3.Encoding, is30 bool) error {
	if enc != nil && len(enc.Headers) > 0 {
		return fmt.Errorf("multipart part %q declares encoding.headers, but this binding revision defines no caller source for dynamic part-header values", name)
	}
	parsedContentType, err := revision3PartContentType(schema, enc, is30)
	if err != nil {
		return fmt.Errorf("multipart part %q: %w", name, err)
	}

	mode, err := revision3PropertyCarriage(schema, parsedContentType, is30, true)
	if err != nil {
		return fmt.Errorf("multipart part %q: %w", name, err)
	}
	rawBinary := mode == revision3PropertyRaw30
	var body []byte
	switch mode {
	case revision3PropertyRaw30:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("binary part %q: the value must be a string carrying canonical Base64 bytes, got %T", name, value)
		}
		body, err = canonicalBase64BoundaryBytes(name, text)
	case revision3PropertyJSON:
		body, err = json.Marshal(value)
	case revision3PropertyEncoded31:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("part %q: the artifact-declared string value has type %T", name, value)
		}
		body, err = encodeTextString(text, parsedContentType)
	case revision3PropertyText:
		var text string
		text, err = primitiveString(value)
		if err == nil {
			body, err = encodeTextString(text, parsedContentType)
		}
	}
	if err != nil {
		return fmt.Errorf("multipart part %q: %w", name, err)
	}

	h := make(textproto.MIMEHeader)
	disposition := fmt.Sprintf(`form-data; name=%q`, name)
	if rawBinary {
		disposition += fmt.Sprintf(`; filename=%q`, name)
	}
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Type", parsedContentType.canonical)
	if !is30 {
		contentEncoding, conflict := resolvedSchemaKeywordString(schema, "contentEncoding")
		if conflict {
			return fmt.Errorf("multipart part %q: resolved schema declares conflicting contentEncoding values", name)
		}
		if contentEncoding != "" {
			h.Set("Content-Transfer-Encoding", contentEncoding)
		}
	}
	part, err := writer.CreatePart(h)
	if err != nil {
		return fmt.Errorf("create part %q: %w", name, err)
	}
	if _, err := part.Write(body); err != nil {
		return fmt.Errorf("write part %q: %w", name, err)
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
	return binarySignaledWithSeen(schema, is30, map[*openapi3.Schema]bool{})
}

func binarySignaledWithSeen(schema *openapi3.Schema, is30 bool, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if is30 {
		if schema.Format == "binary" {
			return true
		}
	} else if schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) &&
		(schemaExtensionString(schema, "contentMediaType", map[*openapi3.Schema]bool{}) != "" ||
			schemaExtensionString(schema, "contentEncoding", map[*openapi3.Schema]bool{}) != "") {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && binarySignaledWithSeen(member.Value, is30, seen) {
			return true
		}
	}
	return false
}

// declaredContentEncoding returns the 3.1 schema's declared contentEncoding
// (3.0 has no equivalent keyword; its binary signal carries no encoding).
func declaredContentEncoding(schema *openapi3.Schema, is30 bool) string {
	if is30 {
		return ""
	}
	return schemaExtensionString(schema, "contentEncoding", map[*openapi3.Schema]bool{})
}

// schemaExtensionString reads a 3.1 keyword kin-openapi carries in the
// schema's extensions map (contentMediaType, contentEncoding), including a
// declaration contributed through allOf.
func schemaExtensionString(schema *openapi3.Schema, key string, seen map[*openapi3.Schema]bool) string {
	if schema == nil || seen[schema] {
		return ""
	}
	seen[schema] = true
	defer delete(seen, schema)
	switch key {
	case "contentMediaType":
		if schema.ContentMediaType != "" {
			return schema.ContentMediaType
		}
	case "contentEncoding":
		if schema.ContentEncoding != "" {
			return schema.ContentEncoding
		}
	}
	if schema.Extensions != nil {
		if s, _ := schema.Extensions[key].(string); s != "" {
			return s
		}
	}
	for _, member := range schema.AllOf {
		if member == nil {
			continue
		}
		if s := schemaExtensionString(member.Value, key, seen); s != "" {
			return s
		}
	}
	return ""
}

// resolvedSchemaKeywordString collects one string annotation through allOf.
// Revision 3 uses it for carriage-affecting contentEncoding: two distinct
// values do not have a meaningful single wire interpretation and therefore
// refuse instead of depending on traversal order.
func resolvedSchemaKeywordString(schema *openapi3.Schema, key string) (string, bool) {
	values := map[string]bool{}
	seen := map[*openapi3.Schema]bool{}
	var visit func(*openapi3.Schema)
	visit = func(current *openapi3.Schema) {
		if current == nil || seen[current] {
			return
		}
		seen[current] = true
		var typed string
		switch key {
		case "contentMediaType":
			typed = current.ContentMediaType
		case "contentEncoding":
			typed = current.ContentEncoding
		}
		if typed != "" {
			values[typed] = true
		}
		if extension, _ := current.Extensions[key].(string); extension != "" {
			values[extension] = true
		}
		for _, member := range current.AllOf {
			if member != nil {
				visit(member.Value)
			}
		}
	}
	visit(schema)
	if len(values) > 1 {
		return "", true
	}
	for value := range values {
		return value, false
	}
	return "", false
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

// canonicalBase64BoundaryBytes decodes revision 3's JSON-domain raw-octet
// boundary. Unlike the older multipart convenience decoder, this lane accepts
// only canonical RFC 4648 §4 spelling: standard alphabet, required padding,
// no whitespace, and zero unused pad bits. Re-encoding is the simplest exact
// proof of all of those properties.
func canonicalBase64BoundaryBytes(name, value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value {
		if err == nil {
			err = fmt.Errorf("non-canonical RFC 4648 base64 spelling")
		}
		return nil, fmt.Errorf("binary value %q: invalid base64: %w", name, err)
	}
	return decoded, nil
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
	return buildURLEncodedBodyForRevision(nil, media, fields, BindingSpecV2)
}

func buildURLEncodedBodyForRevision(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any, bindingSpec string) (string, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var units []string
	schema := mediaSchema(media)
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	openapiVersion := ""
	if doc != nil {
		openapiVersion = doc.OpenAPI
	}
	for _, name := range names {
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if hasMediaFidelity(bindingSpec) && !legacyOpenAPIFormEncoding(openapiVersion) && !encodingUsesSerialization(enc) {
			propertySchema := resolvedMultipartPropertyFor(
				schema,
				name,
				map[*openapi3.Schema]bool{},
				hasDynamicObjectCarriage(bindingSpec),
			)
			contentType, err := revision3PartContentType(propertySchema, enc, is30)
			if err != nil {
				return "", fmt.Errorf("form field %q: %w", name, err)
			}
			value, err := revision3PropertyBytes(name, fields[name], propertySchema, contentType, is30)
			if err != nil {
				return "", fmt.Errorf("form field %q: %w", name, err)
			}
			units = append(units, url.QueryEscape(name)+"="+url.QueryEscape(string(value)))
			continue
		}
		style, explode, allowReserved := openapi3.SerializationForm, true, false
		if enc != nil {
			sm := enc.SerializationMethod()
			style, explode = sm.Style, sm.Explode
			allowReserved = enc.AllowReserved
		}
		var u []string
		var err error
		if hasMediaFidelity(bindingSpec) {
			u, err = serializeQueryValueForRevision(name, fields[name], style, explode, allowReserved, bindingSpec, true)
		} else {
			u, err = serializeQueryValue(name, fields[name], style, explode, allowReserved)
		}
		if err != nil {
			return "", fmt.Errorf("form field %q: %w", name, err)
		}
		units = append(units, u...)
	}
	return strings.Join(units, "&"), nil
}

func revision3PropertyBytes(name string, value any, schema *openapi3.Schema, contentType parsedMediaType, is30 bool) ([]byte, error) {
	mode, err := revision3PropertyCarriage(schema, contentType, is30, false)
	if err != nil {
		return nil, err
	}
	switch mode {
	case revision3PropertyJSON:
		return json.Marshal(value)
	case revision3PropertyRaw30:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("binary value must be a canonical Base64 string, got %T", value)
		}
		return canonicalBase64BoundaryBytes(name, text)
	case revision3PropertyEncoded31:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("artifact-declared string value has type %T", value)
		}
		return encodeTextString(text, contentType)
	case revision3PropertyText:
		text, err := primitiveString(value)
		if err != nil {
			return nil, err
		}
		return encodeTextString(text, contentType)
	}
	return nil, fmt.Errorf("unknown revision-3 property carriage")
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
	return governingResponseMediaFor(response, actual, BindingSpecV2)
}

func governingResponseMediaFor(response *openapi3.Response, actual, bindingSpec string) (parsedMediaType, error) {
	match, err := governingResponseMediaMatchFor(response, actual, bindingSpec)
	if err != nil {
		return parsedMediaType{}, err
	}
	return match.declared, nil
}

type governingResponseMediaMatch struct {
	key      string
	declared parsedMediaType
	media    *openapi3.MediaType
}

func governingResponseMediaMatchFor(response *openapi3.Response, actual, bindingSpec string) (governingResponseMediaMatch, error) {
	if response == nil || len(response.Content) == 0 {
		return governingResponseMediaMatch{}, fmt.Errorf("the governing response declares no media")
	}
	var parsedActual parsedMediaType
	var err error
	if hasMediaFidelity(bindingSpec) {
		parsedActual, err = parseRevision3MediaType(actual)
	} else {
		parsedActual, err = parseMediaType(actual)
	}
	if err != nil {
		return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type: %w", err)
	}
	identities := map[string]string{}
	var declarations []governingResponseMediaMatch
	for key, media := range response.Content {
		var declared parsedMediaType
		var err error
		if hasMediaFidelity(bindingSpec) {
			declared, err = parseMediaDeclaration(key)
		} else {
			declared, err = parseMediaType(key)
		}
		if err != nil {
			if hasMediaFidelity(bindingSpec) {
				return governingResponseMediaMatch{}, fmt.Errorf("invalid response media declaration %q: %w", key, err)
			}
			continue
		}
		identity := declared.identity
		if hasMediaFidelity(bindingSpec) {
			identity = declared.semanticIdentity
		}
		if previous, exists := identities[identity]; exists {
			return governingResponseMediaMatch{}, fmt.Errorf("response content declarations %q and %q denote the same parsed media type or range declaration", previous, key)
		}
		identities[identity] = key
		if declared.rangeSpecificity < 2 && !hasResponseFidelity(bindingSpec) {
			// Range identities participate in collision inventory, but response
			// ranges do not compete with a concrete match and cannot authorize
			// a decode lane in revision 3.
			continue
		}
		declarations = append(declarations, governingResponseMediaMatch{key: key, declared: declared, media: media})
	}

	bestRangeSpecificity, bestParameters := -1, -1
	var matches []governingResponseMediaMatch
	for _, declaration := range declarations {
		declared := declaration.declared
		if hasMediaFidelity(bindingSpec) {
			if !requestMediaDeclarationMatches(declared, parsedActual) {
				continue
			}
		} else if declared.base != parsedActual.base {
			continue
		} else {
			matchesParams := true
			for name, value := range declared.params {
				if actualValue, present := parsedActual.params[name]; !present || actualValue != value {
					matchesParams = false
					break
				}
			}
			if !matchesParams {
				continue
			}
		}
		rangeSpecificity, parameters := declared.rangeSpecificity, len(declared.params)
		if rangeSpecificity > bestRangeSpecificity || (rangeSpecificity == bestRangeSpecificity && parameters > bestParameters) {
			bestRangeSpecificity, bestParameters = rangeSpecificity, parameters
			matches = []governingResponseMediaMatch{declaration}
		} else if rangeSpecificity == bestRangeSpecificity && parameters == bestParameters {
			matches = append(matches, declaration)
		}
	}
	if len(matches) == 0 {
		return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type %q matches no media in the governing response", actual)
	}
	if len(matches) != 1 {
		return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type %q ambiguously matches %d equally specific declarations", actual, len(matches))
	}
	return matches[0], nil
}

// successMediaTypes returns the declared concrete media types that may govern
// a successful response: literal 2xx, 2XX, and default declarations. Members
// retain declaration parameters; ordering is an implementation convention.
func successMediaTypes(op *openapi3.Operation) []string {
	return successMediaTypesFor(op, BindingSpecV2)
}

func successMediaTypesFor(op *openapi3.Operation, bindingSpec string) []string {
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
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseMediaDeclaration(mt)
			} else {
				parsed, err = parseMediaType(mt)
			}
			if err != nil || (parsed.rangeSpecificity < 2 && !hasResponseFidelity(bindingSpec)) {
				continue
			}
			identity := parsed.identity
			if hasMediaFidelity(bindingSpec) {
				identity = parsed.semanticIdentity
			}
			if identities[identity] {
				continue
			}
			identities[identity] = true
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

func acceptHeaderFor(op *openapi3.Operation, bindingSpec string) string {
	return strings.Join(successMediaTypesFor(op, bindingSpec), ", ")
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

func isStreamingCapableFor(op *openapi3.Operation, bindingSpec string) bool {
	if !hasMediaFidelity(bindingSpec) {
		return isStreamingCapable(op)
	}
	for _, mt := range successMediaTypesFor(op, bindingSpec) {
		parsed, err := parseMediaDeclaration(mt)
		if err == nil && mediaRangeBaseMatches(parsed.base, "text/event-stream") {
			return true
		}
	}
	return false
}
