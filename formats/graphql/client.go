package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// introspectionSchema holds a parsed GraphQL introspection result.
type introspectionSchema struct {
	QueryType        *typeRef   `json:"queryType"`
	MutationType     *typeRef   `json:"mutationType"`
	SubscriptionType *typeRef   `json:"subscriptionType"`
	Types            []fullType `json:"types"`
}

type typeRef struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	OfType *typeRef `json:"ofType"`
}

type fullType struct {
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	Fields        []field      `json:"fields"`
	InputFields   []inputValue `json:"inputFields"`
	EnumValues    []enumValue  `json:"enumValues"`
	Interfaces    []typeRef    `json:"interfaces"`
	PossibleTypes []typeRef    `json:"possibleTypes"`
}

type field struct {
	Name              string       `json:"name"`
	Description       string       `json:"description"`
	Args              []inputValue `json:"args"`
	Type              typeRef      `json:"type"`
	IsDeprecated      bool         `json:"isDeprecated"`
	DeprecationReason string       `json:"deprecationReason"`
}

type inputValue struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Type         typeRef `json:"type"`
	DefaultValue *string `json:"defaultValue"`
}

type enumValue struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	IsDeprecated      bool   `json:"isDeprecated"`
	DeprecationReason string `json:"deprecationReason"`
}

type graphqlError struct {
	Message string `json:"message"`
}

type discovery struct {
	schema *introspectionSchema
}

// typeMap builds a name-keyed lookup from the schema's type list.
func (s *introspectionSchema) typeMap() map[string]*fullType {
	m := make(map[string]*fullType, len(s.Types))
	for i := range s.Types {
		m[s.Types[i].Name] = &s.Types[i]
	}
	return m
}

// rootTypeName returns the actual type name for a root operation type.
func (s *introspectionSchema) rootTypeName(rootType string) string {
	switch rootType {
	case "Query":
		if s.QueryType != nil {
			return s.QueryType.Name
		}
	case "Mutation":
		if s.MutationType != nil {
			return s.MutationType.Name
		}
	case "Subscription":
		if s.SubscriptionType != nil {
			return s.SubscriptionType.Name
		}
	}
	return ""
}

// discover introspects a GraphQL endpoint and returns the schema.
func discover(ctx context.Context, client *http.Client, endpointURL string, headers map[string]string) (*discovery, error) {
	schema, err := introspect(ctx, client, endpointURL, headers)
	if err != nil {
		return nil, err
	}
	return &discovery{schema: schema}, nil
}

// introspect sends the standard introspection query and parses the result.
func introspect(ctx context.Context, client *http.Client, endpointURL string, headers map[string]string) (*introspectionSchema, error) {
	data, _, errors, err := doGraphQLHTTP(ctx, client, endpointURL, introspectionQuery, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("introspection: %w", err)
	}
	if len(errors) > 0 {
		msgs := make([]string, len(errors))
		for i, e := range errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("introspection errors: %s", strings.Join(msgs, "; "))
	}

	schemaData, ok := data["__schema"]
	if !ok {
		return nil, fmt.Errorf("introspection response missing __schema field")
	}
	raw, err := json.Marshal(schemaData)
	if err != nil {
		return nil, fmt.Errorf("marshal __schema: %w", err)
	}
	var schema introspectionSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal __schema: %w", err)
	}
	return &schema, nil
}

// doGraphQLHTTP sends a GraphQL query over HTTP POST and returns the parsed
// data and errors from the response.
func doGraphQLHTTP(ctx context.Context, client *http.Client, endpointURL, query string, variables map[string]any, headers map[string]string) (map[string]any, http.Header, []graphqlError, error) {
	body := map[string]any{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// The +1 sentinel distinguishes an at-limit response from an over-limit
	// one (parity with openapi/connect).
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.Header, nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return nil, resp.Header, nil, fmt.Errorf("response exceeds %d byte limit", maxResponseBytes)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, resp.Header, nil, &httpError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header, nil, &httpError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var result struct {
		Data   map[string]any `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, resp.Header, nil, fmt.Errorf("parse response: %w", err)
	}
	return result.Data, resp.Header, result.Errors, nil
}

type httpError struct {
	StatusCode int
	Body       string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// streamSubscription opens a WebSocket connection using the graphql-ws
// protocol, subscribes, and drives the invocation handle: each `next` payload
// is emitted as one output, a `complete` closes the output side, and an
// `error`/transport failure fires a terminal error. The dial/handshake run
// under ctx so a cancelled invocation tears the connection down.
func streamSubscription(ctx context.Context, endpointURL, query string, variables map[string]any, headers map[string]string, inv openbindings.BindingHandle[any, any]) {
	wsURL := httpToWS(endpointURL)

	wsHeaders := http.Header{}
	for k, v := range headers {
		wsHeaders.Set(k, v)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"},
		HTTPHeader:   wsHeaders,
	})
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("websocket dial: %v", err)})
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := writeJSON(ctx, conn, map[string]any{"type": "connection_init"}); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("connection_init: %v", err)})
		return
	}
	if err := expectMessage(ctx, conn, "connection_ack"); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("connection_ack: %v", err)})
		return
	}

	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	if err := writeJSON(ctx, conn, map[string]any{
		"id":      "1",
		"type":    "subscribe",
		"payload": payload,
	}); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("subscribe: %v", err)})
		return
	}

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled; the handle is already terminal
			}
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: err.Error()})
			return
		}

		var msg struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: fmt.Sprintf("parse ws message: %v", err)})
			return
		}

		switch msg.Type {
		case "next":
			var payload struct {
				Data   any            `json:"data"`
				Errors []graphqlError `json:"errors"`
			}
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: fmt.Sprintf("parse next payload: %v", err)})
				return
			}
			if len(payload.Errors) > 0 {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeExecutionFailed,
					Message: payload.Errors[0].Message,
					Details: map[string]any{"errors": payload.Errors},
				})
				return
			}
			if err := inv.EmitOutput(payload.Data); err != nil {
				return // invocation terminated; stop reading
			}

		case "error":
			var errs []graphqlError
			if err := json.Unmarshal(msg.Payload, &errs); err != nil || len(errs) == 0 {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: string(msg.Payload)})
			} else {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeExecutionFailed,
					Message: errs[0].Message,
					Details: map[string]any{"errors": errs},
				})
			}
			return

		case "complete":
			inv.CloseOutput()
			return
		}
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

func expectMessage(ctx context.Context, conn *websocket.Conn, expectedType string) error {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return err
	}
	if msg.Type != expectedType {
		return fmt.Errorf("expected %q, got %q", expectedType, msg.Type)
	}
	return nil
}

func httpToWS(u string) string {
	if strings.HasPrefix(u, "https://") {
		return "wss://" + strings.TrimPrefix(u, "https://")
	}
	if strings.HasPrefix(u, "http://") {
		return "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u
}

// buildHTTPHeaders constructs HTTP headers from binding context and execution options.
// Returns nil when there are no headers to set (matches the convention used by
// other binding format libraries to avoid sending an empty map downstream).
func buildHTTPHeaders(bindCtx map[string]any) map[string]string {
	var headers map[string]string
	set := func(k, v string) {
		if headers == nil {
			headers = map[string]string{}
		}
		headers[k] = v
	}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		set("Authorization", "Bearer "+token)
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		set("Authorization", "ApiKey "+key)
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		set("Authorization", "Basic "+basicAuth(u, p))
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		set(k, v)
	}
	if cookies := openbindings.ContextCookies(bindCtx); len(cookies) > 0 {
		pairs := make([]string, 0, len(cookies))
		for k, v := range cookies {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		set("Cookie", strings.Join(pairs, "; "))
	}

	return headers
}

func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

const maxResponseBytes int64 = 10 * 1024 * 1024

const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: true) {
        name
        description
        args {
          name
          description
          type { ...TypeRef }
          defaultValue
        }
        type { ...TypeRef }
        isDeprecated
        deprecationReason
      }
      inputFields {
        name
        description
        type { ...TypeRef }
        defaultValue
      }
      enumValues(includeDeprecated: true) {
        name
        description
        isDeprecated
        deprecationReason
      }
      interfaces { ...TypeRef }
      possibleTypes { ...TypeRef }
    }
  }
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
            }
          }
        }
      }
    }
  }
}`
