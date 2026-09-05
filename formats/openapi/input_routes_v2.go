package openapi

import (
	"encoding/json"
	"sort"
	"strings"
)

// These helpers render native correspondence facts into ordinary Core
// JSONata. They intentionally know nothing about OpenAPI declaration planning.
func quotedJSONata(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jsonataLookup(field string) string {
	return "$lookup($," + quotedJSONata(field) + ")"
}

func jsonataObject(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, quotedJSONata(key)+":"+jsonataLookup(fields[key]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

const jsonataUndefined = `$lookup({},"__openbindings_absent")`
