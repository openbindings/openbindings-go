package formattoken

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// FormatToken is a normalized `<name>@<version>` token used throughout OpenBindings tooling.
type FormatToken struct {
	// Name is normalized to lowercase.
	Name string
	// Version is preserved as-is except where callers apply additional normalization.
	Version string
}

func (t FormatToken) String() string {
	if t.Name == "" || t.Version == "" {
		return ""
	}
	return t.Name + "@" + t.Version
}

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]*@[A-Za-z0-9][A-Za-z0-9.\-]*$`)

// Parse parses a `<name>@<version>` token and normalizes the name to lowercase.
func Parse(s string) (FormatToken, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return FormatToken{}, errors.New("format token: empty")
	}
	if !tokenRe.MatchString(s) {
		return FormatToken{}, fmt.Errorf("format token: invalid %q", s)
	}
	at := strings.LastIndexByte(s, '@')
	name := strings.ToLower(s[:at])
	ver := s[at+1:]
	return FormatToken{Name: name, Version: ver}, nil
}

// IsFormatToken reports whether s is a syntactically valid `<name>@<version>` token.
func IsFormatToken(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// Normalize normalizes a token string by lowercasing the name and returning `name@version`.
func Normalize(s string) (string, error) {
	t, err := Parse(s)
	if err != nil {
		return "", err
	}
	return t.String(), nil
}

// IsOpenBindings reports whether the token is an OpenBindings token (name == "openbindings").
func IsOpenBindings(t FormatToken) bool {
	return t.Name == "openbindings"
}
