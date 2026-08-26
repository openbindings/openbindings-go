package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"golang.org/x/net/http/httpguts"

	"github.com/openbindings/openbindings-go/invoke"
)

// ParameterConversion is §8.1's deterministic consumer conversion from one
// JSON boolean or number to a string. The converter owns the spelling; the
// binding supplies no canonicalization fallback. It must be safe for
// concurrent invocation by a Runtime.
type ParameterConversion func(value any) (string, error)

func effectiveParameterAt(parameters openapi3.Parameters, in, name string) *openapi3.Parameter {
	for _, ref := range parameters {
		if ref != nil && ref.Value != nil && ref.Value.In == in && ref.Value.Name == name {
			return ref.Value
		}
	}
	return nil
}

// prepareSchemaParameterValue applies the value-dependent half of §8 before
// the standalone HTTP carrier sees a routed value. It returns whether a
// cookie-location parameter would emit at least one structured cookie unit.
func prepareSchemaParameterValue(parameter *openapi3.Parameter, value any, bindingSpec string, conversion ParameterConversion) (any, bool, error) {
	if parameter == nil {
		return nil, false, fmt.Errorf("has no effective declaration")
	}
	if len(parameter.Content) > 0 {
		serialized, err := serializeParamContentFor(parameter, value, bindingSpec)
		if err != nil {
			return nil, false, err
		}
		if parameter.In == openapi3.ParameterInHeader && !httpguts.ValidHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header contains an invalid HTTP field byte")
		}
		return value, parameter.In == openapi3.ParameterInCookie, nil
	}

	method, err := revision3ParameterSerializationMethod(parameter)
	if err != nil {
		return nil, false, err
	}
	prepared, err := prepareStyleValue(parameter.Name, value, method.Style, bindingSpec, conversion)
	if err != nil {
		return nil, false, err
	}
	switch parameter.In {
	case openapi3.ParameterInHeader:
		serialized, err := serializeHeaderValue(prepared, method.Style, method.Explode)
		if err != nil {
			return nil, false, err
		}
		if !httpguts.ValidHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header contains an invalid HTTP field byte")
		}
	case openapi3.ParameterInCookie:
		units, err := serializeCookieValue(parameter.Name, prepared, method.Style, method.Explode)
		if err != nil {
			return nil, false, err
		}
		if bindingSpec == BindingSpecOpenAPI31 && len(units) > 1 {
			return nil, false, fmt.Errorf("supplied value would produce multiple cookie pairs")
		}
		return prepared, len(units) > 0, nil
	}
	return prepared, false, nil
}

func prepareEncodingStylePropertyValue(plan *bodyPlan, name string, value any, bindingSpec string, conversion ParameterConversion) (any, error) {
	if plan == nil || plan.media == nil {
		return value, nil
	}
	encoding := plan.media.Encoding[name]
	if !encodingUsesSerialization(encoding) {
		return value, nil
	}
	method := revision3EncodingSerializationMethod(encoding)
	prepared, err := prepareStyleValue(name, value, method.Style, bindingSpec, conversion)
	if err != nil {
		return nil, fmt.Errorf("body property %q: %w", name, err)
	}
	return prepared, nil
}

func prepareStyleValue(name string, value any, style, bindingSpec string, conversion ParameterConversion) (any, error) {
	if bindingSpec == BindingSpecOpenAPI31 && value == nil {
		switch style {
		case openapi3.SerializationMatrix, openapi3.SerializationLabel, openapi3.SerializationSimple, openapi3.SerializationForm:
			return nil, nil
		default:
			return nil, fmt.Errorf("JSON null has n/a in style %q's undefined cell", style)
		}
	}
	converted := value
	if bindingSpec == BindingSpecOpenAPI31 {
		var err error
		converted, err = convertParameterScalars(value, conversion, false)
		if err != nil {
			return nil, err
		}
	}
	if delimiter := nonRFCStyleDelimiter(style); delimiter != "" {
		if containsAnyDelimiter(name, delimiter) || styleValueContainsDelimiter(converted, delimiter) {
			return nil, fmt.Errorf("value or member name contains style %q's structural delimiter", style)
		}
	}
	switch style {
	case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
		if _, array := asArray(converted); !array {
			if _, object := asObject(converted); !object {
				return nil, fmt.Errorf("style %q is defined only for arrays or objects", style)
			}
		}
	case openapi3.SerializationDeepObject:
		if _, object := asObject(converted); !object {
			return nil, fmt.Errorf("style deepObject is defined only for objects")
		}
	}
	return converted, nil
}

func convertParameterScalars(value any, conversion ParameterConversion, member bool) (any, error) {
	if value == nil {
		if member {
			return nil, fmt.Errorf("null array/object member has no RFC 6570 representation")
		}
		return nil, nil
	}
	if array, ok := asArray(value); ok {
		result := make([]any, len(array))
		for index, item := range array {
			converted, err := convertParameterScalars(item, conversion, true)
			if err != nil {
				return nil, fmt.Errorf("array member %d: %w", index, err)
			}
			result[index] = converted
		}
		return result, nil
	}
	if object, ok := asObject(value); ok {
		result := make(map[string]any, len(object))
		for name, item := range object {
			converted, err := convertParameterScalars(item, conversion, true)
			if err != nil {
				return nil, fmt.Errorf("object member %q: %w", name, err)
			}
			result[name] = converted
		}
		return result, nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	if !jsonBooleanOrNumber(value) {
		return nil, fmt.Errorf("value of type %T is outside the JSON scalar conversion domain", value)
	}
	if conversion == nil {
		return nil, fmt.Errorf("JSON boolean or number requires configuration.parameterConversion")
	}
	text, err := conversion(value)
	if err != nil {
		return nil, fmt.Errorf("configuration.parameterConversion: %w", err)
	}
	return text, nil
}

func jsonBooleanOrNumber(value any) bool {
	switch value.(type) {
	case bool, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func nonRFCStyleDelimiter(style string) string {
	switch style {
	case openapi3.SerializationSpaceDelimited:
		return " "
	case openapi3.SerializationPipeDelimited:
		return "|"
	case openapi3.SerializationDeepObject:
		return "[]=&"
	default:
		return ""
	}
}

func containsAnyDelimiter(value, delimiters string) bool {
	return strings.ContainsAny(value, delimiters)
}

func styleValueContainsDelimiter(value any, delimiters string) bool {
	if array, ok := asArray(value); ok {
		for _, item := range array {
			if text, ok := item.(string); ok && containsAnyDelimiter(text, delimiters) {
				return true
			}
		}
	}
	if object, ok := asObject(value); ok {
		for name, item := range object {
			if containsAnyDelimiter(name, delimiters) {
				return true
			}
			if text, ok := item.(string); ok && containsAnyDelimiter(text, delimiters) {
				return true
			}
		}
	}
	return false
}

func selectedCookieCredentialWouldEmit(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any) bool {
	if doc == nil || op == nil {
		return false
	}
	for _, plan := range securityPlans(doc, op, "") {
		if len(plan.context.Requirements) == 0 {
			return false
		}
		if !invoke.ContextSatisfies(bindCtx, &invoke.ContextRequiredDetails{Alternatives: []invoke.ContextAlternative{plan.context}}) {
			continue
		}
		for _, placement := range credentialValues(plan, bindCtx) {
			if placement.channel == "cookie" && placement.value != "" {
				return true
			}
		}
		return false
	}
	return false
}

func cloneEffectiveParameters(parameters openapi3.Parameters) openapi3.Parameters {
	result := make(openapi3.Parameters, 0, len(parameters))
	for _, ref := range parameters {
		if ref == nil || ref.Value == nil {
			continue
		}
		copy := *ref.Value
		result = append(result, &openapi3.ParameterRef{Ref: ref.Ref, Value: &copy})
	}
	return result
}

// prepareEngineParameterView removes adapter gaps from the private document
// view only. Public caller keys and synthesis retain the authored declaration.
func prepareEngineParameterView(parameters openapi3.Parameters, routes *abstractInputRoutes, bindingSpec string) string {
	usedHeaders := map[string]bool{}
	for _, ref := range parameters {
		if ref != nil && ref.Value != nil && ref.Value.In == openapi3.ParameterInHeader {
			usedHeaders[http.CanonicalHeaderKey(ref.Value.Name)] = true
		}
	}
	sentinel := "X-Openbindings-Adapter-Raw-Cookie"
	for suffix := 2; usedHeaders[http.CanonicalHeaderKey(sentinel)]; suffix++ {
		sentinel = fmt.Sprintf("X-Openbindings-Adapter-Raw-Cookie-%d", suffix)
	}
	foundRaw := false
	for _, ref := range parameters {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		if bindingSpec == BindingSpecOpenAPI31 && parameter.In == openapi3.ParameterInHeader && strings.EqualFold(parameter.Name, "Cookie") {
			parameter.Name = sentinel
			foundRaw = true
		}
		if len(parameter.Content) > 0 {
			continue
		}
		method, err := revision3ParameterSerializationMethod(parameter)
		if err != nil {
			continue
		}
		var engineType string
		switch method.Style {
		case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
			engineType = "array"
		case openapi3.SerializationDeepObject:
			engineType = "object"
		}
		if engineType != "" {
			copy := openapi3.Schema{}
			if parameter.Schema != nil && parameter.Schema.Value != nil {
				copy = *parameter.Schema.Value
			}
			copy.Type = &openapi3.Types{engineType}
			parameter.Schema = &openapi3.SchemaRef{Value: &copy}
		}
	}
	if foundRaw && routes != nil {
		for index := range routes.parameters {
			if routes.parameters[index].In == openapi3.ParameterInHeader && strings.EqualFold(routes.parameters[index].Name, "Cookie") {
				routes.parameters[index].EngineName = sentinel
			}
		}
		return sentinel
	}
	return ""
}

// prepareEngineEncodingView gives the standalone carrier the declaration
// shape its older admission check expects. The adapter has already applied
// the binding's resolved-declaration proof; serialization itself remains
// value-driven and therefore preserves object values for the newly admitted
// spaceDelimited/pipeDelimited cells.
func prepareEngineEncodingView(plans []*bodyPlan) {
	for _, plan := range plans {
		if plan == nil || plan.media == nil {
			continue
		}
		root := mediaSchema(plan.media)
		for name, encoding := range plan.media.Encoding {
			if !encodingUsesSerialization(encoding) {
				continue
			}
			method := revision3EncodingSerializationMethod(encoding)
			var engineType string
			switch method.Style {
			case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
				engineType = "array"
			case openapi3.SerializationDeepObject:
				engineType = "object"
			}
			if engineType == "" {
				continue
			}
			property := resolvedMultipartProperty(root, name, map[*openapi3.Schema]bool{})
			if property != nil {
				property.Type = &openapi3.Types{engineType}
			}
		}
	}
}

type rawCookieBridgeContextKey struct{}
type completedURLValidationContextKey struct{}

type rawCookieBridgeTransport struct{ base http.RoundTripper }

func (t rawCookieBridgeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if bindingSpec, _ := request.Context().Value(completedURLValidationContextKey{}).(string); bindingSpec == BindingSpecOpenAPI31 {
		if err := validateCompletedRequestURL(request.URL); err != nil {
			return nil, err
		}
	}
	sentinel, _ := request.Context().Value(rawCookieBridgeContextKey{}).(string)
	if sentinel == "" || len(request.Header.Values(sentinel)) == 0 {
		return base.RoundTrip(request)
	}
	if len(request.Header.Values("Cookie")) > 0 {
		return nil, fmt.Errorf("raw Cookie adapter bridge reached dispatch with a structured Cookie field")
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	values := copy.Header.Values(sentinel)
	copy.Header.Del(sentinel)
	for _, value := range values {
		copy.Header.Add("Cookie", value)
	}
	return base.RoundTrip(copy)
}

// validateCompletedRequestURL performs §8.2's last pre-dispatch check after
// path and query serialization. Parsing the completed spelling catches bad
// escapes in components such as RawQuery; decoding each percent-bearing
// component makes the RFC 3986 requirement explicit without replacing the
// encoded delimiters that must remain on the wire.
func validateCompletedRequestURL(completed *url.URL) error {
	if completed == nil {
		return fmt.Errorf("completed OpenAPI 3.1 URL is absent")
	}
	parsed, err := url.Parse(completed.String())
	if err != nil {
		return fmt.Errorf("completed OpenAPI 3.1 URL does not parse under RFC 3986: %w", err)
	}
	for name, component := range map[string]string{
		"opaque":   parsed.Opaque,
		"path":     parsed.EscapedPath(),
		"query":    parsed.RawQuery,
		"fragment": parsed.EscapedFragment(),
	} {
		if _, err := url.PathUnescape(component); err != nil {
			return fmt.Errorf("completed OpenAPI 3.1 URL %s does not percent-decode under RFC 3986: %w", name, err)
		}
	}
	return nil
}
