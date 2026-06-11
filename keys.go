package openbindings

import (
	"fmt"
	"regexp"
	"strings"
)

var nonKeyChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeKey converts a name to a valid OBI operation key by replacing
// non-alphanumeric characters (except '.', '_', '-') with underscores. The
// result always matches the OBI-D-04 identifier pattern
// ^[A-Za-z_][A-Za-z0-9_.-]*$: keys that would start with a digit, '.', or
// '-' are prefixed with an underscore.
func SanitizeKey(name string) string {
	key := nonKeyChars.ReplaceAllString(name, "_")
	key = strings.Trim(key, "_")
	if key == "" {
		return "unnamed"
	}
	// OBI-D-04 requires the first character to be a letter or underscore.
	c := key[0]
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_') {
		key = "_" + key
	}
	return key
}

// UniqueKey returns key unchanged if it's not in used; otherwise it appends
// _2, _3, ... until a free slot is found. Use this when you only need simple
// numeric deduplication without contextual disambiguation.
func UniqueKey(key string, used map[string]bool) string {
	if !used[key] {
		return key
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", key, i)
		if !used[candidate] {
			return candidate
		}
	}
}

// ResolveKeyCollision returns a unique key, first attempting to disambiguate
// by prefixing with a sanitized entityType (e.g. service name, resource kind),
// then falling back to numeric suffixes. Use this when the caller has
// contextual information that can produce a more meaningful unique key.
func ResolveKeyCollision(key string, entityType string, used map[string]string) string {
	if _, taken := used[key]; !taken {
		return key
	}
	candidate := SanitizeKey(entityType) + "_" + key
	if _, taken := used[candidate]; !taken {
		return candidate
	}
	for i := 2; ; i++ {
		numbered := fmt.Sprintf("%s_%d", candidate, i)
		if _, taken := used[numbered]; !taken {
			return numbered
		}
	}
}
