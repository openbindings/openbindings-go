package asyncapi

// Document represents an AsyncAPI 3.0 document.
// Only the fields needed for OpenBindings conversion are modeled.
type Document struct {
	AsyncAPI   string                `json:"asyncapi" yaml:"asyncapi"`
	Info       Info                  `json:"info" yaml:"info"`
	Servers    map[string]Server     `json:"servers,omitempty" yaml:"servers,omitempty"`
	Channels   map[string]Channel    `json:"channels,omitempty" yaml:"channels,omitempty"`
	Operations map[string]Operation  `json:"operations,omitempty" yaml:"operations,omitempty"`
	Components *Components           `json:"components,omitempty" yaml:"components,omitempty"`
}

type Info struct {
	Title       string `json:"title" yaml:"title"`
	Version     string `json:"version" yaml:"version"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Server struct {
	Host        string                    `json:"host" yaml:"host"`
	Protocol    string                    `json:"protocol" yaml:"protocol"`
	PathName    string                    `json:"pathname,omitempty" yaml:"pathname,omitempty"`
	Description string                    `json:"description,omitempty" yaml:"description,omitempty"`
	Tags        []Tag                     `json:"tags,omitempty" yaml:"tags,omitempty"`
	Security    []map[string][]string     `json:"security,omitempty" yaml:"security,omitempty"`
}

type Channel struct {
	Address     string               `json:"address,omitempty" yaml:"address,omitempty"`
	Messages    map[string]Message   `json:"messages,omitempty" yaml:"messages,omitempty"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Servers     []ServerRef          `json:"servers,omitempty" yaml:"servers,omitempty"`
	Parameters  map[string]Parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Ref         string               `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type Operation struct {
	Action      string                    `json:"action" yaml:"action"`
	Channel     ChannelRef                `json:"channel" yaml:"channel"`
	Summary     string                    `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string                    `json:"description,omitempty" yaml:"description,omitempty"`
	Messages    []MessageRef              `json:"messages,omitempty" yaml:"messages,omitempty"`
	Tags        []Tag                     `json:"tags,omitempty" yaml:"tags,omitempty"`
	Reply       *OperationReply           `json:"reply,omitempty" yaml:"reply,omitempty"`
	Security    []map[string][]string     `json:"security,omitempty" yaml:"security,omitempty"`
}

type OperationReply struct {
	Channel  *ChannelRef  `json:"channel,omitempty" yaml:"channel,omitempty"`
	Messages []MessageRef `json:"messages,omitempty" yaml:"messages,omitempty"`
}

type Message struct {
	Name        string         `json:"name,omitempty" yaml:"name,omitempty"`
	Title       string         `json:"title,omitempty" yaml:"title,omitempty"`
	Summary     string         `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	ContentType string         `json:"contentType,omitempty" yaml:"contentType,omitempty"`
	Payload     map[string]any `json:"payload,omitempty" yaml:"payload,omitempty"`
	Ref         string         `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type ChannelRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type MessageRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type ServerRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
}

type Parameter struct {
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Default     string   `json:"default,omitempty" yaml:"default,omitempty"`
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
}

type Tag struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type SecurityScheme struct {
	Type         string     `json:"type" yaml:"type"`
	Description  string     `json:"description,omitempty" yaml:"description,omitempty"`
	Name         string     `json:"name,omitempty" yaml:"name,omitempty"`
	In           string     `json:"in,omitempty" yaml:"in,omitempty"`
	Scheme       string     `json:"scheme,omitempty" yaml:"scheme,omitempty"`
	BearerFormat string     `json:"bearerFormat,omitempty" yaml:"bearerFormat,omitempty"`
	Flows        *OAuthFlows `json:"flows,omitempty" yaml:"flows,omitempty"`
}

type OAuthFlows struct {
	Implicit          *OAuthFlow `json:"implicit,omitempty" yaml:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty" yaml:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty" yaml:"clientCredentials,omitempty"`
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty" yaml:"authorizationCode,omitempty"`
}

type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty" yaml:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty" yaml:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty" yaml:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

type Components struct {
	Messages        map[string]Message        `json:"messages,omitempty" yaml:"messages,omitempty"`
	Schemas         map[string]any            `json:"schemas,omitempty" yaml:"schemas,omitempty"`
	Channels        map[string]Channel        `json:"channels,omitempty" yaml:"channels,omitempty"`
	SecuritySchemes map[string]SecurityScheme  `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}
