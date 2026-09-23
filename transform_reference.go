package openbindings

import (
	"net/url"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// transformReferenceName returns the transforms entry a named-transform $ref
// names: a same-document fragment whose pointer is exactly
// /transforms/<name> (§7 item 3). URI semantics decode the fragment before it
// is read as a JSON Pointer (RFC 6901 §6); whether it is written in literal
// form is OBI-D-05's concern. problem states why ref names no entry.
func transformReferenceName(ref string) (name, problem string) {
	if !strings.HasPrefix(ref, "#") {
		return "", "must be a same-document fragment of the form #/transforms/<name>"
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", "is not a URI reference"
	}
	tokens, ok := jsonpointer.Parse(parsed.Fragment)
	switch {
	case ok:
	case !strings.HasPrefix(parsed.Fragment, "/"):
		return "", "is not a JSON Pointer fragment: a pointer begins with / (RFC 6901), as #/transforms/<name>"
	default:
		return "", "is not a JSON Pointer fragment: ~ must be followed by 0 or 1 (RFC 6901)"
	}
	if len(tokens) != 2 || tokens[0] != "transforms" {
		return "", "must name one transforms entry, as #/transforms/<name>"
	}
	return tokens[1], ""
}
