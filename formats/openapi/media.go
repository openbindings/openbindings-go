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

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/dlclark/regexp2"
	"github.com/getkin/kin-openapi/openapi3"
)

// This file implements openbindings.openapi-3.0@1 §9.2 and
// openbindings.openapi-3.1@1 §9.2: request
// media selection with its deterministic tiebreaks and pre-dispatch
// refusals, multipart part encoding (including the Base64 boundary encoding
// for binary-signaled parts), urlencoded field serialization, and the §8
// declared-media facts (success responses and streaming capability) the
// interaction shape is bounded by. Those facts never produce an Accept
// header; §9.1 requires the binding to omit it.

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
	declared                  bool
	required                  bool
	mediaKey                  string // the declared content key, verbatim
	mediaType                 string // normalized type/subtype (the request Content-Type)
	media                     *openapi3.MediaType
	family                    string
	synthetic                 bool
	wholeObject               bool            // complete body rides under one protocol-neutral application field
	props                     map[string]bool // declared top-level body property names (object mode)
	mediaRange                bool            // mediaKey is a revision-3 media-range declaration
	rawBoundary               bool            // caller string is Base64 boundary carriage for raw bytes
	bindingSpec               string
	oas30                     bool
	propertyMedia             []string          // properties requiring a concrete consumer media choice
	propertyMediaDeclarations map[string]string // authored Encoding contentType lists, before concrete selection
	rawProperties             map[string]bool   // content-path properties crossing the canonical Base64 boundary
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
// refusal (openbindings.openapi-3.0@1 §9.2;
// openbindings.openapi-3.1@1 §9.2): the selected request media type has no OAS-defined
// wire form for the declared body schema. A distinct type so synthesis
// (synthesize.go) can surface the same fact as the
// openapi.media_schema_mismatch warning without re-deriving the selection.
type degenerateMediaError struct{ msg string }

func (e *degenerateMediaError) Error() string { return e.msg }

// normalizedMediaCollisions inventories ONE OAS content map and returns the
// keys that denote a parsed media identity another key of the SAME map also
// denotes, each mapped to a stable rendering of the identity they collide on.
//
// §9.2: two keys in one content map denoting the same parsed media type are a
// normalized collision, and the defect CONFINES to that colliding parsed
// identity -- the smallest unit that owns it. No first-key rule is invented:
// every caller skips exactly the keys named here, so no request selection
// lands on the colliding identity, no response match is governed by it, and
// it is never advertised as an available response representation -- while the
// map's non-colliding entries remain usable alternatives. The inventory never
// spans two content maps, because a content map is the unit the rule names.
func normalizedMediaCollisions(content openapi3.Content, bindingSpec string) map[string]string {
	if len(content) < 2 {
		return nil
	}
	members := map[string][]string{}
	rendering := map[string]string{}
	for key := range content {
		var parsed parsedMediaType
		var err error
		if hasMediaFidelity(bindingSpec) {
			parsed, err = parseMediaDeclaration(key)
		} else {
			parsed, err = parseMediaType(key)
		}
		if err != nil {
			// An unparseable key denotes no identity, so it collides with
			// nothing. Whether it is itself a defect is its owner's question.
			continue
		}
		identity := parsed.identity
		if hasMediaFidelity(bindingSpec) {
			identity = parsed.semanticIdentity
		}
		members[identity] = append(members[identity], key)
		if existing, seen := rendering[identity]; !seen || parsed.canonical < existing {
			rendering[identity] = parsed.canonical
		}
	}
	var colliding map[string]string
	for identity, keys := range members {
		if len(keys) < 2 {
			continue
		}
		if colliding == nil {
			colliding = map[string]string{}
		}
		for _, key := range keys {
			colliding[key] = rendering[identity]
		}
	}
	return colliding
}

// collidesWithNormalizedIdentity reports whether a concrete media type denotes
// one of the parsed identities a content map's keys collide on.
func collidesWithNormalizedIdentity(colliding map[string]string, parsed parsedMediaType, bindingSpec string) bool {
	if len(colliding) == 0 {
		return false
	}
	identity := parsed.identity
	if hasMediaFidelity(bindingSpec) {
		identity = parsed.semanticIdentity
	}
	for key := range colliding {
		var declared parsedMediaType
		var err error
		if hasMediaFidelity(bindingSpec) {
			declared, err = parseMediaDeclaration(key)
		} else {
			declared, err = parseMediaType(key)
		}
		if err != nil {
			continue
		}
		declaredIdentity := declared.identity
		if hasMediaFidelity(bindingSpec) {
			declaredIdentity = declared.semanticIdentity
		}
		if declaredIdentity == identity {
			return true
		}
	}
	return false
}

// planRequestBody returns the reference SDK's first declaration-sorted
// candidate for the exact binding family token in scope. Runtime invocation
// uses planRequestBodiesFor and applies candidate-specific admissibility after
// reading the caller value.
func planRequestBody(op *openapi3.Operation, bindingSpec string) (*bodyPlan, error) {
	plans, err := planRequestBodiesFor(nil, op, bindingSpec)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return &bodyPlan{}, nil
	}
	return plans[0], nil
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
	collided := map[string]bool{}
	nonColliding := 0
	colliding := normalizedMediaCollisions(rb.Content, bindingSpec)
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
		if identity, collides := colliding[key]; collides {
			// §9.2 normalized collision, confined: no request selection may
			// land on this parsed identity, so the key contributes no
			// candidate and its alternative is an accounted exclusion. The
			// map's non-colliding entries are unaffected (§9.2 in both family documents).
			collided[identity] = true
			continue
		}
		nonColliding++
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
		if len(collided) > 0 && nonColliding == 0 {
			identities := make([]string, 0, len(collided))
			for identity := range collided {
				identities = append(identities, identity)
			}
			sort.Strings(identities)
			return nil, fmt.Errorf("every request content declaration denotes a normalized-colliding parsed media identity, so no selection may land on one (colliding: %s)", strings.Join(identities, ", "))
		}
		sort.Strings(declared)
		return nil, fmt.Errorf("request body declares no media type whose declaration selects a request carriage lane the registered OpenAPI binding family defines (declared: %s)", strings.Join(declared, ", "))
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].parsed.identity < candidates[j].parsed.identity })
	plans := make([]*bodyPlan, 0, len(candidates))
	oas30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for _, candidate := range candidates {
		media := rb.Content[candidate.key]
		wholeJSON := hasWholeJSONCarriage(bindingSpec) && !candidate.mediaRange && candidate.family == familyJSON &&
			requiresWholeJSONCarriage(mediaSchema(media), map[*openapi3.Schema]bool{}, oas30)
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
		plan.oas30 = oas30
		plan.propertyMedia = requiredPropertyMediaNames(plan)
		plan.propertyMediaDeclarations = propertyMediaDeclarations(plan)
		plan.rawProperties = rawPropertyNames(plan)
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
//
// The keywords are read under the GOVERNING EDITION'S dialect (§9.1). The
// 3.0 line's Schema Object carries oneOf, anyOf and not only, so if/then/
// else, dependentSchemas and unevaluatedProperties are strictly-unsupported
// keywords there and decide as if absent.
func requiresWholeJSONCarriage(schema *openapi3.Schema, seen map[*openapi3.Schema]bool, oas30 bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	if _, boolean := booleanSchemaLiteral(schema); boolean {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || schema.Not != nil {
		return true
	}
	if !oas30 && (schema.If != nil || schema.Then != nil || schema.Else != nil ||
		len(schema.DependentSchemas) > 0 || declaresUnevaluatedProperties(schema.UnevaluatedProperties)) {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && requiresWholeJSONCarriage(member.Value, seen, oas30) {
			return true
		}
	}
	return false
}

// declaresUnevaluatedProperties is §9.1's presence trigger: a schema
// "carries an explicit unevaluatedProperties (any value)". Presence is the
// whole test — the declared value, `false` included, never narrows it. This
// is deliberately NOT explicitDynamicAdditionalProperties, which answers the
// different question of whether additionalProperties opens a dynamic surface.
func declaresUnevaluatedProperties(declared openapi3.BoolSchema) bool {
	return declared.Has != nil || declared.Schema != nil
}

func applyDynamicObjectShape(plan *bodyPlan) {
	if plan == nil || plan.synthetic ||
		!hasExplicitDynamicProperties(mediaSchema(plan.media), map[*openapi3.Schema]bool{}, plan.oas30) {
		return
	}
	plan.wholeObject = true
	plan.props = nil
}

// hasExplicitDynamicProperties reads its trigger keywords under the
// governing edition's dialect (§9.1): patternProperties is not in the 3.0
// line's Schema Object at all, so there it decides as if absent and the
// 3.0-line trigger is an explicit additionalProperties alone.
func hasExplicitDynamicProperties(schema *openapi3.Schema, seen map[*openapi3.Schema]bool, oas30 bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if (!oas30 && len(schema.PatternProperties) > 0) || explicitDynamicAdditionalProperties(schema.AdditionalProperties) {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && hasExplicitDynamicProperties(member.Value, seen, oas30) {
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
			return nil, fmt.Errorf("request media candidate %s has an object body schema and is inadmissible", plan.mediaType)
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
			return "", false, fmt.Errorf("schema-omitted form media has no application-value caller route")
		}
		if hasMediaFidelity(bindingSpec) {
			if err := validateRevision3URLEncodedMedia(doc, media); err != nil {
				return "", false, err
			}
		}
		return familyURLEncoded, false, nil
	case parsed.base == "text/plain" && !hasMediaFidelity(bindingSpec):
		return familyText, false, nil
	}
	if !hasMediaFidelity(bindingSpec) {
		return "", false, nil
	}
	// The artifact-authorized byte lanes are evaluated first: they are the
	// cases where the ARTIFACT determines the octets. §9.2 carries no
	// media-type carve-out here — a schema-omitted text declaration is a
	// schema-omitted declaration like any other, which is what keeps it from
	// being orphaned between two lanes. They are also the only edition-
	// dependent lanes, so they need the document; the string lane below does
	// not, and is decided identically under every accepted edition.
	if doc != nil {
		eligible, rawBoundary, err := octetRequestCarriage(doc, media, bindingSpec)
		if err != nil {
			return "", false, err
		}
		if eligible {
			return familyOctets, rawBoundary, nil
		}
	}
	// §9.2 string carriage. Its scope is DERIVED from two authorities, not
	// chosen: the OAS decides that the value is a string (and under 3.1
	// `format` has no content-encoding force, so `format: binary` there is
	// still a string declaration), while the media-type registration decides
	// whether a string has an octet image at all. A string becomes wire bytes
	// only where a character encoding applies, so the lane admits exactly the
	// character-data media below. The CALLER determines those octets, so the
	// lane authors no correspondence. Scoped by the declaration: the supplied
	// value's type never selects a lane.
	oas30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	if isCharacterDataMedia(parsed.base) && resolveDeclaration(mediaSchema(media), oas30).admitsStringAsSoleNonNullType() {
		if err := supportedTextCharset(parsed); err != nil {
			return "", false, err
		}
		return familyText, false, nil
	}
	return "", false, nil
}

// isCharacterDataMedia is §9.2's closed, structural character-data test: the
// media types whose registration establishes their content as characters
// under a charset, and therefore the only ones for which a caller-supplied
// string has a defined octet image. Members, with the authority for each:
//
//	text/*                     RFC 6838 §4.2.1, RFC 2046 §4.1 — the text tree
//	                           is "material that is principally textual in
//	                           form", every registration must state how its
//	                           charset is determined, UTF-8 recommended
//	application/xml, text/xml  RFC 7303 §3, §9.1, §9.2
//	*+xml                      RFC 7303 §9.6.1
//	*+json                     RFC 8259 §8.1 (the JSON lanes claim these first)
//
// It is deliberately NOT RFC 6838 §4.8's "Encoding considerations" field,
// which does not decide the question — application/json registers `binary`
// (RFC 8259 §11) while requiring UTF-8 text, and text/csv (RFC 4180 §3)
// records no §4.8 value at all — and deliberately not a registry lookup,
// which would make the accepted domain depend on a mutable source against
// §2's pinning discipline. The argument must already be normalized.
func isCharacterDataMedia(base string) bool {
	primary, subtype, ok := strings.Cut(base, "/")
	if !ok || primary == "" || subtype == "" {
		return false
	}
	if primary == "text" {
		return true
	}
	if index := strings.LastIndexByte(subtype, '+'); index >= 0 {
		switch subtype[index:] {
		case "+xml", "+json":
			return true
		}
	}
	return base == "application/xml"
}

// schemaAssertsNothing reports a Media Type Object schema that makes no
// assertion at all: the JSON Schema boolean `true`, or a Schema Object with
// no members. §9.2 treats it as the same declaration as an omitted `schema`
// — the artifact made no claim that the body is a string — so it takes the
// artifact-authorized byte lanes rather than falling between two lanes.
func schemaAssertsNothing(schema *openapi3.Schema) bool {
	if schema == nil {
		return false
	}
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		return literal
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var raw map[string]any
	if json.Unmarshal(encoded, &raw) != nil {
		return false
	}
	return len(raw) == 0
}

func validateRevision3URLEncodedMedia(doc *openapi3.T, media *openapi3.MediaType) error {
	schema := mediaSchema(media)
	if schema == nil {
		return fmt.Errorf("schema-omitted urlencoded media has no application-value caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for name := range props {
		propertySchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		if literal, boolean := booleanSchemaLiteral(propertySchema); boolean {
			if is30 && literal {
				return fmt.Errorf("OAS 3.0 form property %q uses a boolean schema outside its Schema Object dialect", name)
			}
			if !literal {
				continue
			}
		}
		propertySchema, _ = effectiveRevision3PartSchema(propertySchema, is30)
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if encodingUsesSerialization(enc) {
			if err := validateMultipartSerializationMethod(name, propertySchema, enc, is30); err != nil {
				return err
			}
			continue
		}
		if encodingRequiresPropertyMedia(enc) {
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
		return fmt.Errorf("schema-omitted multipart media has no application-value caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for name := range props {
		if !multipartPropertyNameSafe(name) {
			return fmt.Errorf("multipart property name %q contains CR or LF", name)
		}
		partSchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		if literal, boolean := booleanSchemaLiteral(partSchema); boolean {
			if is30 && literal {
				return fmt.Errorf("OAS 3.0 multipart property %q uses a boolean schema outside its Schema Object dialect", name)
			}
			if !literal {
				continue // an unsatisfiable property has no admissible runtime value
			}
		}
		partSchema, _ = effectiveRevision3PartSchema(partSchema, is30)
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if !is30 && encodingUsesSerialization(enc) {
			if err := validateMultipartSerializationMethod(name, partSchema, enc, is30); err != nil {
				return err
			}
			continue
		}
		if encodingRequiresPropertyMedia(enc) {
			continue
		}
		contentSchema := partSchema
		if schemaTypeIs(partSchema, "array", map[*openapi3.Schema]bool{}) {
			contentSchema = resolvedMultipartItems(partSchema, map[*openapi3.Schema]bool{})
			if schemaTypeIs(contentSchema, "array", map[*openapi3.Schema]bool{}) {
				return fmt.Errorf("multipart part %q has nested array items with no defined repeated-part mapping", name)
			}
		}
		if is30 && byteFormatSignaled(contentSchema) {
			if _, err := openAPI30Base64TransferHeader(enc); err != nil {
				return fmt.Errorf("multipart part %q: %w", name, err)
			}
		}
		if is30 && resolveDeclaration(contentSchema, true).typeless() && (enc == nil || enc.ContentType == "") {
			continue // invocation supplies propertyMedia; synthesis keeps the alternative
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

func multipartPropertyNameSafe(name string) bool {
	return !strings.ContainsAny(name, "\r\n")
}

func encodingRequiresPropertyMedia(encoding *openapi3.Encoding) bool {
	if encoding == nil || encoding.ContentType == "" {
		return false
	}
	members, err := splitHTTPList(encoding.ContentType)
	return err == nil && (len(members) != 1 || isMediaRange(members[0]))
}

func requiredPropertyMediaNames(plan *bodyPlan) []string {
	if plan == nil || plan.media == nil || (plan.family != familyMultipart && plan.family != familyURLEncoded) {
		return nil
	}
	root := resolveDeclaration(mediaSchema(plan.media), plan.oas30)
	var names []string
	for _, name := range root.propertyNames() {
		property := root.property(name)
		if property.declaresOnly("array") || property.declaresOnly("array", "null") {
			property = property.items()
		}
		encoding := plan.media.Encoding[name]
		if encodingUsesSerializationForPlan(plan, encoding) {
			continue
		}
		contentType := ""
		if encoding != nil {
			contentType = encoding.ContentType
		}
		requiresChoice := plan.oas30 && plan.family == familyMultipart && property.typeless()
		if contentType != "" {
			members, err := splitHTTPList(contentType)
			if err == nil && (len(members) != 1 || isMediaRange(members[0])) {
				requiresChoice = true
			}
		}
		if requiresChoice {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func rawPropertyNames(plan *bodyPlan) map[string]bool {
	if plan == nil || plan.media == nil || (plan.family != familyMultipart && plan.family != familyURLEncoded) {
		return nil
	}
	root := resolveDeclaration(mediaSchema(plan.media), plan.oas30)
	result := map[string]bool{}
	for _, name := range root.propertyNames() {
		encoding := plan.media.Encoding[name]
		if encodingUsesSerializationForPlan(plan, encoding) {
			continue
		}
		property := root.property(name)
		if property.declaresOnly("array") || property.declaresOnly("array", "null") {
			property = property.items()
		}
		if plan.family == familyMultipart && property.typeless() {
			result[name] = true
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func propertyMediaDeclarations(plan *bodyPlan) map[string]string {
	if plan == nil || plan.media == nil || len(plan.propertyMedia) == 0 {
		return nil
	}
	result := map[string]string{}
	for _, name := range plan.propertyMedia {
		if encoding := plan.media.Encoding[name]; encoding != nil && encoding.ContentType != "" {
			result[name] = encoding.ContentType
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func encodingUsesSerializationForPlan(plan *bodyPlan, encoding *openapi3.Encoding) bool {
	// OAS 3.0 applies Encoding serialization controls to urlencoded bodies
	// only. Its multipart lane ignores all three controls.
	if plan != nil && plan.oas30 && plan.family == familyMultipart {
		return false
	}
	return encodingUsesSerialization(encoding)
}

// encodingUsesSerialization reports whether an Encoding Object writes an
// EXPLICIT RFC6570-style serialization control. That, and nothing else,
// selects the RFC6570-style lane for a form property; with all three fields
// absent the property takes the CONTENT lane, whose contentType default
// depends on the property's own declared type.
//
// The predicate is EDITION-INDEPENDENT across all eight accepted editions, and
// that uniformity is an incorporated consequence rather than a simplification.
//
//	3.0.4        Section 4.7.15.1.2: "whenever any of style, explode, or
//	             allowReserved are present with an explicit value: The value of
//	             contentType, whether it is explicitly defined or has the
//	             default value, is to be ignored ... However, if all three of
//	             style, explode, and allowReserved fields are absent, it is
//	             RECOMMENDED that: All three keywords are to be entirely
//	             ignored ... Encoding is to be based on contentType alone".
//	3.1.0        Section 4.8.15.1 carries, on EACH of style, explode and
//	             allowReserved: "If a value is explicitly defined, then the
//	             value of contentType (implicit or explicit) SHALL be ignored."
//	3.1.1, 3.1.2 Section 4.8.15.1.2 ("Fixed Fields for RFC6570-style
//	             Serialization") carries that same sentence on each field.
//	3.0.0-3.0.3  State no lane-selection sentence of their own. Their OWN
//	             section 4.1 supplies the rule instead: "Tooling which supports
//	             OAS 3.0 SHOULD be compatible with all OAS 3.0.* versions. The
//	             patch version SHOULD NOT be considered by tooling, making no
//	             distinction between 3.0.0 and 3.0.1 for example." 3.0.4 is a
//	             3.0.* version, so the 3.0 line takes ONE behavior, and only
//	             the content lane is consistent with 3.0.4 read under its own
//	             text -- which section 2 of the binding specification equally
//	             requires.
//
// So this function does NOT read the document's openapi field, and no engine
// here keys any urlencoded behavior on the patch component of a version. Until
// 2026-08-17 a legacyOpenAPIFormEncoding("3.0.0"|"3.0.1"|"3.0.2"|"3.0.3")
// predicate did exactly that. It was the ONLY patch-version-keyed behavioural
// switch in the three engines; it discarded an explicitly written
// encoding.contentType, collided sibling field names, and refused at dispatch
// for values the artifact had fully declared. It is deleted, and
// the registered OpenAPI binding family section 2 now states the patch-uniformity reading
// once. Package: design/openapi-30-urlencoded-default-lane-ruling.md.
//
// Reproduce the per-edition presence pattern (edition order 3.0.0, 3.0.1,
// 3.0.2, 3.0.3, 3.0.4, 3.1.0, 3.1.1, 3.1.2):
//
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "If a value is explicitly defined, then the value of contentType \
//	   \(implicit or explicit\) SHALL be ignored"             -> 0/0/0/0/0/3/3/3
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "Encoding is to be based on contentType alone"          -> 0/0/0/0/1/0/0/0
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "The patch version SHOULD NOT be considered by tooling" -> 1/1/1/1/1/1/1/1
//
// (each --count is one line). The digests of the renderings those counts run
// over are recorded in corpus-lab/OPENAPI-RUNTIME-INDEX.md.
//
// NOT the discriminator, and the reason an earlier comment here was wrong:
// "the default serialization strategy of such properties is described in the
// Encoding Object's style property as form", which is present in 3.0.0-3.0.3
// AND 3.1.0 and absent from 3.0.4, 3.1.1 and 3.1.2, so it partitions nothing.
//
// Pinned by the 64-cell twin table in urlencoded_lane_partition_test.go and
// the 80-cell body table in urlencoded_content_path_test.go, both shared
// byte-for-byte with the other two engines.
func encodingUsesSerialization(enc *openapi3.Encoding) bool {
	if enc == nil {
		return false
	}
	return enc.Style != "" || enc.Explode != nil || enc.AllowReserved
}

func validateMultipartSerializationMethod(name string, schema *openapi3.Schema, enc *openapi3.Encoding, is30 bool) error {
	method := revision3EncodingSerializationMethod(enc)
	resolved := resolveDeclaration(schema, is30)
	switch method.Style {
	case openapi3.SerializationForm:
	case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
		if method.Explode {
			return fmt.Errorf("multipart part %q style %q has no explode=true cell", name, method.Style)
		}
		if resolved.declaresOnly("null", "boolean", "number", "integer", "string") {
			return fmt.Errorf("multipart part %q style %q is defined only for arrays or objects", name, method.Style)
		}
	case openapi3.SerializationDeepObject:
		if !method.Explode {
			return fmt.Errorf("multipart part %q style deepObject has no explode=false cell", name)
		}
		if resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array") {
			return fmt.Errorf("multipart part %q style deepObject is defined only for objects", name)
		}
	default:
		return fmt.Errorf("multipart part %q declares unsupported encoding style %q", name, method.Style)
	}
	if member := styleLaneUndefinedExpansionMember(schema, is30); member != "" {
		return fmt.Errorf("multipart part %q member %q has no expansion defined for style %q", name, name+member, method.Style)
	}
	return nil
}

// styleLaneUndefinedExpansionMember reports the first DECLARED member of a
// resolved RFC6570-style-lane schema whose expansion the governing OAS style
// row leaves undefined. It answers "[]" for an array's items, ".<name>" for
// an object's property, and "" when no declared member offends. Members are
// visited in code-point order, so both engines name the same one.
//
// A member offends when its resolved schema is `object` or `array`. Every
// style this lane serves expands a composite value exactly one level: each
// member becomes a member STRING. A member that is itself composite has no
// representation, and the refusal is decided by the DECLARATION, not by a
// supplied value — every value conforming to such a declaration is composite,
// so an operation admitted here would be one this candidate is statically
// guaranteed to refuse. That is why the exclusion is accounted at synthesis
// rather than raised at invocation.
//
// WHERE THE AUTHORITY SPEAKS, AND WHERE IT DOES NOT. Read per edition, because
// section 2 of the binding specification reads every accepted edition under its
// own immutable text and does not aggregate them. The style table is section
// 4.7.12.3 "Style Values" on the 3.0 line and section 4.8.12.3 on the 3.1 line.
//
//	form              Every accepted edition's row cites the incorporated
//	                  authority by section: "Form style parameters defined by
//	                  [RFC6570] Section 3.2.8." RFC 6570 section 2.4.2 states
//	                  that an explode modifier applies "the expansion process
//	                  ... to each member of the composite as if it were listed
//	                  as a separate variable", and the expansions it defines
//	                  append member strings. A member that is itself a list or
//	                  an associative array has no expansion there.
//	spaceDelimited,   No accepted edition's row cites any RFC section. The
//	pipeDelimited     rows read "Space separated array values" / "Pipe
//	                  separated array values" on 3.0.0 through 3.0.3, and
//	                  "... array values or object properties and values" on
//	                  3.0.4 and the 3.1 line.
//	deepObject        No accepted edition's row cites any RFC section either,
//	                  and the row's own text differs by edition:
//
//	                    3.0.0, 3.0.1, 3.0.2, 3.0.3, 3.1.0
//	                      "deepObject | object | query | Provides a simple way
//	                      of rendering nested objects using form parameters."
//	                      The row GOVERNS this case and does not DEFINE it.
//	                      The worked example beside it has scalar members only
//	                      (color[R]=100&color[G]=200&color[B]=150).
//
//	                    3.0.4, 3.1.1, 3.1.2
//	                      "Allows objects with scalar properties to be
//	                      represented using form parameters. The
//	                      representation of array or object properties is not
//	                      defined."
//
// The later editions say in words what the earlier ones leave undefined; on no
// accepted edition does an authority this specification incorporates supply a
// representation for a composite member. So this function refuses and the
// synthesizer accounts the exclusion. Nothing here authors bytes for the
// member: an interpretation choice for these cells remains available to a
// future revision and is deliberately not taken.
//
// Reproduce the per-edition presence pattern (edition order 3.0.0, 3.0.1,
// 3.0.2, 3.0.3, 3.0.4, 3.1.0, 3.1.1, 3.1.2):
//
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "Provides a simple way of rendering nested objects using form \
//	   parameters"                                           -> 1/1/1/1/0/1/0/0
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "The representation of array or object properties is not defined"
//	                                                         -> 0/0/0/0/1/0/1/1
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "Form style parameters defined by \[RFC6570\] Section 3\.2\.8"
//	                                                         -> 1/1/1/1/1/1/1/1
//
// (each --count is one line). The digests of the renderings those counts run
// over are recorded in corpus-lab/OPENAPI-RUNTIME-INDEX.md.
//
// Pinned by the shared style-lane-composite-member-cases.json case table,
// carried byte-for-byte by the other two engines. Package:
// design/openapi-style-lane-composite-member-ruling.md, RULED 2026-08-18.
func styleLaneUndefinedExpansionMember(schema *openapi3.Schema, is30 bool) string {
	resolved := resolveDeclaration(schema, is30)
	if resolved.declaresOnly("array") {
		if resolved.items().declaresOnly("object", "array") {
			return "[]"
		}
		return ""
	}
	if !resolved.declaresOnly("object") {
		return ""
	}
	names := resolved.propertyNames()
	sort.Strings(names)
	for _, name := range names {
		if resolved.property(name).declaresOnly("object", "array") {
			return "." + name
		}
	}
	return ""
}

// parameterStyleLaneUndefinedExpansionMember reports a style-lane parameter's
// first offending declared member as "<parameter><member path>", or "" when
// the parameter is not on the style lane or declares no offending member.
//
// Every compound-capable Parameter style uses the same proof. The governing
// location/style cell is checked separately; this function only answers the
// nested-member question.
func parameterStyleLaneUndefinedExpansionMember(p *openapi3.Parameter, is30 bool) string {
	if p == nil || len(p.Content) > 0 || p.Schema == nil {
		return ""
	}
	method, err := revision3ParameterSerializationMethod(p)
	if err != nil {
		return ""
	}
	switch method.Style {
	case openapi3.SerializationForm, openapi3.SerializationSpaceDelimited,
		openapi3.SerializationPipeDelimited, openapi3.SerializationDeepObject,
		openapi3.SerializationSimple, openapi3.SerializationLabel, openapi3.SerializationMatrix:
	default:
		return ""
	}
	member := styleLaneUndefinedExpansionMember(p.Schema.Value, is30)
	if member == "" {
		return ""
	}
	return p.Name + member
}

// styleLaneUndefinedExpansionParamFor reports the first effective parameter
// whose style-lane declaration carries a member with no defined expansion.
// Parameters are visited in their effective declaration order.
func styleLaneUndefinedExpansionParamFor(params openapi3.Parameters, bindingSpec string, is30 bool) string {
	if !hasMediaFidelity(bindingSpec) {
		return ""
	}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		if member := parameterStyleLaneUndefinedExpansionMember(ref.Value, is30); member != "" {
			return member
		}
	}
	return ""
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
		if is30 {
			return parsedMediaType{}, fmt.Errorf("a typeless OAS 3.0 part requires configuration.propertyMedia")
		}
		return parseRevision3MediaType("application/octet-stream")
	}
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		if !literal {
			return parsedMediaType{}, fmt.Errorf("an unsatisfiable false part schema admits no value")
		}
		schema = &openapi3.Schema{} // true is the same unconstrained declaration as {}
	}
	declaration := resolveDeclaration(schema, is30)
	if declaration.ambiguous {
		return parsedMediaType{}, fmt.Errorf("part schema declares a choice applicator that does not collapse to one non-null branch; no single part carriage is defined")
	}
	if len(declaration.types) == 0 && !declaration.typeless() {
		return parsedMediaType{}, fmt.Errorf("part schema admits no resolved type with a faithful part carriage")
	}
	contentEncoding, encodingConflict := resolvedSchemaKeywordString(schema, "contentEncoding")
	if encodingConflict {
		return parsedMediaType{}, fmt.Errorf("resolved schema declares conflicting contentEncoding values")
	}
	if contentEncoding != "" && !httpToken(contentEncoding) {
		return parsedMediaType{}, fmt.Errorf("contentEncoding %q is not a valid HTTP token", contentEncoding)
	}
	_, mediaConflict := resolvedSchemaKeywordString(schema, "contentMediaType")
	if mediaConflict {
		return parsedMediaType{}, fmt.Errorf("resolved schema declares conflicting contentMediaType values")
	}
	// A declared non-string part carrying contentEncoding or contentMediaType
	// is NOT refused. Both keywords belong to [JSON Schema 2020-12]'s Content
	// vocabulary, whose Section 8.1 makes them annotations that "do not
	// function as validation assertions", and whose Section 8.3 and Section
	// 8.4 each open on the instance being a string ("If the instance value is
	// a string" / "If the instance is a string"). On a declaration that names
	// a non-string type they are therefore inert, and every accepted 3.1
	// edition states what such a part gets instead — see the per-row basis on
	// defaultRevision3PartContentType. The part-Content-Type rows below key
	// application/octet-stream on a declared string, which is the whole force
	// the keywords have here.

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
			if partSchemaDeclaresNoType(schema, map[*openapi3.Schema]bool{}) {
				if schema.Type != nil {
					// `type` is present but names no type at all.
					if is30 {
						return parsedMediaType{}, fmt.Errorf("part schema declares an array-valued `type`, but an OAS 3.0 `type` MUST be a string and multiple types via an array are not supported; no part carriage is defined")
					}
					// No accepted 3.1 default contentType row reaches it —
					// 3.1.1 and 3.1.2 key their first row on `type` being
					// ABSENT, and 3.1.0's catch-all keys on the property type
					// it does not have — and JSON Schema 2020-12's own
					// meta-schema requires an array-valued `type` to carry at
					// least one member, so the declaration admits no instance.
					return parsedMediaType{}, fmt.Errorf("part schema declares an empty `type`, which no accepted OAS 3.1 default part Content-Type row reaches and which admits no instance; no part carriage is defined")
				}
				if !is30 {
					return parseRevision3MediaType("application/octet-stream")
				}
				// The 3.0 line states no row for it at all: 3.0.0 through
				// 3.0.3 enumerate a `string` with `format: binary`, "other
				// primitive types", `object` and `array` and close without a
				// catch-all, and 3.0.4 tabulates the same cases keyed on
				// `type`. Every stated row is keyed on a declared `type` and
				// none reaches a declaration carrying none. This
				// specification authors no default for that residue. The
				// consumer supplies the missing concrete type through the
				// propertyMedia configuration point.
				return parsedMediaType{}, fmt.Errorf("a typeless OAS 3.0 part requires configuration.propertyMedia")
			}
			if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 {
				return parsedMediaType{}, fmt.Errorf("part schema declares a choice applicator that does not collapse to one non-null branch; no single part carriage is defined")
			}
			if types, union := nonCollapsingUnionType(schema); union {
				if is30 {
					return parsedMediaType{}, fmt.Errorf("part schema declares type %v, but an OAS 3.0 `type` MUST be a string and multiple types via an array are not supported; no part carriage is defined", types)
				}
				return parsedMediaType{}, fmt.Errorf("part schema declares union type %v, which does not collapse to one non-null type; no single part carriage is defined", types)
			}
			if !is30 {
				return parseRevision3MediaType("application/octet-stream")
			}
			return parsedMediaType{}, fmt.Errorf("a typeless OAS 3.0 part requires configuration.propertyMedia")
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

// defaultRevision3PartContentType applies the accepted editions' own default
// part-Content-Type rows. Every row is keyed on the DECLARED type:
//
//   - 3.0.0-3.0.3 and 3.0.4 key application/octet-stream on "a `type: string`
//     with `format: binary` or `format: base64`"; `contentEncoding` is not in
//     the 3.0 Schema Object's dialect at all and appears zero times in the
//     first four editions' text.
//   - 3.1.0 Section 4.8.14.5 keys it on "a `type: string` with a
//     `contentEncoding`", and gives primitives text/plain and complex values
//     application/json without reference to the keyword.
//   - 3.1.1 and 3.1.2 Section 4.8.15.1.1 tabulate `string` x contentEncoding
//     present -> application/octet-stream, and give `number, integer, or
//     boolean` -> text/plain, `object` -> application/json and `array` ->
//     "according to the type of the items schema" an `n/a` in the
//     contentEncoding column, which the table's own note defines as "the
//     presence or value of contentEncoding is irrelevant".
//
// So the encoded row requires a declared string in all three 3.1 editions, and
// a declared non-string keeps its own row. No accepted edition states a
// refusal for that combination.
func defaultRevision3PartContentType(schema *openapi3.Schema, is30 bool) (string, bool) {
	declaration := resolveDeclaration(schema, is30)
	switch {
	case !is30 && declaration.typeless():
		return "application/octet-stream", true
	case schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && is30 &&
		(binarySignaled(schema, true) || byteFormatSignaled(schema)):
		// 3.0.4's default-`contentType` table gives `string` with `format`
		// "`binary` or `byte`" the default application/octet-stream, while
		// 3.0.0 through 3.0.3's prose names only `binary` before "other
		// primitive types". Under the editions' own §4.1 patch-uniformity
		// instruction the line answers uniformly, and 3.0.4 read under its
		// own text fixes what the uniform default is (§9.2 in both family documents).
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
	revision3PropertyRawOctets
	// revision3PropertyArtifactEncoded carries a string the ARTIFACT declares
	// to be already-encoded text: the characters ride the wire unchanged and
	// the OpenBindings raw-byte boundary decode never runs. Each accepted line
	// spells that declaration in its own vocabulary, and §9.2 states them as
	// parallels of one another -- the 3.1 line with `contentEncoding` on a
	// declared string, the 3.0 line with `format: byte`, which every 3.0
	// edition's own format registry defines as "base64 encoded characters".
	revision3PropertyArtifactEncoded
)

// artifactEncodedStringProperty reports §9.2's artifact-encoded string cell
// under the governing edition's own vocabulary. The edition gate is here and
// not at the call sites because the two spellings are inert on each other's
// line: `contentEncoding` is not in the 3.0 Schema Object's dialect at all,
// and on the 3.1 line `format` is an annotation with no content-encoding
// force and `byte` is absent from the format tables.
func artifactEncodedStringProperty(schema *openapi3.Schema, is30 bool) bool {
	if !schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) {
		return false
	}
	if is30 {
		return byteFormatSignaled(schema)
	}
	return schemaHasContentEncoding(schema)
}

func revision3PropertyCarriage(schema *openapi3.Schema, contentType parsedMediaType, is30, allowRaw30 bool) (revision3PropertyCarriageMode, error) {
	declaration := resolveDeclaration(schema, is30)
	if artifactEncodedStringProperty(schema, is30) {
		return revision3PropertyArtifactEncoded, nil
	}
	if isJSONMediaType(contentType.base) {
		return revision3PropertyJSON, nil
	}
	if is30 && schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) && binarySignaled(schema, true) {
		if allowRaw30 {
			return revision3PropertyRawOctets, nil
		}
		return 0, fmt.Errorf("OAS 3.0 binary has no defined urlencoded octet boundary")
	}
	if declaration.typeless() {
		if allowRaw30 {
			return revision3PropertyRawOctets, nil
		}
		return 0, fmt.Errorf("a typeless property has no defined urlencoded octet boundary")
	}
	if contentType.base == "text/plain" {
		if schemaTypeIs(schema, "string", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "number", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "integer", map[*openapi3.Schema]bool{}) ||
			schemaTypeIs(schema, "boolean", map[*openapi3.Schema]bool{}) {
			return revision3PropertyText, nil
		}
	}
	return 0, fmt.Errorf("Content-Type %q has no defined native property serializer", contentType.canonical)
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

func octetRequestCarriage(doc *openapi3.T, media *openapi3.MediaType, bindingSpec string) (bool, bool, error) {
	if doc == nil {
		return false, false, nil
	}
	schema := mediaSchema(media)
	oas30 := isOpenAPI30(majorMinor(doc.OpenAPI))
	declaration := resolveDeclaration(schema, oas30)
	if oas30 {
		if declaration.typeless() && hasSchemaOmittedOAS30ByteCarriage(bindingSpec) {
			return true, true, nil
		}
		format, conflict := declaration.format()
		if conflict {
			return false, false, fmt.Errorf("resolved schema declares conflicting format values")
		}
		if declaration.admitsStringAsSoleNonNullType() && format == "binary" {
			return true, true, nil
		}
		// §9.2: a `type: string` with `format: byte` is ALREADY the encoded
		// characters — every 3.0 edition's own format registry defines `byte`
		// as "base64 encoded characters", 3.0.4 citing RFC 4648 §4 — so the
		// declared value is the body and rides as-is. This is the 3.0 line's
		// parallel of the 3.1 `contentEncoding` rule below, and it takes the
		// same disposition: the byte lane WITHOUT the OpenBindings boundary
		// decode, because artifact encoding and the OpenBindings raw-byte
		// boundary encoding are distinct.
		if declaration.admitsStringAsSoleNonNullType() && format == "byte" {
			return true, false, nil
		}
		return false, false, nil
	}
	// OAS 3.1's schema-omitted, exact non-JSON declaration is the raw shape.
	// Its JSON-domain caller boundary is Base64. Conversely a declared
	// contentEncoding describes the application string itself; those encoded
	// characters ride the wire unchanged.
	if declaration.typeless() {
		return true, true, nil
	}
	encoding, conflict := declaration.keywordString("contentEncoding")
	if conflict {
		return false, false, fmt.Errorf("resolved schema declares conflicting contentEncoding values")
	}
	if declaration.admitsStringAsSoleNonNullType() && encoding != "" {
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
func responseUsesRawBoundary(doc *openapi3.T, media *openapi3.MediaType, actualContentType string, bindingSpec string, exactDeclaration bool) bool {
	actual, err := parseRevision3MediaType(actualContentType)
	if err != nil || isJSONMediaType(actual.base) || strings.HasPrefix(actual.base, "text/") {
		return false
	}
	if doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI)) {
		declaration := resolveDeclaration(mediaSchema(media), true)
		format, conflict := declaration.format()
		return !conflict && ((hasSchemaOmittedOAS30ByteCarriage(bindingSpec) && exactDeclaration && declaration.typeless()) ||
			(declaration.admitsStringAsSoleNonNullType() && format == "binary"))
	}
	return resolveDeclaration(mediaSchema(media), false).typeless()
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
		return false, nil, fmt.Errorf("conditional/combinatorial request schema has no single declaration-defined flattened surface in the registered OpenAPI binding family revision 1")
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

func configuredRequestPlansFor(doc *openapi3.T, op *openapi3.Operation, plans []*bodyPlan, bindCtx map[string]any, bindingSpec string) ([]*bodyPlan, error) {
	cfg := invoke.ContextConfiguration(bindCtx)
	raw, configured := cfg["requestMedia"]
	if !configured || raw == nil {
		if !hasMediaFidelity(bindingSpec) {
			return plans, nil
		}
		if soleConcreteRequestPlan(op, plans) != nil {
			return plans, nil
		}
		if op != nil && op.RequestBody != nil && op.RequestBody.Value != nil && op.RequestBody.Value.Required {
			return nil, &configRequired{
				point:       "requestMedia",
				path:        "",
				description: "OpenAPI request body requires a concrete requestMedia selection",
			}
		}
		if len(plans) > 0 {
			if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
				return nil, fmt.Errorf("OpenAPI request body requires configuration.requestMedia before this supplied optional body can be dispatched")
			}
		}
		return nil, fmt.Errorf("request body has no admissible media candidate")
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

// soleConcreteRequestPlan implements the ratified usable-set election:
// exactly one USABLE concrete alternative (after confinement removes
// excluded and colliding entries) self-selects; the authored map size is
// irrelevant. Two or more usable alternatives require requestMedia, and
// supplied values never elect.
func soleConcreteRequestPlan(op *openapi3.Operation, plans []*bodyPlan) *bodyPlan {
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || len(plans) != 1 {
		return nil
	}
	plan := plans[0]
	if plan == nil || plan.mediaRange {
		return nil
	}
	declared, err := parseMediaDeclaration(plan.mediaKey)
	if err != nil || declared.rangeSpecificity != 2 {
		return nil
	}
	return plan
}

func selectRevision3RequestPlan(doc *openapi3.T, op *openapi3.Operation, plans []*bodyPlan, wanted parsedMediaType, bindingSpec string) ([]*bodyPlan, error) {
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
	// §9.2 normalized collision, confined: a colliding parsed identity is not
	// a selectable declaration, so a configured media type that would land on
	// one matches nothing here rather than "ambiguously matching" the two
	// spellings of one identity. Non-colliding declarations are untouched.
	colliding := normalizedMediaCollisions(op.RequestBody.Value.Content, bindingSpec)
	for _, key := range keys {
		if _, collides := colliding[key]; collides {
			continue
		}
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
		return nil, fmt.Errorf("configured requestMedia %q selects declaration %q, which has no application-value carriage", wanted.canonical, selected.key)
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
	plan.propertyMedia = requiredPropertyMediaNames(plan)
	plan.propertyMediaDeclarations = propertyMediaDeclarations(plan)
	plan.rawProperties = rawPropertyNames(plan)
	applyRevision3BodyShape(plan)
	if hasDynamicObjectCarriage(bindingSpec) {
		applyDynamicObjectShape(plan)
	}
	if plan.synthetic != skeleton.synthetic || plan.wholeObject != skeleton.wholeObject {
		return nil, fmt.Errorf("configured requestMedia %q selects range %q with no single application-value body shape", wanted.canonical, selected.key)
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
	raw, configured := invoke.ContextConfiguration(bindCtx)["requestMedia"]
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
			// §9.2 selects this lane from the declaration alone, so a
			// non-string value is a pre-dispatch refusal against the
			// artifact's own type, not a change of candidate set.
			return nil, "", fmt.Errorf("request media %s declares a string body but the supplied value is %T, not a string", plan.mediaType, routed.bodyValue)
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
// Multipart (openbindings.openapi-3.0@1 §9.2;
// openbindings.openapi-3.1@1 §9.2)
// ---------------------------------------------------------------------------

// buildMultipartBodyForRevision encodes body fields as multipart/form-data
// for the exact binding family token in scope. Revisions 1 and 2 retain their
// legacy edition-aware binary decoder. Revision 3 instead treats only OAS 3.0
// format:binary as a canonical Base64 raw-byte boundary; OAS 3.1
// contentEncoding/contentMediaType strings ride as artifact text. Other parts
// follow the artifact's encoding object or OAS per-type defaults. Fields are
// written in sorted order for a deterministic body.
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
		if !multipartPropertyNameSafe(name) {
			return nil, "", fmt.Errorf("multipart property name %q contains CR or LF", name)
		}
		value := fields[name]
		propSchema := resolvedMultipartPropertyFor(
			schema,
			name,
			map[*openapi3.Schema]bool{},
			hasDynamicObjectCarriage(bindingSpec),
			is30,
		)
		if hasMediaFidelity(bindingSpec) {
			var partNullable bool
			propSchema, partNullable = effectiveRevision3PartSchema(propSchema, is30)
			if partNullable && value == nil {
				continue // §9.2: a JSON null value elides the nullable optional part
			}
		}
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
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
				// The write lane refuses exactly what the admission lane
				// refuses. validateRevision3Multipart excludes a nested array
				// declaration from the candidate set, so a plan carrying one
				// cannot be selected; without this the two lanes disagree
				// about the same declaration, and the disagreement is only
				// invisible because something else happens to exclude it
				// first. Same decision, each engine's own wording.
				if hasMediaFidelity(bindingSpec) && schemaTypeIs(items, "array", map[*openapi3.Schema]bool{}) {
					return nil, "", fmt.Errorf("multipart part %q has nested array items with no defined repeated-part mapping", name)
				}
				for _, elem := range arr {
					if err := writeMultipartPart(writer, name, elem, items, enc, is30, bindingSpec); err != nil {
						return nil, "", err
					}
				}
				continue
			}
			// §9.2: "An invalid value or a member for which the resolved schema
			// leaves no faithful form carriage refuses before dispatch." The
			// declaration decides this part's carriage — an array property
			// rides as one part per element — so a supplied value with no
			// elements to carry has no faithful form carriage at all. Falling
			// through would serialize it against the whole-property schema and
			// send one application/json part the declaration never described,
			// which is the silent-carriage outcome the rule exists to prevent.
			if hasMediaFidelity(bindingSpec) {
				return nil, "", fmt.Errorf("multipart part %q is declared as an array but the supplied value is %T, which carries no elements", name, value)
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

// resolvedMultipartProperty resolves the STATIC chain, which reads neither
// patternProperties nor additionalProperties, so the governing edition's
// dialect never enters it.
func resolvedMultipartProperty(schema *openapi3.Schema, name string, seen map[*openapi3.Schema]bool) *openapi3.Schema {
	return resolvedMultipartPropertyFor(schema, name, seen, false, false)
}

// effectiveRevision3PartSchema applies this candidate's part-schema
// interpretations before carriage selection (§9.2 of the binding
// specification). The boolean literal true is the same unconstrained
// declaration as {}. A resolved top level declaring exactly one anyOf/oneOf
// whose branches comprise one non-null branch beside {type: "null"}-only
// branches collapses to that non-null branch — the artifact declares exactly
// one carriage, so this is schema interpretation, not branch selection — and
// the returned nullable flag lets a JSON null value elide the optional part.
// The union-type spelling of the same declaration takes the same collapse;
// see collapsedNullableTypeMember.
func effectiveRevision3PartSchema(schema *openapi3.Schema, is30 bool) (*openapi3.Schema, bool) {
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		if literal {
			return &openapi3.Schema{}, false
		}
		return schema, false
	}
	if collapsed, ok := collapsedNullableChoiceBranch(schema); ok {
		return collapsed, true
	}
	if member, ok := collapsedNullableTypeMember(schema, is30); ok {
		// The sibling keywords are the same schema's own assertions and keep
		// applying: only the union spelling of the type is resolved.
		collapsed := *schema
		collapsed.Type = &openapi3.Types{member}
		return &collapsed, true
	}
	resolved := resolveDeclaration(schema, is30)
	if member, ok := resolved.soleNonNullType(); ok && resolved.admitsNull() {
		// OAS 3.0 spells this as `type: <member>, nullable: true` in
		// the same Schema Object. Keeping null admission as a separate form
		// disposition lets the non-null member select exactly one carriage.
		collapsed := *schema
		collapsed.Type = &openapi3.Types{member}
		collapsed.Nullable = false
		return &collapsed, true
	}
	return schema, false
}

// collapsedNullableTypeMember reports the union-type spelling of the same
// nullable declaration collapsedNullableChoiceBranch handles. JSON Schema
// 2020-12 §6.1.1 defines an array-valued `type` as a union — "If the value of
// `type` is an array, then an instance validates successfully if its type
// matches any of the types indicated by the strings in the array" — so
// {"type": ["string", "null"]} asserts exactly what
// {"anyOf": [{"type": "string"}, {"type": "null"}]} asserts, and takes the
// same collapse to its single non-null member. Two or more non-null members
// declare value-dependent alternatives, leave no single faithful part
// carriage, and do not collapse.
//
// The union spelling belongs to the OAS 3.1 line, which adopts JSON Schema
// 2020-12 wholesale. Every 3.0 edition states of the Schema Object: "type -
// Value MUST be a string. Multiple types via an array are not supported"
// (3.0.0 §Schema Object through 3.0.4, verbatim in all five). An array-valued
// `type` under that line is therefore not a union declaration this
// specification can read, and gains no part carriage from one.
//
// The collapse reads the resolved top level only, exactly as the choice
// collapse does; a union reached through allOf is not rewritten.
func collapsedNullableTypeMember(schema *openapi3.Schema, is30 bool) (string, bool) {
	if schema == nil || is30 || schema.Type == nil {
		return "", false
	}
	types := schema.Type.Slice()
	if len(types) < 2 {
		return "", false // a single-member `type` already denotes that member
	}
	member := ""
	for _, candidate := range types {
		if candidate == "null" {
			continue
		}
		if member != "" {
			return "", false
		}
		member = candidate
	}
	if member == "" {
		return "", false // "null" alone declares no carriable value
	}
	return member, true
}

// nonCollapsingUnionType reports a resolved top-level array-valued `type`
// that collapsedNullableTypeMember declined, so a caller can say which of the
// two reasons applies instead of reporting the schema as typeless.
func nonCollapsingUnionType(schema *openapi3.Schema) ([]string, bool) {
	if schema == nil || schema.Type == nil {
		return nil, false
	}
	types := schema.Type.Slice()
	if len(types) < 2 {
		return nil, false
	}
	return types, true
}

// collapsedNullableChoiceBranch reports the single-non-null-branch collapse:
// the schema's top level declares exactly one of anyOf/oneOf, no type and no
// other applicator, and the branches are exactly one non-null branch with
// every remaining branch declaring {type: "null"} alone. A choice with more
// than one non-null branch declares value-dependent alternatives and does
// not collapse.
func collapsedNullableChoiceBranch(schema *openapi3.Schema) (*openapi3.Schema, bool) {
	if schema == nil {
		return nil, false
	}
	branches := schema.AnyOf
	if len(schema.OneOf) > 0 {
		if len(branches) > 0 {
			return nil, false
		}
		branches = schema.OneOf
	}
	if len(branches) < 2 {
		return nil, false
	}
	if (schema.Type != nil && len(schema.Type.Slice()) > 0) || len(schema.AllOf) > 0 || schema.Not != nil ||
		schema.If != nil || schema.Then != nil || schema.Else != nil || len(schema.DependentSchemas) > 0 ||
		len(schema.Properties) > 0 || schema.Items != nil {
		return nil, false
	}
	var nonNull *openapi3.Schema
	for _, branch := range branches {
		if branch == nil || branch.Value == nil {
			return nil, false
		}
		if nullOnlyBranch(branch.Value) {
			continue
		}
		if nonNull != nil {
			return nil, false
		}
		nonNull = branch.Value
	}
	if nonNull == nil {
		return nil, false
	}
	return nonNull, true
}

// nullOnlyBranch reports a branch declaring {type: "null"} alone (annotations
// aside): the null arm of the nullable-choice idiom.
func nullOnlyBranch(schema *openapi3.Schema) bool {
	if schema == nil || schema.Type == nil {
		return false
	}
	types := schema.Type.Slice()
	if len(types) != 1 || types[0] != "null" {
		return false
	}
	return len(schema.AnyOf) == 0 && len(schema.OneOf) == 0 && len(schema.AllOf) == 0 && schema.Not == nil &&
		len(schema.Properties) == 0 && schema.Items == nil && len(schema.Enum) == 0
}

// partSchemaDeclaresNoType reports the §9.2 type-absent part cell: a resolved
// part declaration carrying no type and no choice or conditional applicator,
// through allOf. Such a part refuses before dispatch on every accepted
// edition — the 3.1 line states a default this revision defines no boundary
// to cross, the 3.0 line states no row at all and this revision fills
// nothing — and no supplied value's JSON type ever selects its carriage. An
// absent schema is not a declaration and is answered by the caller's own
// absent-schema branch.
func partSchemaDeclaresNoType(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		return literal
	}
	seen[schema] = true
	defer delete(seen, schema)
	if schema.Type != nil && len(schema.Type.Slice()) > 0 {
		return false
	}
	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || schema.Not != nil ||
		schema.If != nil || schema.Then != nil || schema.Else != nil || len(schema.DependentSchemas) > 0 ||
		schema.Format == "binary" {
		return false
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		if !partSchemaDeclaresNoType(member.Value, seen) {
			return false
		}
	}
	return true
}

// resolvedMultipartPropertyFor walks §9.2's dynamic-member resolution
// chain: an exact `properties` schema and every matching `patternProperties`
// schema, or the `additionalProperties` schema when neither matches. The
// resolution keywords are read under the governing edition's dialect
// (§9.1/§9.2): on the 3.0 line `patternProperties` has no meaning, so the
// chain there is `properties`, then `additionalProperties`.
func resolvedMultipartPropertyFor(schema *openapi3.Schema, name string, seen map[*openapi3.Schema]bool, dynamic, oas30 bool) *openapi3.Schema {
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
		if !oas30 {
			for pattern := range schema.PatternProperties {
				patterns = append(patterns, pattern)
			}
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
		if match := resolvedMultipartPropertyFor(member.Value, name, seen, dynamic, oas30); match != nil {
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
	parsedContentType, err := revision3PartContentType(schema, enc, is30)
	if err != nil {
		return fmt.Errorf("multipart part %q: %w", name, err)
	}

	mode, err := revision3PropertyCarriage(schema, parsedContentType, is30, true)
	if err != nil {
		return fmt.Errorf("multipart part %q: %w", name, err)
	}
	rawBinary := mode == revision3PropertyRawOctets
	var body []byte
	switch mode {
	case revision3PropertyRawOctets:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("binary part %q: the value must be a string carrying canonical Base64 bytes, got %T", name, value)
		}
		body, err = canonicalBase64BoundaryBytes(name, text)
	case revision3PropertyJSON:
		body, err = json.Marshal(value)
	case revision3PropertyArtifactEncoded:
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
	if is30 && byteFormatSignaled(schema) {
		emit, err := openAPI30Base64TransferHeader(enc)
		if err != nil {
			return fmt.Errorf("multipart part %q: %w", name, err)
		}
		if emit {
			h.Set("Content-Transfer-Encoding", "base64")
		}
	}
	// OAS 3.1 contentEncoding is an artifact annotation, not a part-header
	// emission instruction. Other Encoding headers are descriptive only.
	part, err := writer.CreatePart(h)
	if err != nil {
		return fmt.Errorf("create part %q: %w", name, err)
	}
	if _, err := part.Write(body); err != nil {
		return fmt.Errorf("write part %q: %w", name, err)
	}
	return nil
}

func openAPI30Base64TransferHeader(encoding *openapi3.Encoding) (bool, error) {
	if encoding == nil {
		return false, nil
	}
	var header *openapi3.Header
	for name, ref := range encoding.Headers {
		if strings.EqualFold(name, "Content-Transfer-Encoding") && ref != nil {
			header = ref.Value
			break
		}
	}
	if header == nil {
		return false, nil
	}
	if header.Schema == nil || header.Schema.Value == nil {
		return false, fmt.Errorf("explicit Content-Transfer-Encoding Header does not admit base64")
	}
	declaration := resolveDeclaration(header.Schema.Value, true)
	if declaration.ambiguous || len(declaration.types) > 0 && !declaration.types["string"] {
		return false, fmt.Errorf("explicit Content-Transfer-Encoding Header does not admit base64")
	}
	for _, conjunct := range declaration.conjuncts {
		if conjunct == nil || len(conjunct.Enum) == 0 {
			continue
		}
		admitted := false
		for _, member := range conjunct.Enum {
			if text, ok := member.(string); ok && text == "base64" {
				admitted = true
				break
			}
		}
		if !admitted {
			return false, fmt.Errorf("explicit Content-Transfer-Encoding Header disallows base64")
		}
	}
	return true, nil
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

// byteFormatSignaled reports the 3.0 line's `format: byte` declaration,
// including a `format` contributed through allOf. Every accepted 3.0 edition
// defines `byte` in its own Data Types format registry as "base64 encoded
// characters" (3.0.4's row citing [RFC 4648] §4), so the declared value IS
// the encoded characters. The keyword has no such force on the 3.1 line,
// where `format` is an annotation with no content-encoding effect and `byte`
// is absent from the format tables, so every caller gates this on the
// edition rather than the predicate doing it silently.
func byteFormatSignaled(schema *openapi3.Schema) bool {
	return byteFormatSignaledWithSeen(schema, map[*openapi3.Schema]bool{})
}

func byteFormatSignaledWithSeen(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	defer delete(seen, schema)
	if schema.Format == "byte" {
		return true
	}
	for _, member := range schema.AllOf {
		if member != nil && byteFormatSignaledWithSeen(member.Value, seen) {
			return true
		}
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

// buildURLEncodedBodyForRevision serializes body fields per the OAS
// `encoding` rules for the exact binding family token in scope: each field's
// style/explode/allowReserved come from the media type's encoding object where
// present, defaulting to form/explode=true. Fields are serialized with the
// same expansions as query parameters and joined in sorted-name order for a
// deterministic body.
func buildURLEncodedBodyForRevision(doc *openapi3.T, media *openapi3.MediaType, fields map[string]any, bindingSpec string) (string, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var units []string
	schema := mediaSchema(media)
	is30 := doc != nil && isOpenAPI30(majorMinor(doc.OpenAPI))
	for _, name := range names {
		var enc *openapi3.Encoding
		if media != nil {
			enc = media.Encoding[name]
		}
		if hasMediaFidelity(bindingSpec) && !encodingUsesSerialization(enc) {
			propertySchema := resolvedMultipartPropertyFor(
				schema,
				name,
				map[*openapi3.Schema]bool{},
				hasDynamicObjectCarriage(bindingSpec),
				is30,
			)
			var propertyNullable bool
			propertySchema, propertyNullable = effectiveRevision3PartSchema(propertySchema, is30)
			if propertyNullable && fields[name] == nil {
				continue // §9.2: a JSON null value elides the nullable optional field
			}
			contentType, err := revision3PartContentType(propertySchema, enc, is30)
			if err != nil {
				return "", fmt.Errorf("form field %q: %w", name, err)
			}
			value, err := revision3PropertyBytes(name, fields[name], propertySchema, contentType, is30)
			if err != nil {
				return "", fmt.Errorf("form field %q: %w", name, err)
			}
			units = append(units, formURLEncodedEscape(name)+"="+formURLEncodedEscape(string(value)))
			continue
		}
		style, explode, allowReserved := openapi3.SerializationForm, true, false
		if enc != nil {
			sm := enc.SerializationMethod()
			style, explode = sm.Style, sm.Explode
			allowReserved = enc.AllowReserved
		}
		u, err := serializeQueryValueForRevision(name, fields[name], style, explode, allowReserved, bindingSpec, true)
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
	case revision3PropertyRawOctets:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("binary value must be a canonical Base64 string, got %T", value)
		}
		return canonicalBase64BoundaryBytes(name, text)
	case revision3PropertyArtifactEncoded:
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
	return nil, fmt.Errorf("unknown property carriage")
}

// ---------------------------------------------------------------------------
// Success responses and streaming capability (§8, §9.2)
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

// governingResponseMediaFor selects the one concrete declaration in the
// governing Response Object whose type/subtype and declared parameters are a
// subset of the actual Content-Type under the named binding family. Greatest
// parameter specificity wins; a tie is ambiguous and loud.
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
	colliding := normalizedMediaCollisions(response.Content, bindingSpec)
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
		if _, collides := colliding[key]; collides {
			// §9.2 normalized collision, confined: no response match may be
			// governed by this parsed identity, so the declaration competes
			// for nothing. A concrete response that DOES denote the colliding
			// identity then matches nothing and is loud below; a response
			// denoting a non-colliding sibling decodes unaffected.
			continue
		}
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
		if collidesWithNormalizedIdentity(colliding, parsedActual, bindingSpec) {
			return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type %q denotes a parsed media identity more than one content-map key declares (normalized collision), so no response match may be governed by it", actual)
		}
		return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type %q matches no media in the governing response", actual)
	}
	if len(matches) != 1 {
		return governingResponseMediaMatch{}, fmt.Errorf("response Content-Type %q ambiguously matches %d equally specific declarations", actual, len(matches))
	}
	return matches[0], nil
}

// successMediaTypesFor returns the declared concrete media types that may
// govern a successful response under the named binding family: literal 2xx,
// 2XX, and default declarations. Members retain declaration parameters;
// ordering is an implementation convention.
func successMediaTypesFor(op *openapi3.Operation, bindingSpec string) []string {
	if op == nil || op.Responses == nil {
		return nil
	}
	seen := map[string]bool{}
	// Identity -> the advertised spelling. Two SUCCESS RESPONSES may declare
	// one identity in different spellings; that is not a collision (§9.2's
	// unit is one content map), so the set carries the identity once and the
	// smallest spelling is chosen rather than whichever key iterated first.
	advertised := map[string]string{}
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
		colliding := normalizedMediaCollisions(ref.Value.Content, bindingSpec)
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
			if _, collides := colliding[mt]; collides {
				// §9.2 normalized collision, confined: no response match may
				// be governed by this parsed identity, so it is not an
				// available representation and is not advertised. Advertising
				// it would invite exactly the response the decode lane must
				// refuse. Non-colliding siblings in the map still advertise.
				continue
			}
			identity := parsed.identity
			if hasMediaFidelity(bindingSpec) {
				identity = parsed.semanticIdentity
			}
			if existing, present := advertised[identity]; !present || parsed.canonical < existing {
				advertised[identity] = parsed.canonical
			}
		}
	}
	for _, spelling := range advertised {
		seen[spelling] = true
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

// acceptHeaderFor advertises the declared concrete media types of the
// operation's success responses under the named binding family. Empty
// membership means header omission.
func acceptHeaderFor(op *openapi3.Operation, bindingSpec string) string {
	return strings.Join(successMediaTypesFor(op, bindingSpec), ", ")
}

// isStreamingCapableFor reports the §8 static capability under the named
// binding family: an operation is streaming-capable iff text/event-stream
// appears among the declared media types of its success responses.
func isStreamingCapableFor(op *openapi3.Operation, bindingSpec string) bool {
	for _, mt := range successMediaTypesFor(op, bindingSpec) {
		if hasMediaFidelity(bindingSpec) {
			parsed, err := parseMediaDeclaration(mt)
			if err == nil && mediaRangeBaseMatches(parsed.base, "text/event-stream") {
				return true
			}
		} else if normalizeMediaType(mt) == "text/event-stream" {
			return true
		}
	}
	return false
}
