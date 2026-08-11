package asyncapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// This file honors declared protocol `bindings` objects, which are
// authoritative where they speak (openbindings.asyncapi@1 §8, ASYNC-P-02):
// an http operation binding's `method` selects the request method; a
// websockets channel binding's `method`, `query`, and `headers` govern the
// upgrade request, with declared query and header values supplied like
// address parameters (§9.2) and any unsatisfied required declaration a
// pre-dispatch refusal. Revision 1 requires the HTTP publish method instead
// of inventing one; the WebSocket protocol itself fixes the upgrade method.

// requestMethod returns the request method for an http-protocol cell: the
// http operation binding's `method` where declared. The default parameter
// remains only for the package's unreachable legacy SSE helper.
func requestMethod(op *asyncOperation, cellDefault string) string {
	if op != nil && op.Bindings != nil && op.Bindings.HTTP != nil && op.Bindings.HTTP.Method != "" {
		return strings.ToUpper(strings.TrimSpace(op.Bindings.HTTP.Method))
	}
	return cellDefault
}

// wsUpgrade is the websockets channel binding's resolved contribution to
// the upgrade request: query values (which join the dialed URL, and so the
// pool key) and header values (applied to the upgrade request, and hashed
// into the connection's credential identity).
type wsUpgrade struct {
	Query   url.Values
	Headers map[string]string
}

type protocolFields struct {
	HTTPQuery        map[string]string
	WebSocketQuery   map[string]string
	WebSocketHeaders map[string]string
}

func protocolFieldValues(ctx map[string]any) (protocolFields, error) {
	var out protocolFields
	raw, present := openbindings.ContextConfiguration(ctx)["protocolFields"]
	if !present {
		return out, nil
	}
	record, ok := raw.(map[string]any)
	if !ok {
		return out, fmt.Errorf("configuration.protocolFields must be an object")
	}
	for name, value := range record {
		var target *map[string]string
		switch name {
		case "httpQuery":
			target = &out.HTTPQuery
		case "webSocketQuery":
			target = &out.WebSocketQuery
		case "webSocketHeaders":
			target = &out.WebSocketHeaders
		default:
			return out, fmt.Errorf("configuration.protocolFields names unsupported map %q", name)
		}
		m, ok := value.(map[string]any)
		if !ok {
			return out, fmt.Errorf("configuration.protocolFields.%s must be an object of strings", name)
		}
		*target = map[string]string{}
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return out, fmt.Errorf("configuration.protocolFields.%s[%q] must be a string", name, k)
			}
			(*target)[k] = s
		}
	}
	return out, nil
}

func resolveHTTPQuery(op *asyncOperation, supplied map[string]string) (map[string]string, error) {
	if op == nil || op.Bindings == nil || op.Bindings.HTTP == nil {
		return nil, nil
	}
	return schemaPropertyValues(op.Bindings.HTTP.Query, supplied, nil, "HTTP operation binding query field")
}

// resolveWSUpgrade resolves the WebSockets channel binding against the
// protocolFields query and header maps. The two artifact channels remain
// distinct; a query value never satisfies a header declaration or vice
// versa. A required declaration left unsatisfied is a pre-dispatch refusal.
func resolveWSUpgrade(ch *channel, channelName string, queryFields map[string]string, headerFields map[string]string) (wsUpgrade, error) {
	var up wsUpgrade
	if ch == nil || ch.Bindings == nil || ch.Bindings.WS == nil {
		return up, nil
	}
	b := ch.Bindings.WS

	if b.Method != "" && !strings.EqualFold(strings.TrimSpace(b.Method), http.MethodGet) {
		return up, fmt.Errorf("channel %q: websockets binding declares upgrade method %q, which this platform cannot apply (RFC 6455 upgrades are GET) — refused rather than silently rerouted", channelName, b.Method)
	}

	queryVals, err := schemaPropertyValues(b.Query, queryFields, nil,
		fmt.Sprintf("channel %q: websockets binding query parameter", channelName))
	if err != nil {
		return up, err
	}
	if len(queryVals) > 0 {
		up.Query = url.Values{}
		for name, val := range queryVals {
			up.Query.Set(name, val)
		}
	}

	headerVals, err := schemaPropertyValues(b.Headers, headerFields, nil,
		fmt.Sprintf("channel %q: websockets binding header", channelName))
	if err != nil {
		return up, err
	}
	up.Headers = headerVals
	return up, nil
}

// schemaPropertyValues resolves the declared properties of a ws-binding
// `query`/`headers` Schema Object: each declared property takes the
// consumer-supplied value, else a satisfied generic-carriage value (headers
// only; the value already rides the request, so it resolves the declaration
// without being duplicated here), else the property's declared `default`.
// A `required` property left unresolved is a pre-dispatch refusal
// (ASYNC-P-02). Only scalar defaults map to a wire value.
func schemaPropertyValues(schema map[string]any, supplied map[string]string, satisfiedElsewhere map[string]string, what string) (map[string]string, error) {
	if schema == nil {
		return nil, nil
	}
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if rawReq, ok := schema["required"].([]any); ok {
		for _, r := range rawReq {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}

	names := make(map[string]bool, len(props)+len(required))
	for name := range props {
		names[name] = true
	}
	for name := range required {
		names[name] = true
	}

	out := map[string]string{}
	for name := range names {
		if val, ok := supplied[name]; ok {
			out[name] = val
			continue
		}
		if hasCaseInsensitive(satisfiedElsewhere, name) {
			continue // already riding the request via the generic carriage
		}
		if required[name] {
			return nil, fmt.Errorf("%s %q is required but has no supplied value and no declared default", what, name)
		}
	}
	for name := range supplied {
		prop, declared := props[name]
		if !declared {
			return nil, fmt.Errorf("%s %q is not declared by the binding schema", what, name)
		}
		if schema, ok := prop.(map[string]any); ok {
			if typ, exists := schema["type"]; exists && typ != "string" {
				return nil, fmt.Errorf("%s %q cannot admit a string value", what, name)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// hasCaseInsensitive reports whether m carries key under HTTP header
// case-insensitivity.
func hasCaseInsensitive(m map[string]string, key string) bool {
	for k := range m {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// stringifyScalar renders a JSON-schema scalar default as a wire value.
// Structured values (objects, arrays, null) resolve nothing.
func stringifyScalar(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case bool:
		return strconv.FormatBool(s), true
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64), true
	case int:
		return strconv.Itoa(s), true
	case int64:
		return strconv.FormatInt(s, 10), true
	}
	return "", false
}

// mergeQuery appends resolved ws-binding query values onto an expanded
// address, keeping the result a single dialable path-and-query string (so
// the pool key and the dialed URL always agree).
func mergeQuery(address string, q url.Values) string {
	if len(q) == 0 {
		return address
	}
	sep := "?"
	if strings.Contains(address, "?") {
		sep = "&"
	}
	return address + sep + q.Encode()
}
