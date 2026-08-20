package openbindings

// BindingSpecInfo describes a binding specification supported by an
// invoker, by exact identifier.
type BindingSpecInfo struct {
	BindingSpec string `json:"bindingSpec"`
	Description string `json:"description,omitempty"`
}
