package openbindings

// ExecuteSource identifies the binding source for execution.
type ExecuteSource struct {
	Format   string `json:"format"`
	Location string `json:"location,omitempty"`
	Content  any    `json:"content,omitempty"`
}

// BindingExecutionInput is the input for executing a binding against a format-specific source.
type BindingExecutionInput struct {
	Source  ExecuteSource  `json:"source"`
	Ref     string         `json:"ref"`
	Input   any            `json:"input,omitempty"`
	Context *BindingContext `json:"context,omitempty"`
}

// OperationExecutionInput is the input for executing an OBI operation.
// The OperationExecutor resolves the binding internally.
type OperationExecutionInput struct {
	Interface *Interface      `json:"interface"`
	Operation string          `json:"operation"`
	Input     any             `json:"input,omitempty"`
	Context   *BindingContext `json:"context,omitempty"`
}

// ExecuteOutput is the result of an operation execution.
type ExecuteOutput struct {
	Output     any           `json:"output,omitempty"`
	Status     int           `json:"status,omitempty"`
	DurationMs int64         `json:"durationMs,omitempty"`
	Error      *ExecuteError `json:"error,omitempty"`
}

// CreateSource describes a binding source for interface creation.
type CreateSource struct {
	Format         string `json:"format"`
	Name           string `json:"name,omitempty"`
	Location       string `json:"location,omitempty"`
	Content        any    `json:"content,omitempty"`
	OutputLocation string `json:"outputLocation,omitempty"`
	Embed          bool   `json:"embed,omitempty"`
	Description    string `json:"description,omitempty"`
}

// CreateInput is the input for creating an OpenBindings interface from format-specific sources.
type CreateInput struct {
	OpenBindingsVersion string         `json:"openbindingsVersion,omitempty"`
	Sources             []CreateSource `json:"sources,omitempty"`
	Name                string         `json:"name,omitempty"`
	Version             string         `json:"version,omitempty"`
	Description         string         `json:"description,omitempty"`
}

// BindingContext holds runtime context for a binding execution, produced by a
// binding context provider and passed to the format provider by the orchestrator.
type BindingContext struct {
	Credentials *Credentials      `json:"credentials,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Cookies     map[string]string `json:"cookies,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
}

// Credentials holds well-known credential fields for binding execution.
type Credentials struct {
	BearerToken string            `json:"bearerToken,omitempty"`
	APIKey      string            `json:"apiKey,omitempty"`
	Basic       *BasicCredentials `json:"basic,omitempty"`
	Custom      map[string]any    `json:"custom,omitempty"`
}

// BasicCredentials holds username/password credentials.
type BasicCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// StreamEvent represents a single event received from a streaming subscription.
type StreamEvent struct {
	Data  any           `json:"data,omitempty"`
	Error *ExecuteError `json:"error,omitempty"`
}

// ExecuteError represents a structured execution error.
type ExecuteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *ExecuteError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
