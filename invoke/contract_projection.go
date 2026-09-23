package invoke

import openbindings "github.com/openbindings/openbindings-go"

// sourceLocation projects an OBI source's optional location onto the
// binding-invoker contract's source, where an absent location is an empty
// string, omitted on the wire.
func sourceLocation(location *string) string {
	if location == nil {
		return ""
	}
	return *location
}

// contractSelector projects a binding's optional selector onto the
// binding-invoker contract. Revision 0.1 of that contract requires a selector
// string, so it cannot carry the absent-selector case the core model keeps
// distinct (§5.3): an absent selector is sent as "", the same spelling as a
// present empty selector. A contract revision that makes selector optional
// removes this projection.
func contractSelector(selector *string) string {
	if selector == nil {
		return ""
	}
	return *selector
}

// samePreparedBinding reports whether two prepared binding descriptors
// describe the same binding: equal fields, with selectors compared by
// presence and value rather than by pointer.
func samePreparedBinding(a, b openbindings.PreparedBindingDescriptor) bool {
	if (a.Selector == nil) != (b.Selector == nil) || (a.Selector != nil && *a.Selector != *b.Selector) {
		return false
	}
	a.Selector, b.Selector = nil, nil
	return a == b
}
