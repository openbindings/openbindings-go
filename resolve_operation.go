package openbindings

import "slices"

// ResolveOperation resolves a name to the operation it identifies: a name
// identifies an operation exactly when it equals the operation's key or one
// of its aliases (§5.1, Aliases).
//
// An operation's identifiers are its key plus its Aliases; together they form
// one flat namespace in which key and alias matches are equally authoritative.
// OBI-05 makes that namespace document-unique, so in a conformant document
// a name resolves to at most one operation. A name that several operations
// carry, in a document that violates OBI-05, names no one operation and does
// not resolve: no match is privileged over another. Matching is exact: no
// trimming, case-folding, or approximation.
//
// It returns the resolved operation's key, the operation, and true on a
// match; the zero values and false otherwise, and for a nil Document. A
// caller finding the operation's bindings finds them by the returned key
// (OperationBindings), not by the name it looked up.
//
// It reads the model as it is and does not check the declared version: a
// caller interpreting a document it has not validated or parsed refuses an
// unsupported version itself (CheckVersion), as ParseDocument,
// Validate, and ValueContractCompiler.Resolve do.
func (d *Document) ResolveOperation(name string) (string, Operation, bool) {
	if d == nil {
		return "", Operation{}, false
	}
	var matches []string
	for key, operation := range d.Operations {
		if key == name || slices.Contains(operation.Aliases, name) {
			matches = append(matches, key)
		}
	}
	if len(matches) != 1 {
		return "", Operation{}, false
	}
	return matches[0], d.Operations[matches[0]], true
}

// OperationBindings returns the keys of an operation's bindings: the bindings
// whose Operation is key, the operation's key (§5.1, Aliases), sorted. The
// order is for presentation only; it is not the author's preference
// (Binding.Preference), and choosing among the bindings is the caller's.
//
// It finds by key alone: an alias finds nothing, so a caller holding a name
// resolves it first (ResolveOperation). A key that is not an operation's key,
// an operation no binding realizes, and a nil Document give nil. Like
// ResolveOperation, it does not check the declared version.
func (d *Document) OperationBindings(key string) []string {
	if d == nil {
		return nil
	}
	if _, isOperation := d.Operations[key]; !isOperation {
		return nil
	}
	var keys []string
	for bindingKey, binding := range d.Bindings {
		if binding.Operation == key {
			keys = append(keys, bindingKey)
		}
	}
	slices.Sort(keys)
	return keys
}
