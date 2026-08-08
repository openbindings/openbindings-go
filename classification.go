package openbindings

// classifiedError is retained as the common constructor for SDK-originated
// invocation failures. The name is historical: OpenBindings no longer assigns
// every error to a closed cross-protocol category or derives retry policy from
// its code.
func classifiedError(code, message string) *InvocationError {
	return &InvocationError{Code: code, Message: message}
}
