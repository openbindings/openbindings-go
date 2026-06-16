package openbindings

import "sort"

// ResolveOperation resolves an operation by name against an interface, per
// OBI-T-12.
//
// An operation's identifiers are its key plus its Aliases; together they form
// one flat namespace in which key and alias matches are equally authoritative.
// OBI-D-04 makes that namespace document-unique, so a name resolves to at most
// one operation. Key matches are NOT privileged over alias matches: a name that
// is some operation's native key always belongs to that operation (a different
// operation cannot also carry it as an alias without violating OBI-D-04).
//
// It returns the resolved operation's canonical key, the operation, and true on
// a match; the zero values and false otherwise. Binding selection MUST use the
// returned key, not the name the caller looked up by.
func ResolveOperation(iface *Interface, name string) (string, Operation, bool) {
	if iface == nil {
		return "", Operation{}, false
	}

	// Direct key match. By OBI-D-04 a key is never also another operation's
	// alias, so this is authoritative when it hits.
	if op, ok := iface.Operations[name]; ok {
		return name, op, true
	}

	// Alias match across the flat namespace.
	for key, op := range iface.Operations {
		for _, a := range op.Aliases {
			if a == name {
				return key, op, true
			}
		}
	}

	return "", Operation{}, false
}

// AllOperationIdentifiers returns the sorted set of every operation identifier
// (keys plus aliases) in the document, for diagnostic "operation not found"
// errors.
func AllOperationIdentifiers(iface *Interface) []string {
	if iface == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for key, op := range iface.Operations {
		seen[key] = struct{}{}
		for _, a := range op.Aliases {
			seen[a] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
