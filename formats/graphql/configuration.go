package graphql

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/invoke"
)

type documentConfiguration struct {
	Source        string
	OperationName string
}

type protocolConfiguration struct {
	HTTPHeaders       map[string]string
	HTTPCookies       map[string]string
	WebSocketHeaders  map[string]string
	WebSocketCookies  map[string]string
	ConnectionInit    any
	ConnectionInitSet bool
}

type bindingConfiguration struct {
	Document           *documentConfiguration
	SubscriptionTarget string
	Protocol           protocolConfiguration
}

func readConfiguration(ctx map[string]any) (*bindingConfiguration, error) {
	cfg := &bindingConfiguration{}
	if hasUnnamedCredential(ctx) {
		return nil, fmt.Errorf("GraphQL does not infer protocol placement for generic credential context; supply the artifact's explicitly named header, cookie, or connection-init field through configuration.protocolFields")
	}
	raw := invoke.ContextConfiguration(ctx)
	if raw == nil {
		return cfg, nil
	}

	if value, present := raw["document"]; present {
		doc, err := readDocumentConfiguration(value)
		if err != nil {
			return nil, err
		}
		cfg.Document = doc
	}
	if value, present := raw["subscriptionTarget"]; present {
		target, ok := value.(string)
		if !ok || target == "" {
			return nil, fmt.Errorf("configuration.subscriptionTarget must be a non-empty absolute ws or wss URI")
		}
		u, err := url.Parse(target)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
			return nil, fmt.Errorf("configuration.subscriptionTarget must be a non-empty absolute ws or wss URI")
		}
		cfg.SubscriptionTarget = target
	}
	if value, present := raw["protocolFields"]; present {
		protocol, err := readProtocolConfiguration(value)
		if err != nil {
			return nil, err
		}
		cfg.Protocol = protocol
	}
	return cfg, nil
}

func hasUnnamedCredential(ctx map[string]any) bool {
	if invoke.ContextBearerToken(ctx) != "" || invoke.ContextAPIKey(ctx) != "" || invoke.ContextString(ctx, "accessToken") != "" {
		return true
	}
	if _, _, ok := invoke.ContextBasicAuth(ctx); ok {
		return true
	}
	if keys, ok := ctx["apiKeys"].(map[string]any); ok && len(keys) > 0 {
		return true
	}
	if keys, ok := ctx["apiKeys"].(map[string]string); ok && len(keys) > 0 {
		return true
	}
	return false
}

func readDocumentConfiguration(value any) (*documentConfiguration, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("configuration.document must supply non-empty GraphQL source text")
		}
		return &documentConfiguration{Source: v}, nil
	case map[string]any:
		for name := range v {
			if name != "source" && name != "operationName" {
				return nil, fmt.Errorf("configuration.document member %q is not defined", name)
			}
		}
		source, ok := v["source"].(string)
		if !ok || source == "" {
			return nil, fmt.Errorf("configuration.document.source must be a non-empty string")
		}
		doc := &documentConfiguration{Source: source}
		if raw, present := v["operationName"]; present {
			name, ok := raw.(string)
			if !ok || !graphqlName.MatchString(name) {
				return nil, fmt.Errorf("configuration.document.operationName must be a GraphQL Name")
			}
			doc.OperationName = name
		}
		return doc, nil
	default:
		return nil, fmt.Errorf("configuration.document must be GraphQL source text or an object with source and optional operationName")
	}
}

func readProtocolConfiguration(value any) (protocolConfiguration, error) {
	var out protocolConfiguration
	raw, ok := value.(map[string]any)
	if !ok {
		return out, fmt.Errorf("configuration.protocolFields must be an object")
	}
	allowed := map[string]bool{
		"httpHeaders": true, "httpCookies": true,
		"websocketHeaders": true, "websocketCookies": true,
		"connectionInitPayload": true,
	}
	for name := range raw {
		if !allowed[name] {
			return out, fmt.Errorf("configuration.protocolFields member %q is not defined", name)
		}
	}
	var err error
	if out.HTTPHeaders, err = readStringMap(raw, "httpHeaders"); err != nil {
		return out, err
	}
	if out.HTTPCookies, err = readStringMap(raw, "httpCookies"); err != nil {
		return out, err
	}
	if out.WebSocketHeaders, err = readStringMap(raw, "websocketHeaders"); err != nil {
		return out, err
	}
	if out.WebSocketCookies, err = readStringMap(raw, "websocketCookies"); err != nil {
		return out, err
	}
	if init, present := raw["connectionInitPayload"]; present {
		if init != nil {
			if _, ok := init.(map[string]any); !ok {
				return out, fmt.Errorf("configuration.protocolFields.connectionInitPayload must be an object or null")
			}
		}
		out.ConnectionInit, out.ConnectionInitSet = init, true
	}
	return out, nil
}

func readStringMap(raw map[string]any, point string) (map[string]string, error) {
	value, present := raw[point]
	if !present {
		return nil, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configuration.protocolFields.%s must be an object of strings", point)
	}
	out := make(map[string]string, len(obj))
	for name, rawValue := range obj {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("configuration.protocolFields.%s[%q] must be a string", point, name)
		}
		out[name] = value
	}
	return out, nil
}

var httpOwnedHeaders = map[string]bool{
	"content-type":   true,
	"accept":         true,
	"content-length": true,
	"host":           true,
}

var websocketOwnedHeaders = map[string]bool{
	"host":                   true,
	"connection":             true,
	"upgrade":                true,
	"sec-websocket-key":      true,
	"sec-websocket-version":  true,
	"sec-websocket-protocol": true,
}

func effectiveHeaders(explicit, contextual, cookies, contextualCookies map[string]string, owned map[string]bool) (map[string]string, error) {
	headers := map[string]string{}
	seen := map[string]string{}
	add := func(name, value, origin string) error {
		folded := strings.ToLower(name)
		if owned[folded] {
			return fmt.Errorf("%s header %q collides with a processor-owned field", origin, name)
		}
		if prior, ok := seen[folded]; ok {
			return fmt.Errorf("%s header %q collides with %s", origin, name, prior)
		}
		seen[folded] = origin
		headers[name] = value
		return nil
	}
	for name, value := range contextual {
		if err := add(name, value, "context.headers"); err != nil {
			return nil, err
		}
	}
	for name, value := range explicit {
		if err := add(name, value, "configuration.protocolFields"); err != nil {
			return nil, err
		}
	}

	allCookies := map[string]string{}
	for name, value := range contextualCookies {
		allCookies[name] = value
	}
	for name, value := range cookies {
		if _, duplicate := allCookies[name]; duplicate {
			return nil, fmt.Errorf("cookie %q is supplied more than once", name)
		}
		allCookies[name] = value
	}
	if len(allCookies) > 0 {
		if prior, ok := seen["cookie"]; ok {
			return nil, fmt.Errorf("cookie entries collide with raw Cookie header from %s", prior)
		}
		names := make([]string, 0, len(allCookies))
		for name := range allCookies {
			names = append(names, name)
		}
		sort.Strings(names)
		pairs := make([]string, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, name+"="+allCookies[name])
		}
		headers["Cookie"] = strings.Join(pairs, "; ")
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}

func (c *bindingConfiguration) httpHeaders(ctx map[string]any) (map[string]string, error) {
	return effectiveHeaders(c.Protocol.HTTPHeaders, invoke.ContextHeaders(ctx), c.Protocol.HTTPCookies, invoke.ContextCookies(ctx), httpOwnedHeaders)
}

func (c *bindingConfiguration) websocketHeaders(ctx map[string]any) (http.Header, error) {
	values, err := effectiveHeaders(c.Protocol.WebSocketHeaders, nil, c.Protocol.WebSocketCookies, nil, websocketOwnedHeaders)
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	for name, value := range values {
		headers.Set(name, value)
	}
	return headers, nil
}

func validateHTTPLocation(location string) error {
	u, err := url.Parse(location)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("GraphQL source location must be an absolute http or https URI")
	}
	return nil
}

func configurationRequirement(target, point, description string) *invoke.ContextRequiredDetails {
	durable := true
	return &invoke.ContextRequiredDetails{
		Target: target,
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{
			invoke.NewConfigValueRequirement(point, "", description, nil, &durable),
		}}},
	}
}
