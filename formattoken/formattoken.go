package formattoken

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]*$`)

// IsValidName reports whether s is a valid versionless format name (e.g., "grpc").
// Per spec, formats without a meaningful version MAY omit @<version>.
func IsValidName(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.Contains(s, "@") && nameRe.MatchString(s)
}

// IsOpenBindings reports whether the token is an OpenBindings token (name == "openbindings").
func IsOpenBindings(t FormatToken) bool {
	return t.Name == "openbindings"
}

// RangeKind describes the type of version constraint in a VersionRange.
type RangeKind int

const (
	RangeVersionless RangeKind = iota
	RangeExact
	RangeCaret
)

// VersionRange represents a version constraint parsed from an executor's format token.
type VersionRange struct {
	Name    string // lowercase
	Kind    RangeKind
	Version string // normalized version string (for Exact)
	Major   int    // for Caret
	Minor   int    // for Caret
	Patch   int    // for Caret
}

// ParseRange parses an executor format token into a VersionRange.
// Tokens may be versionless ("grpc"), exact ("mcp@2025-11-25"), or caret ("openapi@^3.0.0").
func ParseRange(s string) (VersionRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return VersionRange{}, errors.New("format range: empty")
	}

	at := strings.IndexByte(s, '@')
	if at < 0 {
		// Versionless
		name := strings.ToLower(s)
		if !nameRe.MatchString(name) {
			return VersionRange{}, fmt.Errorf("format range: invalid name %q", s)
		}
		return VersionRange{Name: name, Kind: RangeVersionless}, nil
	}

	name := strings.ToLower(s[:at])
	ver := s[at+1:]
	if name == "" || ver == "" {
		return VersionRange{}, fmt.Errorf("format range: invalid %q", s)
	}

	if strings.HasPrefix(ver, "^") {
		// Caret range
		numStr := ver[1:]
		parts := strings.Split(numStr, ".")
		nums := make([]int, 3)
		for i := 0; i < len(parts) && i < 3; i++ {
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				return VersionRange{}, fmt.Errorf("format range: invalid caret version %q", ver)
			}
			nums[i] = n
		}
		return VersionRange{
			Name:  name,
			Kind:  RangeCaret,
			Major: nums[0],
			Minor: nums[1],
			Patch: nums[2],
		}, nil
	}

	// Exact
	return VersionRange{Name: name, Kind: RangeExact, Version: ver}, nil
}

// Matches reports whether sourceToken (an exact format token like "openapi@3.1")
// satisfies the given VersionRange.
func Matches(vr VersionRange, sourceToken string) bool {
	sourceToken = strings.TrimSpace(sourceToken)
	at := strings.IndexByte(sourceToken, '@')

	var srcName, srcVer string
	if at < 0 {
		srcName = strings.ToLower(sourceToken)
	} else {
		srcName = strings.ToLower(sourceToken[:at])
		srcVer = sourceToken[at+1:]
	}

	if srcName != vr.Name {
		return false
	}

	switch vr.Kind {
	case RangeVersionless:
		return srcVer == ""

	case RangeExact:
		if srcVer == "" {
			return false
		}
		return normalizeVersion(vr.Version) == normalizeVersion(srcVer)

	case RangeCaret:
		if srcVer == "" {
			return false
		}
		parts := strings.Split(srcVer, ".")
		nums := make([]int, 3)
		for i := 0; i < len(parts) && i < 3; i++ {
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				return false
			}
			nums[i] = n
		}
		if nums[0] != vr.Major {
			return false
		}
		if nums[1] > vr.Minor {
			return true
		}
		return nums[1] == vr.Minor && nums[2] >= vr.Patch

	default:
		return false
	}
}

// normalizeVersion strips trailing .0 segments from numeric versions for exact comparison.
// Non-numeric versions are returned as-is.
func normalizeVersion(v string) string {
	parts := strings.Split(v, ".")
	// Check all parts are numeric
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return v
		}
	}
	// Strip trailing "0" segments
	for len(parts) > 1 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, ".")
}
