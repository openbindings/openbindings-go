// Package obishape tells a fetched JSON response that is not an OBI document
// apart from an OBI document, before the document is parsed and validated.
package obishape

// LooksLikeOBI is a cheap shape probe: it checks for an "openbindings" string
// and an "operations" object, and validates nothing.
func LooksLikeOBI(v map[string]any) bool {
	_, hasVersion := v["openbindings"].(string)
	_, hasOperations := v["operations"].(map[string]any)
	return hasVersion && hasOperations
}
