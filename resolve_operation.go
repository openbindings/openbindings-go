package openbindings

import "slices"

// ResolveOperation resolves an operation by name against an interface, per
// OBI-T-12.
//
// An operation's identifiers are its key plus its Aliases; together they form
// one flat namespace in which key and alias matches are equally authoritative.
// OBI-D-04 makes that namespace document-unique, so in a conformant document
// a name resolves to at most one operation. A name that several operations
// carry, in a document that violates OBI-D-04, names no one operation and does
// not resolve: no match is privileged over another.
//
// It returns the resolved operation's canonical key, the operation, and true on
// a match; the zero values and false otherwise. Binding selection MUST use the
// returned key, not the name the caller looked up by.
//
// It reads the model as it is and does not check the declared version: a
// caller interpreting a document it has not validated or parsed refuses an
// unsupported version itself (OBI-T-04), as ParseDocument, Validate, and
// CompileOperationSchema do.
func ResolveOperation(iface *Interface, name string) (string, Operation, bool) {
	if iface == nil {
		return "", Operation{}, false
	}
	var matches []string
	for key, operation := range iface.Operations {
		if key == name || slices.Contains(operation.Aliases, name) {
			matches = append(matches, key)
		}
	}
	if len(matches) != 1 {
		return "", Operation{}, false
	}
	return matches[0], iface.Operations[matches[0]], true
}
