package openbindings

import (
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// transformReferenceName returns the transforms entry a named-transform $ref
// names: a same-document fragment whose pointer is exactly
// /transforms/<name> (§7 item 3). problem states why ref is not one.
func transformReferenceName(ref string) (name, problem string) {
	const prefix = "#/transforms/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "must be a same-document fragment of the form #/transforms/<name>"
	}
	tokens, ok := jsonpointer.Parse(strings.TrimPrefix(ref, "#"))
	if !ok {
		return "", "is not a JSON Pointer fragment: ~ must be followed by 0 or 1 (RFC 6901)"
	}
	if len(tokens) != 2 || tokens[1] == "" {
		return "", "must name one transforms entry, as #/transforms/<name>"
	}
	return tokens[1], ""
}
