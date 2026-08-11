package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// This file implements the flattened input model of openbindings.openapi@1
// §9.1 (OAPI-P-02, OAPI-P-03): the caller-facing input value is one JSON
// object — parameters from every location and the request body merged into
// one object — and parameter serialization follows the OAS
// style/explode/allowReserved rules, incorporated wholesale.

// ---------------------------------------------------------------------------
// Effective parameter set
// ---------------------------------------------------------------------------

// effectiveParameters merges path-item and operation `parameters` (operation
// winning on same name-and-location collision, per the OAS) and drops header
// parameters named Accept, Content-Type, or Authorization: the OAS declares
// such parameter definitions SHALL be ignored.
func effectiveParameters(pathItem *openapi3.PathItem, op *openapi3.Operation) openapi3.Parameters {
	merged := mergeParameters(pathItem.Parameters, op.Parameters)
	out := make(openapi3.Parameters, 0, len(merged))
	for _, pref := range merged {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		if p.In == openapi3.ParameterInHeader {
			switch http.CanonicalHeaderKey(p.Name) {
			case "Accept", "Content-Type", "Authorization":
				continue // ignored per the OAS's parameter rules
			}
		}
		out = append(out, pref)
	}
	return out
}

// checkEffectiveParameterOwnership enforces the declaration-only portions of
// OAPI-P-10. Host and Content-Length are owned by the HTTP processor and
// therefore cannot be caller-routed parameters. A raw Cookie header also
// cannot coexist with structured cookie parameters because both would own the
// single HTTP Cookie field. These are artifact-shape failures, independent of
// any invocation input, and must be refused before input consumption.
func checkEffectiveParameterOwnership(params openapi3.Parameters) error {
	var rawCookieHeader bool
	var structuredCookieParameter bool
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		switch p.In {
		case openapi3.ParameterInHeader:
			switch http.CanonicalHeaderKey(p.Name) {
			case "Host", "Content-Length":
				return fmt.Errorf("operation declares processor-owned header parameter %q (OAPI-P-10: unresolvable)", p.Name)
			case "Cookie":
				rawCookieHeader = true
			}
		case openapi3.ParameterInCookie:
			structuredCookieParameter = true
		}
	}
	if rawCookieHeader && structuredCookieParameter {
		return fmt.Errorf("operation declares both a raw Cookie header parameter and structured cookie parameters (OAPI-P-10: unresolvable)")
	}
	return nil
}

// unflattenableParam reports the first parameter name declared in two
// DIFFERENT locations (legal per the OAS's name-plus-location identity, but
// unrepresentable by the flattened model): such an operation is refused
// loudly at binding resolution (OAPI-P-03). Empty string means flattenable.
func unflattenableParam(params openapi3.Parameters) string {
	locs := map[string]string{}
	headerNames := map[string]string{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		if prev, ok := locs[p.Name]; ok && prev != p.In {
			return p.Name
		}
		locs[p.Name] = p.In
		if p.In == openapi3.ParameterInHeader {
			folded := strings.ToLower(p.Name)
			if previous, ok := headerNames[folded]; ok && previous != p.Name {
				return p.Name
			}
			headerNames[folded] = p.Name
		}
	}
	return ""
}

// unflattenableParamForRevision keeps revision 1's complete flattened-model
// refusal while letting the binding-private route disambiguate names across protocol
// locations. Case-distinct header declarations remain unresolvable in both
// revisions because HTTP field names themselves are case-insensitive: no
// routing envelope can create two semantically distinct wire destinations.
func unflattenableParamForRevision(params openapi3.Parameters, bindingSpec string) string {
	if !usesRoutedInput(bindingSpec) {
		return unflattenableParam(params)
	}
	headerNames := map[string]string{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil || pref.Value.In != openapi3.ParameterInHeader {
			continue
		}
		name := pref.Value.Name
		folded := strings.ToLower(name)
		if previous, ok := headerNames[folded]; ok && previous != name {
			return name
		}
		headerNames[folded] = name
	}
	return ""
}

// ---------------------------------------------------------------------------
// Routing (the flatten, wire side)
// ---------------------------------------------------------------------------

// routedInput is the wire-side product of routing one flattened input object
// through the operation's declared surface.
type routedInput struct {
	resolvedPath string   // path template with path parameters substituted
	queryUnits   []string // fully percent-encoded name=value units, declaration order
	headers      [][2]string
	cookieUnits  []string // raw name=value units, declaration order

	bodyFields map[string]any // object-mode body fields
	bodyValue  any            // synthetic-mode body value (§9.1: the `body` property, unwrapped at the wire)
	bodySet    bool

	// populated records which declared parameters the caller populated, per
	// channel ("header" names canonicalized), for the OAPI-P-10
	// credential-collision refusal.
	populated map[string]map[string]bool
}

// routeInput maps one flattened input object onto the wire per §9.1
// (OAPI-P-03):
//
//   - declared parameters ride their location, serialized per the OAS
//     style/explode/allowReserved rules (OAPI-P-02);
//   - parameter/body-property collisions are rejected before this function:
//     independently declared upstream values are never collapsed or
//     duplicated;
//   - a field matching no declared parameter or body property passes through
//     into the body when a request body is declared, and is refused loudly
//     before dispatch when none is declared;
//   - a missing declared path parameter always refuses before dispatch (the
//     URL cannot be built); every other missing member is the server's
//     declared validation's business.
func routeInput(params openapi3.Parameters, input map[string]any, pathTemplate string, plan *bodyPlan) (*routedInput, error) {
	return routeInputFor(params, input, pathTemplate, plan, BindingSpec)
}

func routeInputFor(params openapi3.Parameters, input map[string]any, pathTemplate string, plan *bodyPlan, bindingSpec string) (*routedInput, error) {
	r := &routedInput{
		resolvedPath: pathTemplate,
		bodyFields:   map[string]any{},
		populated: map[string]map[string]bool{
			"header": {}, "query": {}, "cookie": {},
		},
	}

	consumed := map[string]bool{}
	var missingPath []string

	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		value, ok := input[p.Name]
		if !ok {
			if p.In == openapi3.ParameterInPath {
				missingPath = append(missingPath, p.Name)
			}
			continue
		}
		consumed[p.Name] = true

		if err := routeParameterFor(r, p, value, bindingSpec); err != nil {
			return nil, err
		}

	}

	if len(missingPath) > 0 {
		sort.Strings(missingPath)
		return nil, fmt.Errorf("%w(s) %s: the URL cannot be built without them", errMissingPathParam, strings.Join(missingPath, ", "))
	}

	// Fields matching no declared parameter.
	var unmatched []string
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if consumed[name] {
			continue
		}
		value := input[name]
		switch {
		case plan == nil || !plan.declared:
			unmatched = append(unmatched, name)
		case plan.synthetic || plan.wholeObject:
			if name == syntheticBodyProperty {
				r.bodyValue, r.bodySet = value, true
			} else {
				// The flattened contract of a non-object body carries only
				// parameters and the synthetic `body` property; there is no
				// object body to pass through into.
				unmatched = append(unmatched, name)
			}
		case plan.family == familyJSON || plan.props[name]:
			// Evaluation-free body passthrough: no schema evaluation
			// participates in JSON routing, while form and multipart members
			// require an artifact declaration that defines their wire carriage.
			r.bodyFields[name] = value
		default:
			unmatched = append(unmatched, name)
		}
	}
	if len(unmatched) > 0 {
		if plan != nil && plan.declared && (plan.synthetic || plan.wholeObject) {
			return nil, fmt.Errorf("field(s) %s match no declared parameter, and the declared request body uses whole-value carriage (its flattened contract carries only the synthetic %q property)", strings.Join(unmatched, ", "), syntheticBodyProperty)
		}
		if plan != nil && plan.declared {
			return nil, fmt.Errorf("field(s) %s have no declaration-defined carriage for the %s request body", strings.Join(unmatched, ", "), plan.mediaType)
		}
		return nil, fmt.Errorf("field(s) %s match no declared parameter, and the operation declares no request body to pass them through to", strings.Join(unmatched, ", "))
	}

	return r, nil
}

// syntheticBodyProperty is the flattened contract's property for a
// non-object request body (§9.1): at the wire, its value IS the request
// body, unwrapped.
const syntheticBodyProperty = "body"

// errMissingPathParam marks the §9.1 always-refuses case — a supplied input
// missing a declared path parameter — so the caller can map it to
// ERR_MISSING_INPUT rather than the generic validation refusal.
var errMissingPathParam = errors.New("missing path parameter")

// routeParameter serializes one populated parameter onto its wire location.
func routeParameter(r *routedInput, p *openapi3.Parameter, value any) error {
	return routeParameterFor(r, p, value, BindingSpec)
}

func routeParameterFor(r *routedInput, p *openapi3.Parameter, value any, bindingSpec string) error {
	if hasMediaFidelity(bindingSpec) {
		if err := validateRevision3ParameterSerialization(p); err != nil {
			return fmt.Errorf("parameter %q: %w", p.Name, err)
		}
	}
	// A `content`-form parameter (schema-less, a single-entry content map)
	// serializes its value per its declared media type and rides its
	// location as that serialized string (OAPI-P-02).
	if len(p.Content) > 0 {
		serialized, err := serializeParamContentFor(p, value, bindingSpec)
		if err != nil {
			return err
		}
		switch p.In {
		case openapi3.ParameterInPath:
			escaped := encodePathValue(serialized)
			if hasMediaFidelity(bindingSpec) {
				escaped = revision3URIEscape(serialized, false, false)
			}
			r.resolvedPath = strings.ReplaceAll(r.resolvedPath, "{"+p.Name+"}", escaped)
		case openapi3.ParameterInQuery:
			if hasMediaFidelity(bindingSpec) {
				r.queryUnits = append(r.queryUnits, revision3URIEscape(p.Name, false, false)+"="+revision3URIEscape(serialized, p.AllowReserved, false))
			} else {
				r.queryUnits = append(r.queryUnits, queryEscape(p.Name, false)+"="+queryEscape(serialized, p.AllowReserved))
			}
			r.populated["query"][p.Name] = true
		case openapi3.ParameterInHeader:
			r.headers = append(r.headers, [2]string{p.Name, serialized})
			r.populated["header"][http.CanonicalHeaderKey(p.Name)] = true
		case openapi3.ParameterInCookie:
			r.cookieUnits = append(r.cookieUnits, p.Name+"="+serialized)
			r.populated["cookie"][p.Name] = true
		}
		return nil
	}

	sm, err := p.SerializationMethod() // compatibility defaults
	if hasMediaFidelity(bindingSpec) {
		sm, err = revision3ParameterSerializationMethod(p)
	}
	if err != nil {
		return fmt.Errorf("parameter %q: %w", p.Name, err)
	}

	switch p.In {
	case openapi3.ParameterInPath:
		expanded, err := serializePathValueForRevision(p.Name, value, sm.Style, sm.Explode, bindingSpec)
		if err != nil {
			return fmt.Errorf("path parameter %q: %w", p.Name, err)
		}
		r.resolvedPath = strings.ReplaceAll(r.resolvedPath, "{"+p.Name+"}", expanded)
	case openapi3.ParameterInQuery:
		units, err := serializeQueryValueForRevision(p.Name, value, sm.Style, sm.Explode, p.AllowReserved, bindingSpec, false)
		if err != nil {
			return fmt.Errorf("query parameter %q: %w", p.Name, err)
		}
		r.queryUnits = append(r.queryUnits, units...)
		r.populated["query"][p.Name] = true
	case openapi3.ParameterInHeader:
		v, err := serializeHeaderValue(value, sm.Style, sm.Explode)
		if err != nil {
			return fmt.Errorf("header parameter %q: %w", p.Name, err)
		}
		r.headers = append(r.headers, [2]string{p.Name, v})
		r.populated["header"][http.CanonicalHeaderKey(p.Name)] = true
	case openapi3.ParameterInCookie:
		units, err := serializeCookieValue(p.Name, value, sm.Style, sm.Explode)
		if err != nil {
			return fmt.Errorf("cookie parameter %q: %w", p.Name, err)
		}
		r.cookieUnits = append(r.cookieUnits, units...)
		r.populated["cookie"][p.Name] = true
	default:
		return fmt.Errorf("parameter %q: unsupported location %q", p.Name, p.In)
	}
	return nil
}

func validateRevision3ParameterSerialization(p *openapi3.Parameter) error {
	if p == nil || len(p.Content) > 0 {
		return nil
	}
	method, err := revision3ParameterSerializationMethod(p)
	if err != nil {
		return err
	}
	var schema *openapi3.Schema
	if p.Schema != nil {
		schema = p.Schema.Value
	}
	switch p.In {
	case openapi3.ParameterInPath:
		if method.Style != openapi3.SerializationSimple && method.Style != openapi3.SerializationLabel && method.Style != openapi3.SerializationMatrix {
			return fmt.Errorf("style %q is not defined for path parameters", method.Style)
		}
	case openapi3.ParameterInHeader:
		if method.Style != openapi3.SerializationSimple {
			return fmt.Errorf("style %q is not defined for header parameters", method.Style)
		}
	case openapi3.ParameterInCookie:
		if method.Style != openapi3.SerializationForm {
			return fmt.Errorf("style %q is not defined for cookie parameters", method.Style)
		}
	case openapi3.ParameterInQuery:
		switch method.Style {
		case openapi3.SerializationForm:
			return nil
		case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
			if method.Explode || !schemaTypeIs(schema, "array", map[*openapi3.Schema]bool{}) {
				return fmt.Errorf("query style %q is defined only for arrays with explode=false", method.Style)
			}
		case openapi3.SerializationDeepObject:
			if !method.Explode || !schemaTypeIs(schema, "object", map[*openapi3.Schema]bool{}) {
				return fmt.Errorf("query style deepObject is defined only for objects with explode=true")
			}
		default:
			return fmt.Errorf("style %q is not defined for query parameters", method.Style)
		}
	}
	return nil
}

func revision3ParameterSerializationMethod(p *openapi3.Parameter) (*openapi3.SerializationMethod, error) {
	if p == nil {
		return nil, fmt.Errorf("nil parameter")
	}
	style := p.Style
	switch p.In {
	case openapi3.ParameterInPath, openapi3.ParameterInHeader:
		if style == "" {
			style = openapi3.SerializationSimple
		}
	case openapi3.ParameterInQuery, openapi3.ParameterInCookie:
		if style == "" {
			style = openapi3.SerializationForm
		}
	default:
		return nil, fmt.Errorf("unexpected parameter 'in': %q", p.In)
	}
	explode := style == openapi3.SerializationForm
	if p.Explode != nil {
		explode = *p.Explode
	}
	return &openapi3.SerializationMethod{Style: style, Explode: explode}, nil
}

// serializeParamContent serializes a content-form parameter's value per its
// declared media type: JSON family values JSON-serialize; text/plain carries
// a string value verbatim. Any other declared media type has no defined
// parameter carriage in revision 1 and refuses loudly.
func serializeParamContent(p *openapi3.Parameter, value any) (string, error) {
	return serializeParamContentFor(p, value, BindingSpec)
}

func serializeParamContentFor(p *openapi3.Parameter, value any, bindingSpec string) (string, error) {
	if len(p.Content) != 1 {
		return "", fmt.Errorf("parameter %q content must contain exactly one media type", p.Name)
	}
	var mediaKey string
	for k := range p.Content {
		mediaKey = k
		break // the OAS requires exactly one entry
	}
	mt := normalizeMediaType(mediaKey)
	var parsed parsedMediaType
	if hasMediaFidelity(bindingSpec) {
		var err error
		parsed, err = parseRevision3MediaType(mediaKey)
		if err != nil {
			return "", fmt.Errorf("parameter %q declares invalid content %q: %w", p.Name, mediaKey, err)
		}
		mt = parsed.base
	}
	switch {
	case isJSONMediaType(mt):
		b, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("parameter %q: serialize as %s: %w", p.Name, mt, err)
		}
		return string(b), nil
	case mt == "text/plain":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("parameter %q declares content %q: the value must be a string, got %T", p.Name, mediaKey, value)
		}
		if hasMediaFidelity(bindingSpec) {
			encoded, err := encodeTextString(s, parsed)
			if err != nil {
				return "", fmt.Errorf("parameter %q declares content %q: %w", p.Name, mediaKey, err)
			}
			return string(encoded), nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("parameter %q declares content %q: no parameter carriage is defined for that media type in openbindings.openapi@1", p.Name, mediaKey)
	}
}

// ---------------------------------------------------------------------------
// Style/explode expansions (OAPI-P-02: the OAS tables, incorporated wholesale)
// ---------------------------------------------------------------------------

// serializePathValue expands one path parameter per the OAS style table.
// Value pieces are percent-encoded with the encodeURIComponent byte set
// (cross-SDK URL parity); the style's structural characters (";", "=", ".",
// ",") stay literal.
func serializePathValue(name string, value any, style string, explode bool) (string, error) {
	return serializePathValueForRevision(name, value, style, explode, BindingSpec)
}

func serializePathValueForRevision(name string, value any, style string, explode bool, bindingSpec string) (string, error) {
	esc := encodePathValue
	if hasMediaFidelity(bindingSpec) {
		esc = func(value string) string { return revision3URIEscape(value, false, false) }
	}
	switch style {
	case openapi3.SerializationSimple:
		return expandSimple(value, explode, esc)
	case openapi3.SerializationLabel:
		return expandLabel(value, explode, esc)
	case openapi3.SerializationMatrix:
		return expandMatrix(name, value, explode, esc)
	default:
		return "", fmt.Errorf("style %q is not defined for path parameters", style)
	}
}

// serializeHeaderValue expands one header parameter (simple style only).
// Header values are not percent-encoded: they are not URL components.
func serializeHeaderValue(value any, style string, explode bool) (string, error) {
	if style != openapi3.SerializationSimple {
		return "", fmt.Errorf("style %q is not defined for header parameters", style)
	}
	return expandSimple(value, explode, func(s string) string { return s })
}

// serializeQueryValue expands one query parameter into fully percent-encoded
// name=value units, per the OAS query styles. allowReserved lets RFC 3986
// reserved characters in VALUES pass unescaped.
func serializeQueryValue(name string, value any, style string, explode bool, allowReserved bool) ([]string, error) {
	return serializeQueryValueForRevision(name, value, style, explode, allowReserved, BindingSpec, false)
}

func serializeQueryValueForRevision(name string, value any, style string, explode bool, allowReserved bool, bindingSpec string, formSafe bool) ([]string, error) {
	n := queryEscape(name, false)
	esc := func(s string) string { return queryEscape(s, allowReserved) }
	keyEsc := func(s string) string { return queryEscape(s, false) }
	if hasMediaFidelity(bindingSpec) {
		n = revision3URIEscape(name, false, formSafe)
		esc = func(value string) string { return revision3URIEscape(value, allowReserved, formSafe) }
		keyEsc = func(value string) string { return revision3URIEscape(value, false, formSafe) }
	}
	switch style {
	case openapi3.SerializationForm:
		return expandFormPairs(n, value, explode, esc)
	case openapi3.SerializationSpaceDelimited:
		return expandDelimited(n, value, explode, "%20", esc)
	case openapi3.SerializationPipeDelimited:
		return expandDelimited(n, value, explode, "|", esc)
	case openapi3.SerializationDeepObject:
		obj, ok := asObject(value)
		if !ok {
			return nil, fmt.Errorf("style deepObject is defined for objects only, got %T", value)
		}
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		units := make([]string, 0, len(pairs))
		for _, kv := range pairs {
			units = append(units, n+"["+keyEsc(kv[0])+"]="+esc(kv[1]))
		}
		return units, nil
	default:
		return nil, fmt.Errorf("style %q is not defined for query parameters", style)
	}
}

func revision3URIEscape(value string, allowReserved, formSafe bool) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		unreserved := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", rune(char))
		reserved := strings.ContainsRune(":/?#[]@!$&'()*+,;=", rune(char))
		if formSafe && strings.ContainsRune("&+=#[]", rune(char)) {
			reserved = false
		}
		if unreserved || allowReserved && reserved {
			out.WriteByte(char)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[char>>4])
		out.WriteByte(hex[char&0x0f])
	}
	return out.String()
}

// serializeCookieValue expands one cookie parameter (form style only) into
// raw name=value units, which channel assembly (§9.6, OAPI-P-10) joins into
// the single Cookie header with "; ". Cookie values are not percent-encoded
// (the OAS defines no cookie escaping); exploded array/object expansions use
// the cookie header's own pair separator rather than form's "&", which has
// no meaning inside a Cookie header.
func serializeCookieValue(name string, value any, style string, explode bool) ([]string, error) {
	if style != openapi3.SerializationForm {
		return nil, fmt.Errorf("style %q is not defined for cookie parameters", style)
	}
	return expandFormPairs(name, value, explode, func(s string) string { return s })
}

// expandSimple implements the OAS "simple" rows:
//
//	primitive        → v
//	array (any expl) → a,b,c
//	object false     → k1,v1,k2,v2
//	object true      → k1=v1,k2=v2
func expandSimple(value any, explode bool, esc func(string) string) (string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		return joinEscaped(parts, ",", esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			return joinPairs(pairs, "=", ",", esc), nil
		}
		return joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	return esc(s), nil
}

// expandLabel implements the OAS "label" rows (the "." prefix, "."
// separators when exploded).
func expandLabel(value any, explode bool, esc func(string) string) (string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		sep := ","
		if explode {
			sep = "."
		}
		return "." + joinEscaped(parts, sep, esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			return "." + joinPairs(pairs, "=", ".", esc), nil
		}
		return "." + joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	return "." + esc(s), nil
}

// expandMatrix implements the OAS "matrix" rows (";name=" prefixes; an empty
// primitive renders ";name").
func expandMatrix(name string, value any, explode bool, esc func(string) string) (string, error) {
	n := esc(name)
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		if explode {
			var b strings.Builder
			for _, p := range parts {
				b.WriteString(";" + n + "=" + esc(p))
			}
			return b.String(), nil
		}
		return ";" + n + "=" + joinEscaped(parts, ",", esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			var b strings.Builder
			for _, kv := range pairs {
				b.WriteString(";" + esc(kv[0]) + "=" + esc(kv[1]))
			}
			return b.String(), nil
		}
		return ";" + n + "=" + joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	if s == "" {
		return ";" + n, nil
	}
	return ";" + n + "=" + esc(s), nil
}

// expandFormPairs implements the OAS "form" rows as name=value units:
//
//	primitive        → [name=v]
//	array false      → [name=a,b,c]
//	array true       → [name=a name=b name=c]
//	object false     → [name=k1,v1,k2,v2]
//	object true      → [k1=v1 k2=v2]
func expandFormPairs(name string, value any, explode bool, esc func(string) string) ([]string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return nil, err
		}
		if explode {
			units := make([]string, 0, len(parts))
			for _, p := range parts {
				units = append(units, name+"="+esc(p))
			}
			return units, nil
		}
		return []string{name + "=" + joinEscaped(parts, ",", esc)}, nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		if explode {
			units := make([]string, 0, len(pairs))
			for _, kv := range pairs {
				units = append(units, esc(kv[0])+"="+esc(kv[1]))
			}
			return units, nil
		}
		return []string{name + "=" + joinEscaped(flattenPairs(pairs), ",", esc)}, nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return nil, err
	}
	return []string{name + "=" + esc(s)}, nil
}

// expandDelimited implements spaceDelimited / pipeDelimited (defined by the
// OAS for arrays and objects, explode=false; the delimiter separates the
// escaped pieces). An exploded spaceDelimited/pipeDelimited parameter has no
// OAS-defined expansion of its own — the delimiter is unused when each value
// rides its own name=value pair — so it degrades to the form-style exploded
// expansion, matching common OpenAPI tooling. Primitives are undefined for
// these styles and refuse loudly.
func expandDelimited(name string, value any, explode bool, delim string, esc func(string) string) ([]string, error) {
	if explode {
		if _, ok := asArray(value); !ok {
			if _, isObj := asObject(value); !isObj {
				return nil, fmt.Errorf("spaceDelimited/pipeDelimited styles are not defined for primitives")
			}
		}
		return expandFormPairs(name, value, true, esc)
	}
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return nil, err
		}
		return []string{name + "=" + joinEscaped(parts, delim, esc)}, nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		return []string{name + "=" + joinEscaped(flattenPairs(pairs), delim, esc)}, nil
	}
	return nil, fmt.Errorf("spaceDelimited/pipeDelimited styles are not defined for primitives")
}

// ---------------------------------------------------------------------------
// Value shaping helpers
// ---------------------------------------------------------------------------

// primitiveString renders a JSON primitive in its defined wire form: strings
// verbatim, booleans as true/false, numbers in their canonical JSON
// rendering, null as the empty string. Arrays and objects are not primitives.
func primitiveString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		return t.String(), nil
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		b, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("value of type %T is not a primitive", v)
	}
}

func asArray(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

func asObject(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, s := range t {
			out[k] = s
		}
		return out, true
	}
	return nil, false
}

// arrayStrings renders each array element as a primitive string (nested
// arrays/objects have no OAS-defined expansion inside a parameter value).
func arrayStrings(arr []any) ([]string, error) {
	out := make([]string, 0, len(arr))
	for i, e := range arr {
		s, err := primitiveString(e)
		if err != nil {
			return nil, fmt.Errorf("array element %d: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// objectPairs renders an object's members as ordered [key, value] pairs,
// keys sorted lexicographically for a deterministic expansion (JSON objects
// carry no order).
func objectPairs(obj map[string]any) ([][2]string, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		s, err := primitiveString(obj[k])
		if err != nil {
			return nil, fmt.Errorf("object member %q: %w", k, err)
		}
		pairs = append(pairs, [2]string{k, s})
	}
	return pairs, nil
}

func flattenPairs(pairs [][2]string) []string {
	out := make([]string, 0, len(pairs)*2)
	for _, kv := range pairs {
		out = append(out, kv[0], kv[1])
	}
	return out
}

func joinEscaped(parts []string, sep string, esc func(string) string) string {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = esc(p)
	}
	return strings.Join(escaped, sep)
}

func joinPairs(pairs [][2]string, kvSep, pairSep string, esc func(string) string) string {
	units := make([]string, len(pairs))
	for i, kv := range pairs {
		units[i] = esc(kv[0]) + kvSep + esc(kv[1])
	}
	return strings.Join(units, pairSep)
}

// queryEscape percent-encodes one query-string piece with the
// encodeURIComponent byte set (cross-SDK parity with the path escape); with
// allowReserved, RFC 3986 reserved characters additionally pass through
// unescaped, per the OAS allowReserved rule.
func queryEscape(s string, allowReserved bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		case allowReserved && strings.IndexByte(":/?#[]@$&+,;=", c) >= 0:
			// The full RFC 3986 reserved set is gen-delims + sub-delims;
			// !, ', (, ), and * already pass through above.
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}
