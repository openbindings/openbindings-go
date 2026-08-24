package operationgraph

import (
	"fmt"
	"strconv"
	"strings"
)

// resolveSelector resolves a binding selector against an operation-graph source
// document. Per the format spec, the selector MUST be a JSON Pointer (RFC 6901)
// fragment: a leading "#" followed by a Pointer. "#" alone resolves to the
// whole document; bare graph keys are not accepted.
//
// Errors distinguish a malformed selector (errInvalidSelector) from a Pointer that does
// not resolve (errSelectorNotFound).
func resolveSelector(doc any, selector string) (any, error) {
	if !strings.HasPrefix(selector, "#") {
		return nil, &selectorError{invalid: true, msg: fmt.Sprintf("selector %q is not a JSON Pointer fragment (must start with '#'; bare graph keys are not accepted)", selector)}
	}
	pointer := selector[1:]
	if pointer == "" {
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, &selectorError{invalid: true, msg: fmt.Sprintf("selector %q carries a malformed JSON Pointer (must be empty or start with '/')", selector)}
	}
	cur := doc
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok {
				return nil, &selectorError{msg: fmt.Sprintf("selector %q does not resolve: no member %q", selector, token)}
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, &selectorError{msg: fmt.Sprintf("selector %q does not resolve: bad array index %q", selector, token)}
			}
			cur = v[idx]
		default:
			return nil, &selectorError{msg: fmt.Sprintf("selector %q does not resolve: %q addresses into a non-container", selector, token)}
		}
	}
	return cur, nil
}

// selectorError reports a selector resolution failure; invalid distinguishes malformed
// selectors (ERR_INVALID_SELECTOR) from well-formed Pointers that miss (ERR_SELECTOR_NOT_FOUND).
type selectorError struct {
	invalid bool
	msg     string
}

func (e *selectorError) Error() string { return e.msg }
