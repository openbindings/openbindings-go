package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// abstractInputRoutes is a synthesis-only correspondence between distinct
// OpenAPI input declarations and protocol-neutral operation property names.
// It is carried to the binding side by a core inputTransform; the operation
// schema and ordinary caller value contain only the abstract field names.
type abstractInputRoutes struct {
	parameters     []abstractParameterRoute
	bodyFields     map[string]string // OpenAPI body property -> abstract field
	wholeBodyField string            // abstract field for a non-object body
	needsTransform bool
}

type abstractParameterRoute struct {
	In    string `json:"in"`
	Name  string `json:"name"`
	Field string `json:"field"`
}

type inputSlot struct {
	kind string
	in   string
	name string
	base string
}

// planAbstractInputRoutes allocates deterministic, protocol-neutral names.
// Original names are preserved wherever they are unique. A duplicate gets
// the first unused numeric suffix, while every artifact-authored base name is
// reserved so a generated suffix never steals another declaration's name.
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
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if plan.synthetic {
			wholeBody = true
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
		slots = append(slots, inputSlot{kind: "wholeBody", base: syntheticBodyProperty})
	}

	reserved := map[string]bool{}
	for _, slot := range slots {
		reserved[slot.base] = true
	}
	used := map[string]bool{}
	assigned := make([]string, len(slots))
	needsTransform := false
	for i, slot := range slots {
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
		assigned[i] = field
		needsTransform = needsTransform || field != slot.base
	}

	routes := abstractInputRoutes{
		bodyFields:     map[string]string{},
		needsTransform: needsTransform,
	}
	for i, slot := range slots {
		switch slot.kind {
		case "parameter":
			routes.parameters = append(routes.parameters, abstractParameterRoute{
				In: slot.in, Name: slot.name, Field: assigned[i],
			})
		case "body":
			routes.bodyFields[slot.name] = assigned[i]
		case "wholeBody":
			routes.wholeBodyField = assigned[i]
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

// flatInputHasAmbiguousParameter reports the only ambiguity that exists
// before request-media candidate election: one supplied flat field naming
// multiple independently declared parameters. Parameter/body collisions are
// candidate-specific and are handled while electing a body plan.
func flatInputHasAmbiguousParameter(params openapi3.Parameters, input map[string]any) bool {
	seen := map[string]bool{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil || pref.Value.Name == "" {
			continue
		}
		name := pref.Value.Name
		if seen[name] {
			if _, supplied := input[name]; supplied {
				return true
			}
		}
		seen[name] = true
	}
	return false
}

// transformExpression produces the source-facing revision-2 routing
// one-tuple. The tuple is structurally disjoint from the object-shaped
// application input. Its binding-private descriptor carries concrete OpenAPI
// identity; the operation-facing value under "value" remains protocol-neutral.
func (r abstractInputRoutes) transformExpression() string {
	return r.transformExpressionFor(BindingSpecV2)
}

func (r abstractInputRoutes) transformExpressionFor(bindingSpec string) string {
	params, _ := json.Marshal(r.parameters)
	body := map[string]any{}
	if len(r.bodyFields) > 0 {
		body["properties"] = r.bodyFields
	}
	if r.wholeBodyField != "" {
		body["whole"] = r.wholeBodyField
	}
	bodyJSON, _ := json.Marshal(body)
	return fmt.Sprintf(`[{"$openbindings":"%s","value":$,"parameters":%s,"body":%s}]`,
		bindingSpec, params, bodyJSON)
}

// routedEnvelope is the binding-private source value defined by revision 2.
// It is normally produced by a core inputTransform. Direct binding-layer
// callers may provide it themselves because they already operate below the
// protocol-independent operation boundary.
type routedEnvelope struct {
	value          map[string]any
	parameters     []abstractParameterRoute
	bodyFields     map[string]string
	wholeBodyField string
}

func parseRoutedEnvelope(input any) (*routedEnvelope, error) {
	return parseRoutedEnvelopeFor(input, BindingSpecV2)
}

func parseRoutedEnvelopeFor(input any, bindingSpec string) (*routedEnvelope, error) {
	tuple, routed := toAnySlice(input)
	if !routed {
		return nil, nil
	}
	if len(tuple) != 1 {
		return nil, fmt.Errorf("revision-2 routed input must be an exact one-item array")
	}
	envelope, ok := toStringAnyMap(tuple[0])
	if !ok {
		return nil, fmt.Errorf("revision-2 routed input array item must be an object")
	}
	marker, marked := envelope["$openbindings"]
	if !marked {
		return nil, fmt.Errorf("revision-2 routed input array item requires $openbindings marker")
	}
	if len(envelope) != 4 || envelope["value"] == nil || envelope["parameters"] == nil || envelope["body"] == nil {
		return nil, fmt.Errorf("revision-2 routed input array item must contain exactly $openbindings, value, parameters, and body")
	}
	if marker != bindingSpec {
		return nil, fmt.Errorf("revision-2 routed input has invalid $openbindings marker %v", marker)
	}
	value, ok := envelope["value"].(map[string]any)
	if !ok {
		// Values decoded through different JSON implementations may use an
		// alias map type; normalize through the shared helper.
		if converted, convertedOK := toStringAnyMap(envelope["value"]); convertedOK {
			value = converted
		} else {
			return nil, fmt.Errorf("revision-2 routed input value must be a JSON object")
		}
	}

	r := &routedEnvelope{value: value, bodyFields: map[string]string{}}
	seenFields := map[string]bool{}
	rawParams, ok := envelope["parameters"].([]any)
	if !ok {
		// An empty JSONata array can arrive as []interface{} (the common
		// case); omission is also accepted as an empty parameter map.
		if envelope["parameters"] != nil {
			return nil, fmt.Errorf("revision-2 routed input parameters must be an array")
		}
	}
	seenParams := map[string]bool{}
	for _, raw := range rawParams {
		entry, ok := toStringAnyMap(raw)
		if !ok {
			return nil, fmt.Errorf("revision-2 routed parameter entry must be an object")
		}
		in, _ := entry["in"].(string)
		name, _ := entry["name"].(string)
		field, _ := entry["field"].(string)
		if in == "" || name == "" || field == "" {
			return nil, fmt.Errorf("revision-2 routed parameter entry requires non-empty in, name, and field")
		}
		identity := in + "\x00" + name
		if seenParams[identity] {
			return nil, fmt.Errorf("revision-2 routed input repeats parameter %q in %q", name, in)
		}
		if seenFields[field] {
			return nil, fmt.Errorf("revision-2 routed input field %q supplies more than one destination", field)
		}
		seenParams[identity] = true
		seenFields[field] = true
		r.parameters = append(r.parameters, abstractParameterRoute{In: in, Name: name, Field: field})
	}

	if rawBody, present := envelope["body"]; present {
		body, ok := toStringAnyMap(rawBody)
		if !ok {
			return nil, fmt.Errorf("revision-2 routed input body descriptor must be an object")
		}
		if rawProps, present := body["properties"]; present {
			props, ok := toStringAnyMap(rawProps)
			if !ok {
				return nil, fmt.Errorf("revision-2 routed body properties must be an object")
			}
			for name, rawField := range props {
				field, ok := rawField.(string)
				if name == "" || !ok || field == "" {
					return nil, fmt.Errorf("revision-2 routed body property mappings require non-empty string names and fields")
				}
				if seenFields[field] {
					return nil, fmt.Errorf("revision-2 routed input field %q supplies more than one destination", field)
				}
				seenFields[field] = true
				r.bodyFields[name] = field
			}
		}
		if rawWhole, present := body["whole"]; present {
			whole, ok := rawWhole.(string)
			if !ok || whole == "" {
				return nil, fmt.Errorf("revision-2 routed whole-body field must be a non-empty string")
			}
			if seenFields[whole] {
				return nil, fmt.Errorf("revision-2 routed input field %q supplies more than one destination", whole)
			}
			seenFields[whole] = true
			r.wholeBodyField = whole
		}
	}
	return r, nil
}

func usesRoutedInput(bindingSpec string) bool {
	return bindingSpec == BindingSpecV2 || bindingSpec == BindingSpecV3
}

// validateEnvelopeRoutes proves that every concrete identity named by a
// routed source value exists in the bound artifact. Body routes are checked
// against the union of admissible candidates because the same synthesized
// transform serves every artifact-declared media alternative; candidate
// election later decides which subset can carry a particular invocation.
func validateEnvelopeRoutes(params openapi3.Parameters, plans []*bodyPlan, envelope *routedEnvelope) error {
	knownParams := map[string]bool{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		knownParams[pref.Value.In+"\x00"+pref.Value.Name] = true
	}
	for _, route := range envelope.parameters {
		if !knownParams[route.In+"\x00"+route.Name] {
			return fmt.Errorf("revision-2 routed parameter %q in %q does not identify an effective OpenAPI declaration", route.Name, route.In)
		}
	}

	knownBodyFields := map[string]bool{}
	wholeBody := false
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if plan.synthetic {
			wholeBody = true
			continue
		}
		for name := range plan.props {
			knownBodyFields[name] = true
		}
	}
	for name := range envelope.bodyFields {
		if !knownBodyFields[name] {
			return fmt.Errorf("revision-2 routed body property %q does not identify a property in any admissible request-body candidate", name)
		}
	}
	if envelope.wholeBodyField != "" && !wholeBody {
		return fmt.Errorf("revision-2 routed whole-body field does not identify any admissible non-object request-body candidate")
	}
	return nil
}

func toStringAnyMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if m, ok := value.(map[string]any); ok {
		return m, true
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

func toAnySlice(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	if items, ok := value.([]any); ok {
		return items, true
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var items []any
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, false
	}
	return items, true
}

func (r *routedEnvelope) parameterField(in, name string) (string, bool) {
	for _, route := range r.parameters {
		if route.In == in && route.Name == name {
			return route.Field, true
		}
	}
	return "", false
}

// routeEnvelope maps the revision-2 source representation to the existing
// wire accumulator, preserving the OpenAPI artifact's serialization rules.
func routeEnvelope(params openapi3.Parameters, envelope *routedEnvelope, pathTemplate string, plan *bodyPlan) (*routedInput, error) {
	return routeEnvelopeFor(params, envelope, pathTemplate, plan, BindingSpecV2)
}

func routeEnvelopeFor(params openapi3.Parameters, envelope *routedEnvelope, pathTemplate string, plan *bodyPlan, bindingSpec string) (*routedInput, error) {
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
		field, mapped := envelope.parameterField(p.In, p.Name)
		if !mapped {
			if p.In == openapi3.ParameterInPath {
				missingPath = append(missingPath, p.Name)
			}
			continue
		}
		value, present := envelope.value[field]
		if !present {
			if p.In == openapi3.ParameterInPath {
				missingPath = append(missingPath, p.Name)
			}
			continue
		}
		consumed[field] = true
		if err := routeParameterFor(r, p, value, bindingSpec); err != nil {
			return nil, err
		}
	}
	if len(missingPath) > 0 {
		sort.Strings(missingPath)
		return nil, fmt.Errorf("%w(s) %s: the URL cannot be built without them", errMissingPathParam, strings.Join(missingPath, ", "))
	}

	if plan != nil && plan.declared {
		if plan.synthetic {
			if envelope.wholeBodyField != "" {
				if value, present := envelope.value[envelope.wholeBodyField]; present {
					r.bodyValue, r.bodySet = value, true
					consumed[envelope.wholeBodyField] = true
				}
			}
		} else {
			bodyNames := make([]string, 0, len(envelope.bodyFields))
			for name := range envelope.bodyFields {
				bodyNames = append(bodyNames, name)
			}
			sort.Strings(bodyNames)
			for _, bodyName := range bodyNames {
				field := envelope.bodyFields[bodyName]
				value, present := envelope.value[field]
				if !present {
					continue
				}
				if !plan.props[bodyName] && !planAllowsObjectPassthrough(plan) {
					continue
				}
				r.bodyFields[bodyName] = value
				consumed[field] = true
			}
		}
	}

	var unmatched []string
	inputNames := make([]string, 0, len(envelope.value))
	for name := range envelope.value {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)
	routedBodyFields := map[string]bool{}
	for _, field := range envelope.bodyFields {
		routedBodyFields[field] = true
	}
	if envelope.wholeBodyField != "" {
		routedBodyFields[envelope.wholeBodyField] = true
	}
	for _, name := range inputNames {
		if consumed[name] {
			continue
		}
		if planAllowsObjectPassthrough(plan) && !routedBodyFields[name] {
			r.bodyFields[name] = envelope.value[name]
			continue
		}
		unmatched = append(unmatched, name)
	}
	if len(unmatched) > 0 {
		return nil, fmt.Errorf("field(s) %s have no destination in the selected OpenAPI request representation", strings.Join(unmatched, ", "))
	}
	return r, nil
}

func planAllowsObjectPassthrough(plan *bodyPlan) bool {
	return plan != nil && plan.declared && !plan.synthetic && plan.family == familyJSON
}

func envelopeWillEmitBody(envelope *routedEnvelope, op *openapi3.Operation) bool {
	if !hasRequestBody(op) {
		return false
	}
	if op.RequestBody.Value.Required {
		return true
	}
	parameterFields := map[string]bool{}
	for _, route := range envelope.parameters {
		parameterFields[route.Field] = true
	}
	for name := range envelope.value {
		if !parameterFields[name] {
			return true
		}
	}
	return false
}
