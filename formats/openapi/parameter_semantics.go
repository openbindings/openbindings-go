package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openapiclient "github.com/openbindings/openapi-client/go"
)

// ParameterConversion is openbindings.openapi-3.0@1 §8.1 /
// openbindings.openapi-3.1@1 §8.1's deterministic consumer conversion from
// one JSON boolean or number to a string. The converter owns the spelling;
// the binding supplies no canonicalization fallback. It must be safe for
// concurrent invocation by a Runtime.
type ParameterConversion = openapiclient.ParameterConverter

func prepareEncodingStylePropertyValue(plan *bodyPlan, name string, value any, bindingSpec string, conversion ParameterConversion) (any, error) {
	if plan == nil || plan.media == nil {
		return value, nil
	}
	encoding := plan.media.Encoding[name]
	if !encodingUsesSerializationForPlan(plan, encoding) {
		return prepareContentFormPropertyValue(plan, name, value, conversion)
	}
	method := revision3EncodingSerializationMethod(encoding)
	prepared, err := prepareStyleValue(name, value, method.Style, bindingSpec, conversion)
	if err != nil {
		return nil, fmt.Errorf("body property %q: %w", name, err)
	}
	return prepared, nil
}

func contentFormNullIsElided(plan *bodyPlan, name string, value any, bindingSpec string) bool {
	if value != nil || plan == nil || plan.media == nil || bindingSpec != BindingSpecOpenAPI30 {
		return false
	}
	if encodingUsesSerializationForPlan(plan, plan.media.Encoding[name]) {
		return false
	}
	root := resolveDeclaration(mediaSchema(plan.media), true)
	return !root.requiresProperty(name) && root.property(name).admitsNull()
}

func prepareStyleValue(name string, value any, style, bindingSpec string, conversion ParameterConversion) (any, error) {
	if (bindingSpec == BindingSpecOpenAPI30 || bindingSpec == BindingSpecOpenAPI31) && value == nil {
		switch style {
		case openapi3.SerializationMatrix, openapi3.SerializationLabel, openapi3.SerializationSimple, openapi3.SerializationForm:
			return nil, nil
		default:
			return nil, fmt.Errorf("JSON null has n/a in style %q's undefined cell", style)
		}
	}
	converted := value
	if bindingSpec == BindingSpecOpenAPI30 || bindingSpec == BindingSpecOpenAPI31 {
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

// prepareContentFormPropertyValue applies §8.1's scalar conversion to the
// content-based form and multipart lanes. Unlike an RFC 6570-style property,
// an object rides as JSON and its nested members are not stringified. Arrays
// on the multipart path become repeated parts, so conversion follows the
// resolved items declaration element by element.
func prepareContentFormPropertyValue(plan *bodyPlan, name string, value any, conversion ParameterConversion) (any, error) {
	if plan == nil || plan.media == nil {
		return value, nil
	}
	// M3 closes the 3.0 per-line conversion row. The 3.1 corpus retains its
	// separately adjudicated non-string multipart behavior in this node.
	if plan.bindingSpec != BindingSpecOpenAPI30 {
		return value, nil
	}
	root := resolveDeclaration(mediaSchema(plan.media), plan.oas30)
	property := root.property(name)
	converted, err := convertContentFormScalars(property, value, conversion)
	if err != nil {
		return nil, fmt.Errorf("body property %q: %w", name, err)
	}
	return converted, nil
}

func convertContentFormScalars(declaration resolvedDeclaration, value any, conversion ParameterConversion) (any, error) {
	if value == nil || declaration.ambiguous || declaration.typeless() {
		return value, nil
	}
	if declaration.declaresOnly("array", "null") {
		array, ok := asArray(value)
		if !ok {
			return value, nil
		}
		items := declaration.items()
		result := make([]any, len(array))
		for index, member := range array {
			converted, err := convertContentFormScalars(items, member, conversion)
			if err != nil {
				return nil, fmt.Errorf("array member %d: %w", index, err)
			}
			result[index] = converted
		}
		return result, nil
	}
	if declaration.declaresOnly("boolean", "number", "integer", "null") && jsonBooleanOrNumber(value) {
		if conversion == nil {
			return nil, fmt.Errorf("JSON boolean or number requires configuration.parameterConversion")
		}
		text, err := conversion(value)
		if err != nil {
			return nil, fmt.Errorf("configuration.parameterConversion: %w", err)
		}
		return text, nil
	}
	return value, nil
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

type completedURLValidationContextKey struct{}
type mediaGovernanceContextKey struct{}
type serverAssemblyContextKey struct{}
type credentialWireContextKey struct{}

type serverAssemblyPolicy struct {
	resolvedBase string
	engineBase   string
}

type governedTransport struct {
	base            http.RoundTripper
	requestCodings  map[string]ContentEncoder
	responseCodings map[string]ContentDecoder
}

func (t governedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	if policy, _ := request.Context().Value(serverAssemblyContextKey{}).(*serverAssemblyPolicy); policy != nil {
		if err := applyVerbatimServerAssembly(copy, policy); err != nil {
			return nil, err
		}
	}
	if policy, _ := request.Context().Value(credentialWireContextKey{}).(*credentialWirePolicy); policy != nil {
		if err := applyCredentialQueryEncoding(copy.URL, policy.query); err != nil {
			return nil, err
		}
	}
	if bindingSpec, _ := request.Context().Value(completedURLValidationContextKey{}).(string); bindingSpec == BindingSpecOpenAPI30 || bindingSpec == BindingSpecOpenAPI31 {
		if err := validateCompletedRequestURL(copy.URL); err != nil {
			return nil, err
		}
	}
	// §9.1: this binding never advertises a response-media preference.
	copy.Header.Del("Accept")
	if err := applyRequestContentCodings(copy, t.requestCodings); err != nil {
		return nil, err
	}
	response, err := base.RoundTrip(copy)
	if err != nil {
		return nil, err
	}
	governed, err := applyResponseGovernance(copy, response, t.responseCodings)
	if err != nil {
		if governance, _ := copy.Context().Value(mediaGovernanceContextKey{}).(*mediaGovernance); governance != nil {
			governance.recordFailure(err)
		}
		return nil, err
	}
	return governed, nil
}

// applyVerbatimServerAssembly restores the family rule after the standalone
// carrier has serialized the operation path and query against its normalized
// helper base. The operation suffix is appended byte-for-byte to the resolved
// Server URL, so a trailing server slash plus a leading path slash remains a
// deliberate double slash and is never resolved or repaired a second time.
func applyVerbatimServerAssembly(request *http.Request, policy *serverAssemblyPolicy) error {
	if request == nil || request.URL == nil || policy == nil {
		return fmt.Errorf("completed OpenAPI URL cannot be assembled")
	}
	engine, err := url.Parse(policy.engineBase)
	if err != nil {
		return fmt.Errorf("normalized server base does not parse: %w", err)
	}
	currentPath := request.URL.EscapedPath()
	enginePath := engine.EscapedPath()
	if !strings.HasPrefix(currentPath, enginePath) {
		return fmt.Errorf("serialized operation path %q does not retain engine base %q", currentPath, enginePath)
	}
	suffix := strings.TrimPrefix(currentPath, enginePath)
	completed := policy.resolvedBase + suffix
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		completed += "?" + request.URL.RawQuery
	}
	parsed, err := url.Parse(completed)
	if err != nil {
		return fmt.Errorf("completed OpenAPI URL does not parse under RFC 3986: %w", err)
	}
	request.URL = parsed
	request.Host = parsed.Host
	return nil
}

func applyCredentialQueryEncoding(target *url.URL, placements []credentialPlacement) error {
	if target == nil || len(placements) == 0 {
		return nil
	}
	parts := strings.Split(target.RawQuery, "&")
	for _, placement := range placements {
		matched := false
		for index, part := range parts {
			namePart, valuePart, hasValue := strings.Cut(part, "=")
			if !hasValue {
				continue
			}
			name, nameErr := url.QueryUnescape(namePart)
			value, valueErr := url.QueryUnescape(valuePart)
			if nameErr != nil || valueErr != nil || name != placement.name || value != placement.value {
				continue
			}
			parts[index] = percentEncodeCredentialQuery(placement.name) + "=" + percentEncodeCredentialQuery(placement.value)
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("query credential %q was not emitted at its declared destination", placement.name)
		}
	}
	target.RawQuery = strings.Join(parts, "&")
	return nil
}

func percentEncodeCredentialQuery(value string) string {
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '.' || char == '_' || char == '~' {
			encoded.WriteByte(char)
			continue
		}
		const hex = "0123456789ABCDEF"
		encoded.WriteByte('%')
		encoded.WriteByte(hex[char>>4])
		encoded.WriteByte(hex[char&0x0f])
	}
	return encoded.String()
}

// validateCompletedRequestURL performs §8.2's last pre-dispatch check after
// path and query serialization. Parsing the completed spelling catches bad
// escapes in components such as RawQuery; decoding each percent-bearing
// component makes the RFC 3986 requirement explicit without replacing the
// encoded delimiters that must remain on the wire.
func validateCompletedRequestURL(completed *url.URL) error {
	if completed == nil {
		return fmt.Errorf("completed OpenAPI URL is absent")
	}
	parsed, err := url.Parse(completed.String())
	if err != nil {
		return fmt.Errorf("completed OpenAPI URL does not parse under RFC 3986: %w", err)
	}
	for name, component := range map[string]string{
		"opaque":   parsed.Opaque,
		"path":     parsed.EscapedPath(),
		"query":    parsed.RawQuery,
		"fragment": parsed.EscapedFragment(),
	} {
		if _, err := url.PathUnescape(component); err != nil {
			return fmt.Errorf("completed OpenAPI URL %s does not percent-decode under RFC 3986: %w", name, err)
		}
	}
	return nil
}
