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
	Name string  `json:"name"`
	Kind string  `json:"kind"`
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
func discover(ctx context.Context, endpointURL string, headers map[string]string) (*discovery, error) {
	schema, err := introspect(ctx, endpointURL, headers)
	if err != nil {
		return nil, err
	}
	return &discovery{schema: schema}, nil
}

// introspect sends the standard introspection query and parses the result.
func introspect(ctx context.Context, endpointURL string, headers map[string]string) (*introspectionSchema, error) {
	data, errors, err := doGraphQLHTTP(ctx, endpointURL, introspectionQuery, nil, headers)
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
func doGraphQLHTTP(ctx context.Context, endpointURL, query string, variables map[string]any, headers map[string]string) (map[string]any, []graphqlError, error) {
	body := map[string]any{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, nil, &httpError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data   map[string]any `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, nil, fmt.Errorf("parse response: %w", err)
	}
	return result.Data, result.Errors, nil
}

type httpError struct {
	StatusCode int
	Body       string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// subscribeGraphQL opens a WebSocket connection using the graphql-ws protocol
// and subscribes to a GraphQL subscription, streaming events to the returned channel.
func subscribeGraphQL(ctx context.Context, endpointURL, query string, variables map[string]any, headers map[string]string) (<-chan openbindings.StreamEvent, error) {
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
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	// connection_init
	if err := writeJSON(ctx, conn, map[string]any{"type": "connection_init"}); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("connection_init: %w", err)
	}

	// Wait for connection_ack
	if err := expectMessage(ctx, conn, "connection_ack"); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("connection_ack: %w", err)
	}

	// Send subscribe
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	if err := writeJSON(ctx, conn, map[string]any{
		"id":      "1",
		"type":    "subscribe",
		"payload": payload,
	}); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	ch := make(chan openbindings.StreamEvent, 16)
	go func() {
		defer close(ch)
		defer conn.Close(websocket.StatusNormalClosure, "")

		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeStreamError,
					Message: err.Error(),
				}}
				return
			}

			var msg struct {
				Type    string         `json:"type"`
				ID      string         `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("parse ws message: %v", err),
				}}
				return
			}

			switch msg.Type {
			case "next":
				var payload struct {
					Data   any            `json:"data"`
					Errors []graphqlError `json:"errors"`
				}
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
						Code:    openbindings.ErrCodeResponseError,
						Message: fmt.Sprintf("parse next payload: %v", err),
					}}
					return
				}
				if len(payload.Errors) > 0 {
					ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
						Code:    openbindings.ErrCodeExecutionFailed,
						Message: payload.Errors[0].Message,
					}}
					return
				}
				ch <- openbindings.StreamEvent{Data: payload.Data}

			case "error":
				var errors []graphqlError
				if err := json.Unmarshal(msg.Payload, &errors); err != nil {
					ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
						Code:    openbindings.ErrCodeExecutionFailed,
						Message: string(msg.Payload),
					}}
				} else if len(errors) > 0 {
					ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
						Code:    openbindings.ErrCodeExecutionFailed,
						Message: errors[0].Message,
					}}
				}
				return

			case "complete":
				return
			}
		}
	}()

	return ch, nil
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
func buildHTTPHeaders(bindCtx map[string]any, opts *openbindings.ExecutionOptions) map[string]string {
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

	if opts != nil {
		for k, v := range opts.Headers {
			set(k, v)
		}
		if len(opts.Cookies) > 0 {
			pairs := make([]string, 0, len(opts.Cookies))
			for k, v := range opts.Cookies {
				pairs = append(pairs, k+"="+v)
			}
			sort.Strings(pairs)
			set("Cookie", strings.Join(pairs, "; "))
		}
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
