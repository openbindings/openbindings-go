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

// This file implements effective-parameter identity and the OAS
// style/explode/allowReserved serialization rules. The binding-facing caller
// value is the §7 envelope; synthesis may separately retain a flat,
// protocol-neutral operation contract and map it into that envelope.

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

// duplicateEffectiveParameterIdentity reports an exact name-plus-location
// identity that survives path/operation override and OAS header ignoring.
// Cross-location duplicates are legal and use the caller envelope's qualified
// mode; two declarations at one effective identity exclude the operation.
func duplicateEffectiveParameterIdentity(params openapi3.Parameters) string {
	seen := map[string]bool{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		parameter := pref.Value
		identity := parameter.In + "\x00" + parameter.Name
		if seen[identity] {
			return parameter.In + "/" + escapeJSONPointerSegment(parameter.Name)
		}
		seen[identity] = true
	}
	return ""
}

// requestBodyIgnoredForBindingSpec applies the sibling-specific HTTP method
// disposition before either invocation or synthesis constructs its input.
func requestBodyIgnoredForBindingSpec(bindingSpec, method string) bool {
	method = strings.ToLower(method)
	if bindingSpec == BindingSpecOpenAPI31 {
		return method == "trace"
	}
	if bindingSpec != BindingSpecOpenAPI30 {
		return false
	}
	switch method {
	case "get", "head", "delete", "options", "trace":
		return true
	default:
		return false
	}
}

// checkEffectiveParameterOwnership enforces the declaration-only portions of
// OAPI-P-10. Host and Content-Length are owned by the HTTP processor and
// therefore cannot be caller-routed parameters. The 3.0 sibling also treats a
// raw Cookie header plus structured cookie parameters as a declaration-time
// collision; 3.1 defers that pair to the invocation-time emission check.
func checkEffectiveParameterOwnership(params openapi3.Parameters, bindingSpec string) error {
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
	if bindingSpec != BindingSpecOpenAPI31 && rawCookieHeader && structuredCookieParameter {
		return fmt.Errorf("operation declares both a raw Cookie header parameter and structured cookie parameters (OAPI-P-10: unresolvable)")
	}
	return nil
}

// checkPathTemplateAddressability enforces §9.3 (OAPI-P-05): the target URL is
// the resolved server joined with the operation's path template, so a template
// variable that no declared path parameter can supply leaves no target to
// address and refuses before dispatch — the same ground §9.1 states for the
// neighbouring case of a declared path parameter the caller omitted ("the URL
// cannot be built"). Every accepted OAS edition requires a path template
// variable to have a corresponding `in: path` Parameter Object with
// `required: true`, so this reaches only artifacts that violate that
// requirement. Emitting the unsubstituted template instead would percent-encode
// the braces and put a meaningless segment on a live service.
//
// The predicate is the exact inverse of the substitution routeParameter
// performs (ReplaceAll of "{" + name + "}"): an expression is addressable iff a
// declared path parameter's name equals the enclosed text. It is declaration-
// only — independent of any invocation input — and so is checked before input
// consumption. This package's invocation delegates to the standalone engine
// (invoker.go), which applies the same check; the twin is kept here because
// this file mirrors that engine's input model.
func checkPathTemplateAddressability(pathTemplate string, params openapi3.Parameters) error {
	return checkPathTemplateDeclaration(pathTemplate, params, BindingSpecOpenAPI31)
}

func checkPathTemplateDeclaration(pathTemplate string, params openapi3.Parameters, bindingSpec string) error {
	declared := map[string]bool{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		if pref.Value.In == openapi3.ParameterInPath {
			declared[pref.Value.Name] = true
		}
	}
	var unaddressable, duplicates []string
	seenExpressions := map[string]bool{}
	for _, name := range pathTemplateVariables(pathTemplate) {
		if seenExpressions[name] {
			duplicates = append(duplicates, name)
		}
		seenExpressions[name] = true
		if !declared[name] {
			unaddressable = append(unaddressable, name)
		}
	}
	if len(unaddressable) > 0 {
		sort.Strings(unaddressable)
		return fmt.Errorf("path template variable(s) %s have no declared path parameter: the target URL cannot be built (OAPI-P-05: unresolvable target)", strings.Join(unaddressable, ", "))
	}
	if bindingSpec != BindingSpecOpenAPI31 {
		return nil
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		return fmt.Errorf("path template expression(s) %s occur more than once (OAPI31-P-02: excluded target)", strings.Join(duplicates, ", "))
	}
	var unmatched []string
	for name := range declared {
		if !seenExpressions[name] {
			unmatched = append(unmatched, name)
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		return fmt.Errorf("declared path parameter(s) %s have no path template expression (OAPI31-P-02: excluded target)", strings.Join(unmatched, ", "))
	}
	return nil
}

// equivalentPathTemplateCollision reports another 3.1 Paths key with the
// same static hierarchy after template names are erased. The OAS forbids this
// ambiguity even though both raw keys are independently valid map members.
func equivalentPathTemplateCollision(paths *openapi3.Paths, selected string) string {
	if paths == nil {
		return ""
	}
	want, templated := normalizedPathTemplateHierarchy(selected)
	if !templated {
		return ""
	}
	for candidate := range paths.Map() {
		if candidate == selected {
			continue
		}
		if normalized, hasTemplate := normalizedPathTemplateHierarchy(candidate); hasTemplate && normalized == want {
			return candidate
		}
	}
	return ""
}

func normalizedPathTemplateHierarchy(path string) (string, bool) {
	var out strings.Builder
	templated := false
	for index := 0; index < len(path); {
		if path[index] != '{' {
			out.WriteByte(path[index])
			index++
			continue
		}
		close := strings.IndexByte(path[index+1:], '}')
		if close < 0 {
			out.WriteByte(path[index])
			index++
			continue
		}
		templated = true
		out.WriteString("{}")
		index += close + 2
	}
	return out.String(), templated
}

func formStyleCookieMultiValueProof(p *openapi3.Parameter, is30 bool) bool {
	if p == nil || is30 || p.In != openapi3.ParameterInCookie || len(p.Content) > 0 || p.Schema == nil || p.Schema.Value == nil {
		return false
	}
	method, err := revision3ParameterSerializationMethod(p)
	if err != nil || method.Style != openapi3.SerializationForm || !method.Explode {
		return false
	}
	resolved := resolveDeclaration(p.Schema.Value, false)
	return resolved.declaresOnly("array") || resolved.declaresOnly("object") && len(resolved.propertyNames()) > 0
}

func formStyleCookieMultiValueParamFor(params openapi3.Parameters, is30 bool) string {
	for _, ref := range params {
		if ref != nil && formStyleCookieMultiValueProof(ref.Value, is30) {
			return ref.Value.Name
		}
	}
	return ""
}

// malformedEffectiveParameterFor applies the 3.1 closed Parameter Object
// declaration gate after reference resolution. The raw acceptance floor owns
// entry-document positions; this typed twin covers external references whose
// target bytes are unavailable to that floor.
func malformedEffectiveParameterFor(params openapi3.Parameters, bindingSpec string) string {
	if bindingSpec != BindingSpecOpenAPI31 {
		return ""
	}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		name := parameter.Name
		if name == "" {
			name = "<unnamed>"
		}
		if parameter.Name == "" || parameter.In != openapi3.ParameterInPath && parameter.In != openapi3.ParameterInQuery &&
			parameter.In != openapi3.ParameterInHeader && parameter.In != openapi3.ParameterInCookie {
			return name
		}
		hasSchema := parameter.Schema != nil
		hasContent := parameter.Content != nil
		if hasSchema == hasContent || parameter.In == openapi3.ParameterInPath && !parameter.Required ||
			hasContent && len(parameter.Content) != 1 {
			return name
		}
	}
	return ""
}

// pathTemplateVariables returns the names of the brace-delimited expressions in
// a path template, in order. An unclosed "{" delimits nothing and is an
// ordinary literal; an inner "{" restarts the expression, matching the
// innermost pair the substitution would otherwise have to match.
func pathTemplateVariables(pathTemplate string) []string {
	var names []string
	open := -1
	for index, char := range pathTemplate {
		switch char {
		case '{':
			open = index
		case '}':
			if open >= 0 {
				names = append(names, pathTemplate[open+1:index])
				open = -1
			}
		}
	}
	return names
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

// routeParameterFor serializes one populated parameter onto its wire location
// under the exact binding family token in scope.
func routeParameterFor(r *routedInput, p *openapi3.Parameter, value any, bindingSpec string) error {
	if hasMediaFidelity(bindingSpec) {
		if err := validateRevision3ParameterSerialization(p, bindingSpec == BindingSpecOpenAPI30); err != nil {
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

func validateRevision3ParameterSerialization(p *openapi3.Parameter, is30 bool) error {
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
	resolved := resolveDeclaration(schema, is30)
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
			if method.Explode {
				return fmt.Errorf("query style %q has no explode=true cell", method.Style)
			}
			if resolved.declaresOnly("null", "boolean", "number", "integer", "string") {
				return fmt.Errorf("query style %q is defined only for arrays or objects", method.Style)
			}
		case openapi3.SerializationDeepObject:
			if !method.Explode {
				return fmt.Errorf("query style deepObject has no explode=false cell")
			}
			if resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array") {
				return fmt.Errorf("query style deepObject is defined only for objects")
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

// serializeParamContentFor serializes a content-form parameter's value per
// its declared media type under the exact binding family token in scope: JSON
// family values JSON-serialize; text/plain carries a string value verbatim.
// Any other declared media type has no defined parameter carriage and refuses
// loudly.
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
		return "", fmt.Errorf("parameter %q declares content %q: no parameter carriage is defined for that media type in the registered OpenAPI binding family", p.Name, mediaKey)
	}
}

// ---------------------------------------------------------------------------
// Style/explode expansions (OAPI-P-02: the OAS tables, incorporated wholesale)
// ---------------------------------------------------------------------------

// serializePathValueForRevision expands one path parameter per the OAS style
// table under the exact binding family token in scope. Value pieces are
// percent-encoded with the encodeURIComponent byte set (cross-SDK URL parity);
// the style's structural characters (";", "=", ".", ",") stay literal.
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

// serializeQueryValueForRevision expands one query parameter into fully
// percent-encoded name=value units under the exact binding family token in
// scope, per the OAS query styles. allowReserved lets RFC 3986 reserved
// characters in VALUES pass unescaped.
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

// formURLEncodedEscape percent-encodes one piece of an
// application/x-www-form-urlencoded body for the CONTENT lane — the lane the
// OAS reaches when an Encoding Object declares none of style, explode or
// allowReserved, and which it assigns to RFC 1866 Section 8.2.1 rather than to
// RFC 6570.
//
// Every accepted edition reaches RFC 1866: "the contents in the requestBody
// MUST be stringified per [RFC1866] when passed to the server" (3.0.0-3.0.3,
// 3.1.0) or "the request body MUST be encoded per [RFC1866]" (3.0.4, 3.1.1,
// 3.1.2). Only the latter three carry Appendix E, whose
// normatively-cited-standards table pairs "content-based serialization" with
// "[RFC1866] Section 8.2.1" and percent-encoding "[RFC1738]", and pairs
// "style-based serialization" with "[RFC6570]", noting that it "does not use +
// for form-urlencoded" (E.3 in 3.0.4 / 3.1.1, E.4 in 3.1.2).
//
// RFC 1866 Section 8.2.1 names the space: "The form field names and values are
// escaped: space characters are replaced by `+', and then reserved characters
// are escaped as per [URL]". The rest of the set is delegated, not stated here:
// the following gloss ("that is, non-alphanumeric characters are replaced by
// `%HH'") is stricter than the [URL] rule it presents itself as restating, and
// no accepted OAS edition asks for the stricter form. [URL] is RFC 1738, pinned
// at corpus-lab/authorities/texts/openapi/url/rfc1738.txt. Its Section 2.2
// settles the set below on every edition:
//
//   - "only alphanumerics, the special characters `$-_.+!*'(),`, and reserved
//     characters used for their reserved purposes may be used unencoded" — so
//     leaving `*`, `-`, `.` and `_` literal is permitted;
//   - "characters that are not required to be encoded (including
//     alphanumerics) may be encoded ... as long as they are not being used for
//     a reserved purpose" — so encoding more is permitted too, which is why
//     `+`, reserved by this media type for the space, is escaped in data;
//   - `~` is named among the unsafe characters, and "All unsafe characters must
//     always be encoded within a URL" — so escaping the tilde is required, not
//     a preference.
//
// The set below is the WHATWG form-urlencoded serializer set, which lies inside
// that permission on every edition. On 3.0.4, 3.1.1 and 3.1.2 it is also named
// outright: Appendix E.3.2 (E.4.2 in 3.1.2) gives a SHOULD for WHATWG's
// form-urlencoded rules, and 3.1.2 Section 4.8.12.4 names the tilde.
//
// Which member of that permitted set to pick is the IMPLEMENTATIONS'
// convention, not the binding specification's: the registered OpenAPI binding family states
// the permitted set and does not narrow it. The pick is pinned by the shared
// twin case table (testdata/urlencoded-escaper-cases.json), executed by both Go
// engines and by openapi-client/typescript.
//
// The STYLE lane keeps revision3URIEscape and its RFC 6570 %20; the two lanes
// disagree about the space character because the OAS assigns them different
// percent-encoding specifications.
func formURLEncodedEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}
