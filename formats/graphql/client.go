package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
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
	case "query":
		if s.QueryType != nil {
			return s.QueryType.Name
		}
	case "mutation":
		if s.MutationType != nil {
			return s.MutationType.Name
		}
	case "subscription":
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
	// Discovery lane: an artifact-side introspection fetch, not a delivery
	// unit — it stays at the fixed default rather than any consumer bound.
	result, err := doGraphQLHTTP(ctx, client, endpointURL, introspectionQuery, "", nil, headers, openbindings.DefaultMaxDeliveryUnitBytes)
	if err != nil {
		return nil, fmt.Errorf("introspection: %w", err)
	}
	if rawErrors, present := result.Body["errors"]; present {
		var errors []graphqlError
		raw, _ := json.Marshal(rawErrors)
		_ = json.Unmarshal(raw, &errors)
		msgs := make([]string, len(errors))
		for i, item := range errors {
			msgs[i] = item.Message
		}
		return nil, fmt.Errorf("introspection errors: %s", strings.Join(msgs, "; "))
	}
	data, ok := result.Body["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("introspection response missing object data")
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

type graphQLHTTPResult struct {
	Body       map[string]any
	Header     http.Header
	StatusCode int
	MediaType  string
}

// doGraphQLHTTP sends one GraphQL-over-HTTP POST and classifies the final
// response using the incorporated media-type/status rules. A well-formed
// application/graphql-response+json envelope is returned regardless of
// status; application/json is returned only for a 2xx response.
func doGraphQLHTTP(ctx context.Context, client *http.Client, endpointURL, query, operationName string, variables map[string]any, headers map[string]string, maxBytes int64) (*graphQLHTTPResult, error) {
	body := map[string]any{"query": query}
	if operationName != "" {
		body["operationName"] = operationName
	}
	if variables != nil {
		body["variables"] = variables
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/graphql-response+json, application/json;q=0.9")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	dispatchClient := clientWithGraphQLRedirectPolicy(client, raw)
	resp, err := dispatchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// The +1 sentinel distinguishes an at-limit response from an over-limit
	// one (parity with openapi/connect).
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(respBody)) > maxBytes {
		return nil, newGraphQLHTTPError(resp, respBody[:maxBytes], "", true, true, fmt.Sprintf("response exceeds %d byte limit", maxBytes))
	}

	mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaErr != nil {
		return nil, newGraphQLHTTPError(resp, respBody, "", true, false, fmt.Sprintf("invalid GraphQL response Content-Type: %v", mediaErr))
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "application/graphql-response+json" && mediaType != "application/json" {
		return nil, newGraphQLHTTPError(resp, respBody, mediaType, true, false, fmt.Sprintf("unsupported GraphQL response media type %q", mediaType))
	}
	if mediaType == "application/json" && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return nil, newGraphQLHTTPError(resp, respBody, mediaType, false, false, "")
	}
	var result any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, newGraphQLHTTPError(resp, respBody, mediaType, true, false, fmt.Sprintf("parse GraphQL response: %v", err))
	}
	envelope, ok := result.(map[string]any)
	if !ok || !wellFormedGraphQLResponse(envelope) {
		return nil, newGraphQLHTTPError(resp, respBody, mediaType, true, false, "response is not a well-formed GraphQL response envelope")
	}
	return &graphQLHTTPResult{Body: envelope, Header: resp.Header, StatusCode: resp.StatusCode, MediaType: mediaType}, nil
}

func clientWithGraphQLRedirectPolicy(client *http.Client, body []byte) *http.Client {
	clone := *client
	prior := client.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.Method != http.MethodPost {
			return http.ErrUseLastResponse
		}
		if req.Header.Get("Content-Type") != "application/json" ||
			req.Header.Get("Accept") != "application/graphql-response+json, application/json;q=0.9" {
			return http.ErrUseLastResponse
		}
		if req.GetBody == nil {
			return http.ErrUseLastResponse
		}
		replay, err := req.GetBody()
		if err != nil {
			return http.ErrUseLastResponse
		}
		replayed, err := io.ReadAll(replay)
		_ = replay.Close()
		if err != nil || !bytes.Equal(replayed, body) {
			return http.ErrUseLastResponse
		}
		if prior != nil {
			return prior(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}
	return &clone
}

func wellFormedGraphQLResponse(envelope map[string]any) bool {
	data, hasData := envelope["data"]
	errorsValue, hasErrors := envelope["errors"]
	if !hasData && !hasErrors {
		return false
	}
	if hasData && data != nil {
		if _, ok := data.(map[string]any); !ok {
			return false
		}
	}
	if hasErrors {
		errors, ok := errorsValue.([]any)
		if !ok || len(errors) == 0 {
			return false
		}
		for _, raw := range errors {
			item, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			if message, ok := item["message"].(string); !ok || message == "" {
				return false
			}
		}
	}
	if extensions, present := envelope["extensions"]; present && extensions != nil {
		if _, ok := extensions.(map[string]any); !ok {
			return false
		}
	}
	return true
}

type httpError struct {
	StatusCode int
	Status     string
	URL        string
	Header     http.Header
	Body       []byte
	MediaType  string
	Protocol   bool
	Truncated  bool
	Message    string
}

func (e *httpError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, string(e.Body))
}

func newGraphQLHTTPError(resp *http.Response, body []byte, mediaType string, protocol, truncated bool, message string) *httpError {
	url := ""
	if resp.Request != nil && resp.Request.URL != nil {
		url = resp.Request.URL.String()
	}
	return &httpError{
		StatusCode: resp.StatusCode, Status: resp.Status, URL: url,
		Header: resp.Header.Clone(), Body: append([]byte(nil), body...),
		MediaType: mediaType, Protocol: protocol, Truncated: truncated, Message: message,
	}
}

func (e *httpError) invocationError() *openbindings.InvocationError {
	var ierr *openbindings.InvocationError
	if e.Protocol {
		ierr = &openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: e.Error()}
	} else {
		ierr = openbindings.HTTPError(e.StatusCode, e.Status)
	}
	headers := map[string][]string{}
	for name, values := range e.Header {
		headers[strings.ToLower(name)] = append([]string(nil), values...)
	}
	body := map[string]any{
		"base64": base64.StdEncoding.EncodeToString(e.Body), "byteLength": len(e.Body),
	}
	if e.Truncated {
		body["truncated"] = true
	}
	ierr.Diagnostics = map[string]any{
		"status": e.StatusCode,
		"body":   string(e.Body),
		"httpResponse": map[string]any{
			"status": e.StatusCode, "statusText": e.Status, "url": e.URL,
			"headers": headers, "body": body,
		},
		"graphql": map[string]any{"mediaType": e.MediaType},
	}
	return ierr
}

// streamSubscription opens a WebSocket connection using the graphql-ws
// protocol, subscribes, and drives the invocation handle: each `next` payload
// is emitted as one output, a `complete` closes the output side, and an
// `error`/transport failure fires a terminal error. The dial/handshake run
// under ctx so a cancelled invocation tears the connection down.
func streamSubscription(ctx context.Context, client *http.Client, target string, document *documentConfiguration, variables map[string]any, headers http.Header, initPayload any, initPayloadSet bool, maxUnit int64, inv openbindings.BindingHandle[any, any]) {
	conn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"},
		HTTPHeader:   headers,
		HTTPClient:   client,
	})
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("websocket dial: %v", err)})
		return
	}
	subscribed := false
	defer func() {
		if subscribed && ctx.Err() != nil {
			sendSubscriptionComplete(conn)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	// One WebSocket message is one delivery unit: apply the resolved bound
	// as the per-message read limit (the library default is ~32 KiB — far
	// below the documented 10 MiB convention). An over-limit message errors
	// the read below and surfaces as ERR_STREAM_ERROR.
	conn.SetReadLimit(maxUnit)

	init := map[string]any{"type": "connection_init"}
	if initPayloadSet {
		init["payload"] = initPayload
	}
	if err := writeJSON(ctx, conn, init); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("connection_init: %v", err)})
		return
	}
	if err := expectMessage(ctx, conn, "connection_ack"); err != nil {
		if errors.Is(err, errWSClosed) {
			// The connection ended before a handshake reply arrived. A
			// stable, library-independent message beats leaking the
			// transport's raw close-frame text (TS parity: "WebSocket
			// closed during handshake").
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: "WebSocket closed during handshake", Diagnostics: graphQLWSCloseDetails(err)})
		} else {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: fmt.Sprintf("connection_ack: %v", err)})
		}
		return
	}

	payload := map[string]any{"query": document.Source}
	if document.OperationName != "" {
		payload["operationName"] = document.OperationName
	}
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
	subscribed = true

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled; the handle is already terminal
			}
			// Once any close frame arrives (clean or abnormal), Read always
			// errors — there is no library-agnostic way to tell "the peer
			// hung up cleanly" from "the network died" here, and either way
			// ending without a `complete` frame is an abnormal termination.
			// A stable message beats leaking the transport's raw close-frame
			// text (TS parity: "WebSocket closed before subscription
			// complete").
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: "WebSocket closed before subscription complete", Diagnostics: graphQLWSCloseDetails(err)})
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
			var payload any
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: fmt.Sprintf("parse next payload: %v", err)})
				return
			}
			envelope, ok := payload.(map[string]any)
			if !ok || !wellFormedGraphQLResponse(envelope) {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: "next payload is not a well-formed GraphQL response envelope"})
				return
			}
			if err := inv.EmitOutput(envelope); err != nil {
				return // invocation terminated; stop reading
			}

		case "error":
			var payload any
			_ = json.Unmarshal(msg.Payload, &payload)
			message := string(msg.Payload)
			if values, ok := payload.([]any); ok && len(values) > 0 {
				if first, ok := values[0].(map[string]any); ok {
					if nativeMessage, ok := first["message"].(string); ok && nativeMessage != "" {
						message = nativeMessage
					}
				}
			}
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeExecutionFailed,
				Message: message,
				Diagnostics: map[string]any{"graphqlTransportWs": map[string]any{
					"type": "error", "payload": payload,
					"payloadBase64": base64.StdEncoding.EncodeToString(msg.Payload),
				}},
			})
			return

		case "complete":
			inv.CloseOutput()
			return
		}
	}
}

func graphQLWSCloseDetails(err error) map[string]any {
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		return nil
	}
	return map[string]any{"graphqlTransportWs": map[string]any{
		"type": "close", "code": int(closeErr.Code), "reason": closeErr.Reason,
	}}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

// errWSClosed marks a connection-close detected while expecting a specific
// handshake message, so the caller can substitute a stable,
// library-independent message instead of leaking the transport's raw
// close-frame text.
var errWSClosed = errors.New("websocket closed")

func expectMessage(ctx context.Context, conn *websocket.Conn, expectedType string) error {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errWSClosed, err)
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

func sendSubscriptionComplete(conn *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = writeJSON(ctx, conn, map[string]any{"id": "1", "type": "complete"})
}

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
