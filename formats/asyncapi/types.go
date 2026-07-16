package asyncapi

// document represents an AsyncAPI 3.0 document.
// Only the fields needed for OpenBindings conversion are modeled.
type document struct {
	AsyncAPI           string                    `json:"asyncapi" yaml:"asyncapi"`
	DefaultContentType string                    `json:"defaultContentType,omitempty" yaml:"defaultContentType,omitempty"`
	Info               info                      `json:"info" yaml:"info"`
	Servers            map[string]server         `json:"servers,omitempty" yaml:"servers,omitempty"`
	Channels           map[string]channel        `json:"channels,omitempty" yaml:"channels,omitempty"`
	Operations         map[string]asyncOperation `json:"operations,omitempty" yaml:"operations,omitempty"`
	Components         *components               `json:"components,omitempty" yaml:"components,omitempty"`
}

type info struct {
	Title       string `json:"title" yaml:"title"`
	Version     string `json:"version" yaml:"version"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type server struct {
	Host        string                    `json:"host" yaml:"host"`
	Protocol    string                    `json:"protocol" yaml:"protocol"`
	PathName    string                    `json:"pathname,omitempty" yaml:"pathname,omitempty"`
	Description string                    `json:"description,omitempty" yaml:"description,omitempty"`
	Variables   map[string]serverVariable `json:"variables,omitempty" yaml:"variables,omitempty"`
	Tags        []tag                     `json:"tags,omitempty" yaml:"tags,omitempty"`
	Security    []securityRequirement     `json:"security,omitempty" yaml:"security,omitempty"`
}

// serverVariable is an AsyncAPI Server Variable Object: a declared `{name}`
// expression in a server's host or pathname, substituted per ASYNC-P-04
// (consumer-supplied value, else the declared default; unresolved is a
// pre-dispatch refusal).
type serverVariable struct {
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
	Default     string   `json:"default,omitempty" yaml:"default,omitempty"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
}

type channel struct {
	Address     string               `json:"address,omitempty" yaml:"address,omitempty"`
	Messages    map[string]message   `json:"messages,omitempty" yaml:"messages,omitempty"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Servers     []serverRef          `json:"servers,omitempty" yaml:"servers,omitempty"`
	Parameters  map[string]parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Bindings    *channelBindings     `json:"bindings,omitempty" yaml:"bindings,omitempty"`
	Ref         string               `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

// channelBindings models the protocol entries of a channel's `bindings`
// object this specification incorporates (§8: bindings are authoritative
// where they speak). Only the websockets binding speaks at channel level in
// revision 1.
type channelBindings struct {
	WS *wsChannelBinding `json:"ws,omitempty" yaml:"ws,omitempty"`
}

// wsChannelBinding is the AsyncAPI WebSockets channel binding: `method`
// selects the upgrade request's method, and `query`/`headers` are Schema
// Objects declaring the upgrade request's query parameters and headers
// (ASYNC-P-02: values supplied like address parameters, any unsatisfied
// required declaration a pre-dispatch refusal).
type wsChannelBinding struct {
	Method  string         `json:"method,omitempty" yaml:"method,omitempty"`
	Query   map[string]any `json:"query,omitempty" yaml:"query,omitempty"`
	Headers map[string]any `json:"headers,omitempty" yaml:"headers,omitempty"`
}

type asyncOperation struct {
	// Ref carries an operations-map entry that is a Reference Object: per
	// ASYNC-D-03 it resolves through the reference before the
	// operation-object test (the artifact's own reference rules). Resolved
	// in place by resolveRefs; a non-empty Ref after resolution is a
	// dangling reference.
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`

	// Action is declared from the DESCRIBED APPLICATION's perspective
	// (AsyncAPI 3.0's own rule): `send` = the application sends to the
	// channel, `receive` = it expects to receive from it. An invocation is
	// the counterparty (ASYNC-P-02): invoking `send` subscribes, invoking
	// `receive` publishes.
	Action      string                `json:"action" yaml:"action"`
	Channel     channelRef            `json:"channel" yaml:"channel"`
	Summary     string                `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Messages    []messageRef          `json:"messages,omitempty" yaml:"messages,omitempty"`
	Tags        []tag                 `json:"tags,omitempty" yaml:"tags,omitempty"`
	Reply       *operationReply       `json:"reply,omitempty" yaml:"reply,omitempty"`
	Bindings    *operationBindings    `json:"bindings,omitempty" yaml:"bindings,omitempty"`
	Security    []securityRequirement `json:"security,omitempty" yaml:"security,omitempty"`
}

// operationBindings models the protocol entries of an operation's `bindings`
// object this specification incorporates. Only the http binding speaks at
// operation level in revision 1.
type operationBindings struct {
	HTTP *httpOperationBinding `json:"http,omitempty" yaml:"http,omitempty"`
}

// httpOperationBinding is the AsyncAPI HTTP operation binding: its `method`
// selects the request method (§8 — authoritative where it speaks; where
// silent, POST for a publish, GET for an SSE subscription).
type httpOperationBinding struct {
	Method string `json:"method,omitempty" yaml:"method,omitempty"`
}

type operationReply struct {
	Channel  *channelRef  `json:"channel,omitempty" yaml:"channel,omitempty"`
	Messages []messageRef `json:"messages,omitempty" yaml:"messages,omitempty"`
}

type message struct {
	Name        string         `json:"name,omitempty" yaml:"name,omitempty"`
	Title       string         `json:"title,omitempty" yaml:"title,omitempty"`
	Summary     string         `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	ContentType string         `json:"contentType,omitempty" yaml:"contentType,omitempty"`
	Payload     map[string]any `json:"payload,omitempty" yaml:"payload,omitempty"`
	Ref         string         `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type channelRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type messageRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type serverRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type parameter struct {
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Default     string   `json:"default,omitempty" yaml:"default,omitempty"`
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
}

type tag struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type securityScheme struct {
	Type         string      `json:"type" yaml:"type"`
	Description  string      `json:"description,omitempty" yaml:"description,omitempty"`
	Name         string      `json:"name,omitempty" yaml:"name,omitempty"`
	In           string      `json:"in,omitempty" yaml:"in,omitempty"`
	Scheme       string      `json:"scheme,omitempty" yaml:"scheme,omitempty"`
	BearerFormat string      `json:"bearerFormat,omitempty" yaml:"bearerFormat,omitempty"`
	Flows        *oauthFlows `json:"flows,omitempty" yaml:"flows,omitempty"`
}

// securityRequirement is one entry in an AsyncAPI 3.0 `security` list. Per
// the AsyncAPI 3.0 spec, `security` on a server or operation is a LIST of
// Security Scheme Objects or Reference Objects — NOT a list of
// requirement-maps keyed by scheme name (that shape is OpenAPI's, and
// AsyncAPI 2.x's). WITHIN one list, satisfying any ONE entry suffices
// (OR; AsyncAPI 3.0 has no AND-conjunction grouping within one entry) —
// but the server's list and the operation's list are CONJUNCTIVE across
// each other (ASYNC-P-07): the targeted server's security applies, and the
// operation's, when declared, applies in addition. An entry is either a
// $ref to a components.securitySchemes definition, or an inline Security
// Scheme Object — both forms are valid per spec.
type securityRequirement struct {
	Ref            string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	securityScheme `yaml:",inline"`
}

type oauthFlows struct {
	Implicit          *oauthFlow `json:"implicit,omitempty" yaml:"implicit,omitempty"`
	Password          *oauthFlow `json:"password,omitempty" yaml:"password,omitempty"`
	ClientCredentials *oauthFlow `json:"clientCredentials,omitempty" yaml:"clientCredentials,omitempty"`
	AuthorizationCode *oauthFlow `json:"authorizationCode,omitempty" yaml:"authorizationCode,omitempty"`
}

type oauthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty" yaml:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty" yaml:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty" yaml:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

type components struct {
	Messages        map[string]message        `json:"messages,omitempty" yaml:"messages,omitempty"`
	Schemas         map[string]any            `json:"schemas,omitempty" yaml:"schemas,omitempty"`
	Channels        map[string]channel        `json:"channels,omitempty" yaml:"channels,omitempty"`
	Operations      map[string]asyncOperation `json:"operations,omitempty" yaml:"operations,omitempty"`
	SecuritySchemes map[string]securityScheme `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}
