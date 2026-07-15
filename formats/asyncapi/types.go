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
	Host        string                `json:"host" yaml:"host"`
	Protocol    string                `json:"protocol" yaml:"protocol"`
	PathName    string                `json:"pathname,omitempty" yaml:"pathname,omitempty"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Tags        []tag                 `json:"tags,omitempty" yaml:"tags,omitempty"`
	Security    []securityRequirement `json:"security,omitempty" yaml:"security,omitempty"`
}

type channel struct {
	Address     string               `json:"address,omitempty" yaml:"address,omitempty"`
	Messages    map[string]message   `json:"messages,omitempty" yaml:"messages,omitempty"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Servers     []serverRef          `json:"servers,omitempty" yaml:"servers,omitempty"`
	Parameters  map[string]parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Ref         string               `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type asyncOperation struct {
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
	Security    []securityRequirement `json:"security,omitempty" yaml:"security,omitempty"`
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
	SecuritySchemes map[string]securityScheme `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}
