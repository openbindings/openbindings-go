package asyncapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

const maxResponseBytes = 10 * 1024 * 1024 // 10 MB

func executeBindingWithDoc(ctx context.Context, client *http.Client, input *openbindings.BindingExecutionInput, doc *Document) *openbindings.ExecuteOutput {
	start := time.Now()

	opID, err := parseRef(input.Ref)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error())
	}

	asyncOp, ok := doc.Operations[opID]
	if !ok {
		return openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound, fmt.Sprintf("operation %q not in AsyncAPI doc", opID))
	}

	serverURL, protocol, err := resolveServer(doc, input.Options)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError, err.Error())
	}

	channelName := extractRefName(asyncOp.Channel.Ref)
	channel, hasChannel := doc.Channels[channelName]

	address := channelName
	if hasChannel && channel.Address != "" {
		address = channel.Address
	}

	switch asyncOp.Action {
	case "receive":
		return executeReceive(ctx, client, serverURL, protocol, address, input, doc, &asyncOp, start)
	case "send":
		return executeSend(ctx, client, serverURL, protocol, address, input, doc, &asyncOp, start)
	default:
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError, fmt.Sprintf("unknown action %q", asyncOp.Action))
	}
}

func subscribeBindingWithDoc(ctx context.Context, client *http.Client, input *openbindings.BindingExecutionInput, doc *Document, pool *wsPool) (<-chan openbindings.StreamEvent, error) {
	opID, err := parseRef(input.Ref)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeInvalidRef, err.Error())), nil
	}

	asyncOp, ok := doc.Operations[opID]
	if !ok {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeRefNotFound, fmt.Sprintf("operation %q not in AsyncAPI doc", opID))), nil
	}

	serverURL, protocol, err := resolveServer(doc, input.Options)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceConfigError, err.Error())), nil
	}

	channelName := extractRefName(asyncOp.Channel.Ref)
	channel, hasChannel := doc.Channels[channelName]
	address := channelName
	if hasChannel && channel.Address != "" {
		address = channel.Address
	}

	switch asyncOp.Action {
	case "receive":
		switch protocol {
		case "ws", "wss":
			return subscribeWS(ctx, serverURL, address, input, doc, &asyncOp)
		case "http", "https":
			return subscribeSSE(ctx, client, serverURL, address, input, doc, &asyncOp)
		default:
			return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceConfigError,
				fmt.Sprintf("streaming not supported for protocol %q (supported: http, https, ws, wss)", protocol))), nil
		}
	case "send":
		switch protocol {
		case "ws", "wss":
			return sendWS(ctx, pool, serverURL, address, input, doc, &asyncOp)
		default:
			return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceConfigError,
				fmt.Sprintf("streaming for send action requires ws or wss protocol (got %q)", protocol))), nil
		}
	default:
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceConfigError,
			fmt.Sprintf("unknown action %q", asyncOp.Action))), nil
	}
}

func parseRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty ref")
	}

	const prefix = "#/operations/"
	if strings.HasPrefix(ref, prefix) {
		opID := strings.TrimPrefix(ref, prefix)
		if opID == "" {
			return "", fmt.Errorf("empty operation ID in ref %q", ref)
		}
		return opID, nil
	}

	return ref, nil
}

func resolveServer(doc *Document, opts *openbindings.ExecutionOptions) (url string, protocol string, err error) {
	if opts != nil && opts.Metadata != nil {
		if base, ok := opts.Metadata["baseURL"].(string); ok && base != "" {
			proto := "http"
			if strings.HasPrefix(base, "https://") {
				proto = "https"
			} else if strings.HasPrefix(base, "wss://") {
				proto = "wss"
			} else if strings.HasPrefix(base, "ws://") {
				proto = "ws"
			}
			return strings.TrimRight(base, "/"), proto, nil
		}
	}

	serverNames := make([]string, 0, len(doc.Servers))
	for name := range doc.Servers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	for _, name := range serverNames {
		server := doc.Servers[name]
		proto := strings.ToLower(server.Protocol)
		host := server.Host
		pathname := server.PathName

		switch proto {
		case "http", "https", "ws", "wss":
			url := proto + "://" + host
			if pathname != "" {
				url += pathname
			}
			return strings.TrimRight(url, "/"), proto, nil
		}
	}

	return "", "", fmt.Errorf("no supported server found (need http, https, ws, or wss protocol)")
}

func executeReceive(ctx context.Context, client *http.Client, serverURL, protocol, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation, start time.Time) *openbindings.ExecuteOutput {
	maxEvents := 1
	if input.Input != nil {
		if m, ok := input.Input.(map[string]any); ok {
			if n, ok := m["maxEvents"].(float64); ok && n > 0 {
				maxEvents = int(n)
			}
		}
	}

	switch protocol {
	case "http", "https":
		return doSSESubscribe(ctx, client, serverURL, address, maxEvents, input, doc, asyncOp, start)
	default:
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError,
			fmt.Sprintf("receive not supported for protocol %q (supported: http, https)", protocol))
	}
}

func executeSend(ctx context.Context, client *http.Client, serverURL, protocol, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation, start time.Time) *openbindings.ExecuteOutput {
	switch protocol {
	case "http", "https":
		return doHTTPSend(ctx, client, serverURL, address, input, doc, asyncOp, start)
	default:
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError,
			fmt.Sprintf("send not supported for protocol %q (supported: http, https)", protocol))
	}
}

func doSSESubscribe(ctx context.Context, client *http.Client, serverURL, address string, maxEvents int, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation, start time.Time) *openbindings.ExecuteOutput {
	url := serverURL + "/" + strings.TrimLeft(address, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())
	}
	req.Header.Set("Accept", "text/event-stream")
	applyHTTPContext(req, doc, asyncOp, input.Context, input.Options)

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeConnectFailed, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
	}

	var events []any
	scanner := bufio.NewScanner(resp.Body)
	var dataLines []string
	var totalBytes int

	for scanner.Scan() && len(events) < maxEvents {
		line := scanner.Text()
		totalBytes += len(line) + 1 // +1 for newline
		if totalBytes > maxResponseBytes {
			return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError,
				fmt.Sprintf("SSE stream exceeds %d byte limit", maxResponseBytes))
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}

		if line == "" && len(dataLines) > 0 {
			events = append(events, parseSSEPayload(dataLines))
			dataLines = dataLines[:0]
		}
	}

	if len(dataLines) > 0 {
		events = append(events, parseSSEPayload(dataLines))
	}

	var output any
	if len(events) == 1 {
		output = events[0]
	} else {
		output = events
	}

	return &openbindings.ExecuteOutput{
		Output:     output,
		Status:     resp.StatusCode,
		DurationMs: time.Since(start).Milliseconds(),
	}
}

func subscribeSSE(ctx context.Context, client *http.Client, serverURL, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation) (<-chan openbindings.StreamEvent, error) {
	sseURL := serverURL + "/" + strings.TrimLeft(address, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeExecutionFailed, err.Error())), nil
	}
	req.Header.Set("Accept", "text/event-stream")
	applyHTTPContext(req, doc, asyncOp, input.Context, input.Options)

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeConnectFailed, err.Error())), nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return openbindings.SingleEventChannel(openbindings.HTTPErrorOutput(time.Now(), resp.StatusCode, resp.Status)), nil
	}

	ch := make(chan openbindings.StreamEvent)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		var dataLines []string
		var totalBytes int

		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}

			line := scanner.Text()
			totalBytes += len(line) + 1 // +1 for newline
			if totalBytes > maxResponseBytes {
				select {
				case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("SSE stream exceeds %d byte limit", maxResponseBytes),
				}}:
				case <-ctx.Done():
				}
				return
			}

			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				continue
			}

			if line == "" && len(dataLines) > 0 {
				ev := parseSSEPayload(dataLines)
				dataLines = dataLines[:0]
				select {
				case ch <- openbindings.StreamEvent{Data: ev}:
				case <-ctx.Done():
					return
				}
			}
		}

		if len(dataLines) > 0 {
			select {
			case ch <- openbindings.StreamEvent{Data: parseSSEPayload(dataLines)}:
			case <-ctx.Done():
			}
		}

		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			select {
			case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{Code: openbindings.ErrCodeStreamError, Message: err.Error()}}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

func doHTTPSend(ctx context.Context, client *http.Client, serverURL, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation, start time.Time) *openbindings.ExecuteOutput {
	url := serverURL + "/" + strings.TrimLeft(address, "/")

	var bodyData []byte
	if input.Input != nil {
		var marshalErr error
		bodyData, marshalErr = json.Marshal(input.Input)
		if marshalErr != nil {
			return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, marshalErr.Error())
		}
	} else {
		bodyData = []byte("{}")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyData))
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyHTTPContext(req, doc, asyncOp, input.Context, input.Options)

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	duration := time.Since(start).Milliseconds()

	if resp.StatusCode >= 400 {
		errOutput := openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
		if body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1)); readErr == nil && len(body) > 0 {
			if len(body) > maxResponseBytes {
				errOutput.Output = fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes)
			} else {
				var parsed any
				if json.Unmarshal(body, &parsed) == nil {
					errOutput.Output = parsed
				} else {
					errOutput.Output = string(body)
				}
			}
		}
		return errOutput
	}

	if resp.StatusCode == 202 || resp.StatusCode == 204 {
		return &openbindings.ExecuteOutput{
			Status:     resp.StatusCode,
			DurationMs: duration,
		}
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error())
	}
	if len(respBody) > maxResponseBytes {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError,
			fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes))
	}

	var output any
	if len(respBody) > 0 {
		trimmed := strings.TrimSpace(string(respBody))
		if openbindings.MaybeJSON(trimmed) {
			if json.Unmarshal(respBody, &output) != nil {
				output = string(respBody)
			}
		} else {
			output = string(respBody)
		}
	}

	return &openbindings.ExecuteOutput{
		Output:     output,
		Status:     resp.StatusCode,
		DurationMs: duration,
	}
}

func subscribeWS(ctx context.Context, serverURL, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation) (<-chan openbindings.StreamEvent, error) {
	wsURL := serverURL + "/" + strings.TrimLeft(address, "/")

	// Build HTTP headers for the upgrade request using applyHTTPContext.
	// applyHTTPContext may also append query parameters (for spec-driven apiKey
	// credentials placed in the query) to upgradeReq.URL.RawQuery, so we must
	// dial using the request's reconstructed URL rather than the original wsURL
	// to ensure those credentials reach the server. Browsers cannot set custom
	// WebSocket upgrade headers, so query-param apiKeys are the only way to
	// authenticate a WebSocket from a browser.
	upgradeReq, err := http.NewRequestWithContext(ctx, "GET", wsURL, nil)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeExecutionFailed, err.Error())), nil
	}
	applyHTTPContext(upgradeReq, doc, asyncOp, input.Context, input.Options)

	dialOpts := &websocket.DialOptions{
		HTTPHeader: upgradeReq.Header,
	}

	conn, _, err := websocket.Dial(ctx, upgradeReq.URL.String(), dialOpts)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeConnectFailed, err.Error())), nil
	}

	// Build and send the initial message: input fields spread at top level + bearerToken.
	payload := make(map[string]any)
	if input.Input != nil {
		if m, ok := input.Input.(map[string]any); ok {
			for k, v := range m {
				payload[k] = v
			}
		}
	}
	if token := openbindings.ContextBearerToken(input.Context); token != "" {
		payload["bearerToken"] = token
	}

	initMsg, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		conn.Close(websocket.StatusNormalClosure, "marshal error")
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeExecutionFailed, marshalErr.Error())), nil
	}

	if err := conn.Write(ctx, websocket.MessageText, initMsg); err != nil {
		conn.Close(websocket.StatusNormalClosure, "write error")
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeExecutionFailed, err.Error())), nil
	}

	ch := make(chan openbindings.StreamEvent)
	go func() {
		defer close(ch)
		defer conn.Close(websocket.StatusNormalClosure, "done")

		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				// On context cancellation, close cleanly.
				if ctx.Err() != nil {
					conn.Close(websocket.StatusNormalClosure, "aborted")
					return
				}
				// Normal close — just return.
				status := websocket.CloseStatus(err)
				if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
					return
				}
				select {
				case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{Code: openbindings.ErrCodeStreamError, Message: err.Error()}}:
				case <-ctx.Done():
				}
				return
			}

			var parsed map[string]any
			if json.Unmarshal(msg, &parsed) == nil {
				if errVal, ok := parsed["error"]; ok {
					// Send as error event.
					var execErr openbindings.ExecuteError
					if errMap, ok := errVal.(map[string]any); ok {
						if code, ok := errMap["code"].(string); ok {
							execErr.Code = code
						}
						if message, ok := errMap["message"].(string); ok {
							execErr.Message = message
						}
					} else {
						errJSON, _ := json.Marshal(errVal)
						execErr.Code = openbindings.ErrCodeStreamError
						execErr.Message = string(errJSON)
					}
					select {
					case ch <- openbindings.StreamEvent{Error: &execErr}:
					case <-ctx.Done():
						return
					}
				} else if dataVal, ok := parsed["data"]; ok {
					select {
					case ch <- openbindings.StreamEvent{Data: dataVal}:
					case <-ctx.Done():
						return
					}
				} else {
					// Send the whole parsed object as data.
					select {
					case ch <- openbindings.StreamEvent{Data: any(parsed)}:
					case <-ctx.Done():
						return
					}
				}
			} else {
				// Not JSON — send raw string as data.
				select {
				case ch <- openbindings.StreamEvent{Data: string(msg)}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

func parseSSEPayload(dataLines []string) any {
	raw := strings.Join(dataLines, "\n")
	var parsed any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		return parsed
	}
	return raw
}

// applyHTTPContext applies opaque binding context (credentials via well-known
// fields) and execution options (headers, cookies) to an HTTP request, using
// AsyncAPI securitySchemes for spec-driven credential placement.
func applyHTTPContext(req *http.Request, doc *Document, asyncOp *Operation, bindCtx map[string]any, opts *openbindings.ExecutionOptions) {
	if len(bindCtx) > 0 {
		applied, queryParams := applyCredentialsViaSecuritySchemes(req, doc, asyncOp, bindCtx)
		if !applied {
			applyCredentialsFallback(req, bindCtx)
		}
		if len(queryParams) > 0 {
			q := req.URL.Query()
			for k, vs := range queryParams {
				for _, v := range vs {
					q.Set(k, v)
				}
			}
			req.URL.RawQuery = q.Encode()
		}
	}

	if opts != nil {
		for k, v := range opts.Headers {
			req.Header.Set(k, v)
		}
		for k, v := range opts.Cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
	}
}

// resolveSecuritySchemes returns the security schemes applicable to an operation.
// Operation-level security overrides server-level; falls back to the first server's
// security if not set on the operation.
func resolveSecuritySchemes(doc *Document, asyncOp *Operation) []SecurityScheme {
	var requirements []map[string][]string

	if asyncOp != nil && len(asyncOp.Security) > 0 {
		requirements = asyncOp.Security
	}

	if len(requirements) == 0 && doc.Servers != nil {
		// Use the first server's security (sorted by name for determinism).
		serverNames := make([]string, 0, len(doc.Servers))
		for name := range doc.Servers {
			serverNames = append(serverNames, name)
		}
		sort.Strings(serverNames)
		for _, name := range serverNames {
			srv := doc.Servers[name]
			if len(srv.Security) > 0 {
				requirements = srv.Security
				break
			}
		}
	}

	if len(requirements) == 0 {
		return nil
	}

	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return nil
	}

	var result []SecurityScheme
	seen := map[string]bool{}
	for _, req := range requirements {
		for schemeName := range req {
			if seen[schemeName] {
				continue
			}
			seen[schemeName] = true
			if scheme, ok := doc.Components.SecuritySchemes[schemeName]; ok {
				result = append(result, scheme)
			}
		}
	}
	return result
}

// applyCredentialsViaSecuritySchemes reads the AsyncAPI doc's securitySchemes
// and operation/server-level security requirements to place credentials exactly
// where the spec declares (header, query, or cookie with the correct name).
func applyCredentialsViaSecuritySchemes(req *http.Request, doc *Document, asyncOp *Operation, bindCtx map[string]any) (applied bool, queryParams url.Values) {
	schemes := resolveSecuritySchemes(doc, asyncOp)
	if len(schemes) == 0 {
		return false, nil
	}

	queryParams = url.Values{}

	for _, s := range schemes {
		switch s.Type {
		case "apiKey", "httpApiKey":
			val := openbindings.ContextAPIKey(bindCtx)
			if val == "" {
				continue
			}
			switch s.In {
			case "header":
				name := s.Name
				if name == "" {
					name = "Authorization"
				}
				req.Header.Set(name, val)
				applied = true
			case "query":
				if s.Name != "" {
					queryParams.Set(s.Name, val)
					applied = true
				}
			case "cookie":
				if s.Name != "" {
					req.AddCookie(&http.Cookie{Name: s.Name, Value: val})
					applied = true
				}
			}

		case "http":
			switch strings.ToLower(s.Scheme) {
			case "bearer":
				if token := openbindings.ContextBearerToken(bindCtx); token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
					applied = true
				}
			case "basic":
				if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
					req.SetBasicAuth(u, p)
					applied = true
				}
			}

		case "httpBearer":
			if token := openbindings.ContextBearerToken(bindCtx); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				applied = true
			}

		case "userPassword":
			if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
				req.SetBasicAuth(u, p)
				applied = true
			}
		}
	}

	return applied, queryParams
}

// applyCredentialsFallback applies credentials using sensible defaults when
// no securitySchemes are defined in the spec.
func applyCredentialsFallback(req *http.Request, bindCtx map[string]any) {
	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		req.SetBasicAuth(u, p)
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		req.Header.Set("Authorization", "ApiKey "+key)
	}
}
