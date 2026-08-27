package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	openapiclient "github.com/openbindings/openapi-client/go"
	"github.com/openbindings/openbindings-go/invoke"
)

// abstractInputRoutes is the synthesis-only correspondence between the flat,
// protocol-neutral operation contract and the binding-boundary caller
// envelope. Concrete HTTP locations appear only in the emitted transform.
type abstractInputRoutes struct {
	parameters     []abstractParameterRoute
	bodyFields     map[string]string // OpenAPI body property -> abstract field
	wholeBodyField string            // abstract field for a whole application body
	openBody       bool              // an object candidate admits undeclared properties
	bodyRequired   bool
}

type abstractParameterRoute struct {
	In         string
	Name       string
	EngineName string
	Field      string
}

type inputSlot struct {
	kind string
	in   string
	name string
	base string
}

// planAbstractInputRoutes allocates deterministic, protocol-neutral flat field
// names. Artifact-authored names win; collisions receive the first unused
// numeric suffix without stealing another authored name.
func planAbstractInputRoutes(params openapi3.Parameters, plans []*bodyPlan) abstractInputRoutes {
	var slots []inputSlot
	for _, pref := range params {
		if pref == nil || pref.Value == nil || pref.Value.Name == "" {
			continue
		}
		p := pref.Value
		slots = append(slots, inputSlot{kind: "parameter", in: p.In, name: p.Name, base: p.Name})
	}

	bodyNames := map[string]bool{}
	wholeBody := false
	protocolNeutralWholeBody := false
	openBody := false
	bodyRequired := false
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		bodyRequired = bodyRequired || plan.required
		openBody = openBody || planAllowsObjectPassthrough(plan)
		if plan.synthetic || plan.wholeObject {
			wholeBody = true
			protocolNeutralWholeBody = protocolNeutralWholeBody || plan.wholeObject
			continue
		}
		for name := range plan.props {
			bodyNames[name] = true
		}
	}
	sortedBodyNames := make([]string, 0, len(bodyNames))
	for name := range bodyNames {
		sortedBodyNames = append(sortedBodyNames, name)
	}
	sort.Strings(sortedBodyNames)
	for _, name := range sortedBodyNames {
		slots = append(slots, inputSlot{kind: "body", name: name, base: name})
	}
	if wholeBody {
		base := syntheticBodyProperty
		if protocolNeutralWholeBody {
			base = "payload"
		}
		slots = append(slots, inputSlot{kind: "wholeBody", base: base})
	}

	reserved := map[string]bool{}
	for _, slot := range slots {
		reserved[slot.base] = true
	}
	used := map[string]bool{}
	assigned := make([]string, len(slots))
	for index, slot := range slots {
		field := slot.base
		if used[field] {
			for suffix := 2; ; suffix++ {
				candidate := fmt.Sprintf("%s_%d", slot.base, suffix)
				if !used[candidate] && !reserved[candidate] {
					field = candidate
					break
				}
			}
		}
		used[field] = true
		assigned[index] = field
	}

	routes := abstractInputRoutes{
		bodyFields:   map[string]string{},
		openBody:     openBody,
		bodyRequired: bodyRequired,
	}
	for index, slot := range slots {
		switch slot.kind {
		case "parameter":
			routes.parameters = append(routes.parameters, abstractParameterRoute{
				In: slot.in, Name: slot.name, EngineName: slot.name, Field: assigned[index],
			})
		case "body":
			routes.bodyFields[slot.name] = assigned[index]
		case "wholeBody":
			routes.wholeBodyField = assigned[index]
		}
	}
	return routes
}

func (r abstractInputRoutes) parameterField(in, name string) string {
	for _, route := range r.parameters {
		if route.In == in && route.Name == name {
			return route.Field
		}
	}
	return name
}

func (r abstractInputRoutes) bodyField(name string) string {
	if field := r.bodyFields[name]; field != "" {
		return field
	}
	return name
}

func (r abstractInputRoutes) hasInput() bool {
	return len(r.parameters) > 0 || len(r.bodyFields) > 0 || r.wholeBodyField != "" || r.openBody || r.bodyRequired
}

func usesRoutedInput(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func qualifiedParameterMode(params openapi3.Parameters) bool {
	locations := map[string]string{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		parameter := pref.Value
		if previous, found := locations[parameter.Name]; found && previous != parameter.In {
			return true
		}
		locations[parameter.Name] = parameter.In
	}
	return false
}

func callerParameterKey(in, name string, qualified bool) string {
	if !qualified {
		return name
	}
	return in + "/" + escapeJSONPointerSegment(name)
}

func quotedJSONata(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jsonataLookup(field string) string {
	return "$lookup($," + quotedJSONata(field) + ")"
}

func jsonataObject(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, quotedJSONata(key)+":"+jsonataLookup(fields[key]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

const jsonataUndefined = `$lookup({},"__openbindings_absent")`

// transformExpression emits ordinary Core JSONata that maps the synthesized
// flat operation value into §7's public {parameters?, body?} envelope. No
// adapter marker, HTTP route descriptor, or engine-private tuple enters the
// OBI document.
func (r abstractInputRoutes) transformExpression(params openapi3.Parameters) string {
	qualified := qualifiedParameterMode(params)
	parameterFields := map[string]string{}
	for _, route := range r.parameters {
		parameterFields[callerParameterKey(route.In, route.Name, qualified)] = route.Field
	}
	parametersExpression := jsonataObject(parameterFields)

	bodyFields := map[string]string{}
	for name, field := range r.bodyFields {
		bodyFields[name] = field
	}
	bodyExpression := jsonataObject(bodyFields)
	bodyPresence := make([]string, 0, len(bodyFields)+1)
	excluded := map[string]bool{}
	for _, route := range r.parameters {
		excluded[route.Field] = true
	}
	for _, field := range r.bodyFields {
		excluded[field] = true
		bodyPresence = append(bodyPresence, "$exists("+jsonataLookup(field)+")")
	}
	if r.wholeBodyField != "" {
		excluded[r.wholeBodyField] = true
	}

	if r.openBody {
		keys := make([]string, 0, len(excluded))
		for key := range excluded {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		condition := "true"
		if len(keys) > 0 {
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, "$key != "+quotedJSONata(key))
			}
			condition = strings.Join(parts, " and ")
		}
		passthrough := "$sift($,function($value,$key){" + condition + "})"
		if len(bodyFields) > 0 {
			bodyExpression = "$merge([" + passthrough + "," + bodyExpression + "])"
		} else {
			bodyExpression = passthrough
		}
		bodyPresence = append(bodyPresence, "$count($keys("+passthrough+")) > 0")
	}
	if r.bodyRequired && r.wholeBodyField == "" {
		bodyPresence = append(bodyPresence, "true")
	}

	parameterValue := "$count($keys($parameters)) > 0 ? $parameters : " + jsonataUndefined
	bodyValue := jsonataUndefined
	if len(bodyFields) > 0 || r.openBody || r.bodyRequired {
		condition := "false"
		if len(bodyPresence) > 0 {
			condition = strings.Join(bodyPresence, " or ")
		}
		bodyValue = "(" + condition + ") ? $bodyObject : " + jsonataUndefined
	}
	if r.wholeBodyField != "" {
		whole := jsonataLookup(r.wholeBodyField)
		bodyValue = "$exists(" + whole + ") ? " + whole + " : (" + bodyValue + ")"
	}

	return "($parameters := " + parametersExpression + "; $bodyObject := " + bodyExpression +
		`; {"parameters":` + parameterValue + `,"body":` + bodyValue + "})"
}

type callerEnvelope struct {
	parameters  map[string]any
	body        any
	bodyPresent bool
}

func parseCallerEnvelope(input any) (*callerEnvelope, error) {
	value, ok := toStringAnyMap(input)
	if !ok {
		return nil, fmt.Errorf("OpenAPI input value must be the caller envelope object")
	}
	for key := range value {
		if key != "parameters" && key != "body" {
			return nil, fmt.Errorf("caller envelope contains unknown top-level key %q", key)
		}
	}
	envelope := &callerEnvelope{}
	if raw, present := value["parameters"]; present {
		parameters, ok := toStringAnyMap(raw)
		if !ok {
			return nil, fmt.Errorf("caller envelope parameters member must be an object")
		}
		envelope.parameters = parameters
	}
	envelope.body, envelope.bodyPresent = value["body"]
	return envelope, nil
}

// engineInputForCallerEnvelope validates the public envelope and lowers it at
// the adapter boundary into the standalone engine's private execution value.
// This value is never emitted in an OBI document or accepted from a caller.
func engineInputForCallerEnvelope(input any, params openapi3.Parameters, plans []*bodyPlan, routes abstractInputRoutes, profile openapiclient.Profile) (any, error) {
	return engineInputForCallerEnvelopeWithSemantics(input, params, plans, routes, profile, BindingSpecOpenAPI31, nil, nil, nil, nil)
}

func engineInputForCallerEnvelopeWithSemantics(input any, params openapi3.Parameters, plans []*bodyPlan, routes abstractInputRoutes, profile openapiclient.Profile, bindingSpec string, conversion ParameterConversion, doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any) (any, error) {
	envelope, err := parseCallerEnvelope(input)
	if err != nil {
		return nil, err
	}
	qualified := qualifiedParameterMode(params)
	byCallerKey := map[string]abstractParameterRoute{}
	for _, route := range routes.parameters {
		byCallerKey[callerParameterKey(route.In, route.Name, qualified)] = route
	}
	value := map[string]any{}
	rawCookieEmits := false
	structuredCookieEmits := false
	for key, member := range envelope.parameters {
		route, found := byCallerKey[key]
		if !found {
			return nil, fmt.Errorf("caller envelope contains unknown parameter key %q", key)
		}
		parameter := effectiveParameterAt(params, route.In, route.Name)
		prepared, emits, err := prepareSchemaParameterValue(parameter, member, bindingSpec, conversion)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", key, err)
		}
		value[route.Field] = prepared
		if route.In == openapi3.ParameterInHeader && strings.EqualFold(route.Name, "Cookie") {
			rawCookieEmits = true
		}
		if route.In == openapi3.ParameterInCookie && emits {
			structuredCookieEmits = true
		}
	}
	if (bindingSpec == BindingSpecOpenAPI30 || bindingSpec == BindingSpecOpenAPI31) && rawCookieEmits {
		if structuredCookieEmits || len(invoke.ContextCookies(bindCtx)) > 0 || selectedCookieCredentialWouldEmit(doc, op, bindCtx) {
			return nil, fmt.Errorf("supplied raw Cookie parameter collides with structured cookie emission")
		}
	}

	bodyDescriptor := map[string]any{}
	if len(routes.bodyFields) > 0 {
		properties := map[string]any{}
		for name, field := range routes.bodyFields {
			properties[name] = field
		}
		bodyDescriptor["properties"] = properties
	}
	if routes.wholeBodyField != "" {
		bodyDescriptor["whole"] = routes.wholeBodyField
	}
	if envelope.bodyPresent {
		bodyDescriptor["present"] = true
		plan := preferredBodyPlan(plans)
		switch {
		case plan == nil:
			return nil, fmt.Errorf("caller envelope supplies body but the operation declares no supported request body")
		case plan.family == familyText && envelope.body == nil:
			return nil, fmt.Errorf("character-data request lane has no null lexical form")
		case plan.synthetic || plan.wholeObject:
			if routes.wholeBodyField == "" {
				return nil, fmt.Errorf("selected request representation has no whole-body route")
			}
			value[routes.wholeBodyField] = envelope.body
		default:
			body, ok := toStringAnyMap(envelope.body)
			if !ok {
				return nil, fmt.Errorf("selected request representation requires an object body")
			}
			for name, member := range body {
				var err error
				member, err = prepareEncodingStylePropertyValue(plan, name, member, bindingSpec, conversion)
				if err != nil {
					return nil, err
				}
				if contentFormNullIsElided(plan, name, member, bindingSpec) {
					continue
				}
				if bindingSpec == BindingSpecOpenAPI31 && plan.rawProperties[name] {
					member, err = decodeRawPropertyForEngine(name, member)
					if err != nil {
						return nil, err
					}
				}
				field := routes.bodyField(name)
				value[field] = member
			}
		}
	}

	parameterDescriptor := make([]any, 0, len(routes.parameters))
	for _, route := range routes.parameters {
		engineName := route.EngineName
		if engineName == "" {
			engineName = route.Name
		}
		parameterDescriptor = append(parameterDescriptor, map[string]any{
			"in": route.In, "name": engineName, "field": route.Field,
		})
	}
	return []any{map[string]any{
		profile.InputRouteKey: profile.InputRouteMarker,
		"value":               value,
		"parameters":          parameterDescriptor,
		"body":                bodyDescriptor,
	}}, nil
}

func decodeRawPropertyForEngine(name string, value any) (any, error) {
	decode := func(member any) (string, error) {
		text, ok := member.(string)
		if !ok {
			return "", fmt.Errorf("raw multipart property %q must be a canonical Base64 string, got %T", name, member)
		}
		decoded, err := canonicalBase64BoundaryBytes(name, text)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	if members, ok := asArray(value); ok {
		result := make([]any, len(members))
		for index, member := range members {
			decoded, err := decode(member)
			if err != nil {
				return nil, err
			}
			result[index] = decoded
		}
		return result, nil
	}
	return decode(value)
}

func preferredBodyPlan(plans []*bodyPlan) *bodyPlan {
	for _, plan := range plans {
		if plan != nil {
			return plan
		}
	}
	return nil
}

func toStringAnyMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if object, ok := value.(map[string]any); ok {
		return object, true
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, false
	}
	return object, true
}

func planAllowsObjectPassthrough(plan *bodyPlan) bool {
	return plan != nil && plan.declared && !plan.synthetic && !plan.wholeObject && plan.family == familyJSON
}
