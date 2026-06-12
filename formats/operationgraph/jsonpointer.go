package operationgraph

import (
	"fmt"
	"strconv"
	"strings"
)

// resolveRef resolves a binding ref against an operation-graph source
// document. Per the format spec, the ref MUST be a JSON Pointer (RFC 6901)
// fragment: a leading "#" followed by a Pointer. "#" alone resolves to the
// whole document; bare graph keys are not accepted.
//
// Errors distinguish a malformed ref (errInvalidRef) from a Pointer that does
// not resolve (errRefNotFound).
func resolveRef(doc any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#") {
		return nil, &refError{invalid: true, msg: fmt.Sprintf("ref %q is not a JSON Pointer fragment (must start with '#'; bare graph keys are not accepted)", ref)}
	}
	pointer := ref[1:]
	if pointer == "" {
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, &refError{invalid: true, msg: fmt.Sprintf("ref %q carries a malformed JSON Pointer (must be empty or start with '/')", ref)}
	}
	cur := doc
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok {
				return nil, &refError{msg: fmt.Sprintf("ref %q does not resolve: no member %q", ref, token)}
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, &refError{msg: fmt.Sprintf("ref %q does not resolve: bad array index %q", ref, token)}
			}
			cur = v[idx]
		default:
			return nil, &refError{msg: fmt.Sprintf("ref %q does not resolve: %q addresses into a non-container", ref, token)}
		}
	}
	return cur, nil
}

// refError reports a ref resolution failure; invalid distinguishes malformed
// refs (ERR_INVALID_REF) from well-formed Pointers that miss (ERR_REF_NOT_FOUND).
type refError struct {
	invalid bool
	msg     string
}

func (e *refError) Error() string { return e.msg }
