package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// discovery holds the results of an MCP server's capability negotiation.
type discovery struct {
	Tools             []*gomcp.Tool
	RequiredTaskTools map[string]bool
	Resources         []*gomcp.Resource
	ResourceTemplates []*gomcp.ResourceTemplate
	Prompts           []*gomcp.Prompt
	ServerInfo        *gomcp.Implementation
}

func clientInfo(version string) *gomcp.Implementation {
	return &gomcp.Implementation{
		Name:    "ob",
		Version: version,
	}
}

func discover(ctx context.Context, clientVersion string, baseClient *http.Client, url string) (*discovery, error) {
	requiredTaskTools := map[string]bool{}
	observe := func(method string, data []byte) {
		if method != "tools/list" {
			return
		}
		var envelope struct {
			Result struct {
				Tools []map[string]any `json:"tools"`
			} `json:"result"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return
		}
		for _, tool := range envelope.Result.Tools {
			name, _ := tool["name"].(string)
			execution, _ := tool["execution"].(map[string]any)
			if name != "" && execution["taskSupport"] == "required" {
				requiredTaskTools[name] = true
			}
		}
	}
	session, err := connect(ctx, clientVersion, baseClient, url, nil, observe)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer func() { _ = session.Close() }()
	if init := session.InitializeResult(); init == nil || init.ProtocolVersion != "2025-11-25" {
		negotiated := ""
		if init != nil {
			negotiated = init.ProtocolVersion
		}
		return nil, fmt.Errorf("MCP negotiated protocol revision %q; openbindings.mcp@1 accepts only 2025-11-25", negotiated)
	}

	result := &discovery{RequiredTaskTools: requiredTaskTools}

	initResult := session.InitializeResult()
	if initResult != nil {
		result.ServerInfo = initResult.ServerInfo
	}

	if initResult != nil && initResult.Capabilities.Tools != nil {
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list tools: %w", err)
			}
			result.Tools = append(result.Tools, tool)
		}
	}

	if initResult != nil && initResult.Capabilities.Resources != nil {
		for resource, err := range session.Resources(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resources: %w", err)
			}
			result.Resources = append(result.Resources, resource)
		}

		for tmpl, err := range session.ResourceTemplates(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resource templates: %w", err)
			}
			result.ResourceTemplates = append(result.ResourceTemplates, tmpl)
		}
	}

	if initResult != nil && initResult.Capabilities.Prompts != nil {
		for prompt, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list prompts: %w", err)
			}
			result.Prompts = append(result.Prompts, prompt)
		}
	}

	return result, nil
}

// readResourcePooled reads a resource using a pooled session.
func readResourcePooled(ctx context.Context, pool *sessionPool, clientVersion string, url string, uri string, headers map[string]string) (*gomcp.ReadResourceResult, error) {
	s, err := pool.acquire(ctx, clientVersion, url, headers)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer s.release()

	result, err := s.session.ReadResource(ctx, &gomcp.ReadResourceParams{
		URI: uri,
	})
	if err != nil {
		return nil, fmt.Errorf("read resource %q: %w", uri, err)
	}
	return result, nil
}

// getPromptPooled gets a prompt using a pooled session.
func getPromptPooled(ctx context.Context, pool *sessionPool, clientVersion string, url string, promptName string, args map[string]string, headers map[string]string) (*gomcp.GetPromptResult, error) {
	s, err := pool.acquire(ctx, clientVersion, url, headers)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer s.release()

	result, err := s.session.GetPrompt(ctx, &gomcp.GetPromptParams{
		Name:      promptName,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("get prompt %q: %w", promptName, err)
	}
	return result, nil
}

func connect(ctx context.Context, clientVersion string, baseClient *http.Client, url string, headers map[string]string, resultObservers ...func(string, []byte)) (*gomcp.ClientSession, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("MCP source location must be an HTTP or HTTPS URL, got %q", url)
	}

	transport := &gomcp.StreamableClientTransport{Endpoint: url}
	transport.HTTPClient = httpClientWithHeaders(baseClient, headers, nil, resultObservers...)

	client := gomcp.NewClient(clientInfo(clientVersion), nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// httpClientWithHeaders builds the *http.Client the MCP Streamable HTTP
// transport uses. The headerTransport is always installed (even with no auth
// headers) so per-call response capture works for HTTP error mapping and
// Invocation header metadata. A caller-supplied base client contributes its
// Transport as the round-trip base plus its Timeout, redirect policy, and
// cookie jar; a nil base means stdlib defaults. This is the seam for corporate
// proxies, mTLS client certificates, or a custom CA pool. observeSSE, when
// non-nil, receives each JSON-RPC payload carried on a text/event-stream
// response body (see sseObserver); the invoker's session pool wires it to the
// session's raw progress-notification capture, and discovery passes nil.
func httpClientWithHeaders(base *http.Client, headers map[string]string, observeSSE func(data []byte), resultObservers ...func(string, []byte)) *http.Client {
	hc := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	roundTripBase := http.DefaultTransport
	if base != nil {
		hc.Timeout = base.Timeout
		hc.Jar = base.Jar
		if base.Transport != nil {
			roundTripBase = base.Transport
		}
	}
	var observeResult func(string, []byte)
	if len(resultObservers) > 0 {
		observeResult = resultObservers[0]
	}
	hc.Transport = &headerTransport{base: roundTripBase, headers: headers, observeSSE: observeSSE, observeResult: observeResult}
	return hc
}

// headerTransport injects extra HTTP headers into every request and records
// POST responses into the per-call *headerCapture carried by the request
// context (per-call ctx values propagate through the go-mcp SDK's JSON-RPC
// writes into the HTTP request). When observeSSE is set, event-stream
// response bodies are teed through an sseObserver so raw server→client
// JSON-RPC payloads can be observed byte-exactly.
type headerTransport struct {
	base          http.RoundTripper
	headers       map[string]string
	observeSSE    func(data []byte)
	observeResult func(method string, data []byte)
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		if clone.Header.Get(k) == "" {
			clone.Header.Set(k, v)
		}
	}
	requestMethod := ""
	if req.Method == http.MethodPost && clone.Body != nil {
		body, rerr := io.ReadAll(clone.Body)
		_ = clone.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		var requestEnvelope struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &requestEnvelope)
		requestMethod = requestEnvelope.Method
		if omit, _ := req.Context().Value(omitEmptyArgumentsKey{}).(bool); omit {
			body = stripInjectedEmptyArguments(body)
		}
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.ContentLength = int64(len(body))
		clone.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	resp, err := t.base.RoundTrip(clone)
	if err == nil && req.Method == http.MethodPost {
		if hc, ok := req.Context().Value(headerCaptureKey{}).(*headerCapture); ok {
			hc.record(resp)
		}
	}
	if err == nil && resp.Body != nil && isEventStream(resp.Header.Get("Content-Type")) {
		resp.Body = &sseObserver{body: resp.Body, observe: func(data []byte) {
			if t.observeSSE != nil {
				t.observeSSE(data)
			}
			observeRawResult(req.Context(), requestMethod, data, t.observeResult)
		}}
	} else if err == nil && resp.Body != nil && req.Method == http.MethodPost {
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		observeRawResult(req.Context(), requestMethod, body, t.observeResult)
	}
	return resp, err
}

type rawResultCaptureKey struct{}

type rawResultCapture struct {
	mu    sync.Mutex
	value any
}

func (c *rawResultCapture) record(data []byte) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || envelope["result"] == nil {
		return
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(envelope["result"]))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		return
	}
	c.mu.Lock()
	c.value = value
	c.mu.Unlock()
}

func (c *rawResultCapture) result() any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func observeRawResult(ctx context.Context, method string, data []byte, observer func(string, []byte)) {
	if observer != nil {
		observer(method, data)
	}
	if capture, _ := ctx.Value(rawResultCaptureKey{}).(*rawResultCapture); capture != nil {
		capture.record(data)
	}
}

// omitEmptyArgumentsKey marks a per-call context (same propagation mechanics
// as headerCaptureKey: per-call ctx values ride the go-mcp SDK's JSON-RPC
// writes into the HTTP request) whose tools/call request must not carry an
// empty arguments object. openbindings.mcp@1 §9.1 requires an absent input
// value to omit the arguments member ENTIRELY — never arguments: {} — but
// the go-mcp client injects `Arguments = map[string]any{}` whenever the
// typed params carry nil ("avoid sending nil over the wire"), so the member
// has to be stripped at the transport seam.
type omitEmptyArgumentsKey struct{}

// stripInjectedEmptyArguments removes a tools/call params.arguments member
// that is exactly the empty object. Only reached under omitEmptyArgumentsKey,
// which the invoker sets solely when the input value was absent — a caller's
// deliberate empty-object input arrives with the key unset and rides
// unmodified. Numbers pass through as json.Number so the round-trip is
// lossless.
func stripInjectedEmptyArguments(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var msg map[string]any
	if dec.Decode(&msg) != nil || msg["method"] != "tools/call" {
		return body
	}
	params, ok := msg["params"].(map[string]any)
	if !ok {
		return body
	}
	args, ok := params["arguments"].(map[string]any)
	if !ok || len(args) != 0 {
		return body
	}
	delete(params, "arguments")
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return out
}

// isEventStream reports whether a Content-Type declares text/event-stream.
func isEventStream(contentType string) bool {
	mt := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	return strings.EqualFold(mt, "text/event-stream")
}

// sseObserver tees a text/event-stream response body: bytes pass through to
// the go-mcp transport unmodified while complete SSE `data` payloads — each
// one a server→client JSON-RPC message on the MCP Streamable HTTP transport —
// are handed to observe. This is the one seam that sees those messages as raw
// bytes: the go-mcp SDK's typed notification structs erase JSON presence (an
// explicit "total": 0 is indistinguishable from an absent total), which the
// presence-preserving progress values of openbindings.mcp@1 §9.2 require.
type sseObserver struct {
	body    io.ReadCloser
	observe func(data []byte)
	line    []byte // partial line carried across Reads
	data    []byte // accumulated data-field payload of the current event
	hasData bool
}

func (o *sseObserver) Read(p []byte) (int, error) {
	n, err := o.body.Read(p)
	if n > 0 {
		o.scan(p[:n])
	}
	return n, err
}

func (o *sseObserver) Close() error { return o.body.Close() }

// scan incrementally parses the SSE framing (data lines accumulate; a blank
// line dispatches the event). Non-data fields (event, id, retry, comments)
// are ignored; the observed payload is exactly the wire bytes of the data
// field, with multi-line data joined by newlines per the SSE specification.
func (o *sseObserver) scan(b []byte) {
	o.line = append(o.line, b...)
	for {
		i := bytes.IndexByte(o.line, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(o.line[:i], []byte("\r"))
		o.line = o.line[i+1:]
		switch {
		case len(line) == 0:
			if o.hasData {
				o.observe(o.data)
				o.data = nil
				o.hasData = false
			}
		case bytes.HasPrefix(line, []byte("data:")):
			v := line[len("data:"):]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			if o.hasData {
				o.data = append(o.data, '\n')
			}
			o.data = append(o.data, v...)
			o.hasData = true
		}
	}
}
