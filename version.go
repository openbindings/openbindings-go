package openbindings

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"
)

// Supported OpenBindings versions for this SDK.
const (
	MinSupportedVersion = "0.2.0"
	MaxTestedVersion    = "0.2.0"
)

// SupportedRange returns the minimum and maximum OpenBindings versions supported by this SDK.
func SupportedRange() (min, max string) {
	return MinSupportedVersion, MaxTestedVersion
}

var (
	minSupportedSemver semver
	maxTestedSemver    semver
)

func init() {
	var err error
	minSupportedSemver, err = parseSemverStrict(MinSupportedVersion)
	if err != nil {
		panic(fmt.Sprintf("openbindings: invalid MinSupportedVersion %q: %v", MinSupportedVersion, err))
	}
	maxTestedSemver, err = parseSemverStrict(MaxTestedVersion)
	if err != nil {
		panic(fmt.Sprintf("openbindings: invalid MaxTestedVersion %q: %v", MaxTestedVersion, err))
	}
}

// IsSupportedVersion reports whether this SDK will ACCEPT (process rather than
// refuse) a document declaring OpenBindings version v — the OBI-T-04 acceptance
// question, not a tested-range membership test. It returns true iff Validate /
// ParseDocument would NOT emit a version refusal for v: a different major is
// refused; while pre-1.0 a different minor is refused; a higher or lower PATCH
// is never a refusal; a prerelease is accepted only when explicitly supported.
// So a 0.2.0 SDK accepts 0.2.1, 0.2.99, etc. and refuses 0.1.x / 0.3.x. This is
// distinct from — and wider than — the maintainer-tested range reported by
// MinSupportedVersion / MaxTestedVersion / SupportedRange: a version can be
// accepted without being inside the tested range.
//
// It shares the single refusal predicate (versionRefusal) that Validate and
// ParseDocument use, so the oracle cannot drift from the actual accept/refuse
// decision. A malformed (non-SemVer) v is no version at all: IsSupportedVersion
// returns false and a parse error, while validation reports such a document
// under OBI-D-12 rather than refusing it.
func IsSupportedVersion(v string) (bool, error) {
	if _, err := parseSemverStrict(v); err != nil {
		return false, err
	}
	_, refused, err := versionRefusal(v)
	if err != nil {
		return false, err
	}
	return !refused, nil
}

// versionRefusal is the single OBI-T-04 accept/refuse evaluation shared by
// Interface.Validate, ParseDocument, and IsSupportedVersion, so the diagnostic
// path and the acceptance oracle consult the same ordered predicate chain and
// cannot diverge. When this SDK MUST refuse to process a document declaring
// SemVer version v it returns (msg, true, nil), where msg is the diagnostic
// core to which callers add the "openbindings:" prefix and "(OBI-T-04)" suffix;
// when the SDK accepts v it returns ("", false, nil). v MUST be well-formed
// SemVer (callers gate on IsValidSemver or the schema pattern first); an
// unparseable v yields a non-nil error.
func versionRefusal(v string) (msg string, refused bool, err error) {
	if higher, err := IsHigherMajorOrPre1MinorThanMaxTested(v); err != nil {
		return "", false, err
	} else if higher {
		return fmt.Sprintf("document declares version %q, newer than the latest version this implementation supports (%s)", v, MaxTestedVersion), true, nil
	}
	if lower, err := IsLowerThanMinSupported(v); err != nil {
		return "", false, err
	} else if lower {
		return fmt.Sprintf("document declares version %q, older than the oldest version this implementation supports (%s)", v, MinSupportedVersion), true, nil
	}
	if pre, err := isUnsupportedPrerelease(v); err != nil {
		return "", false, err
	} else if pre {
		return fmt.Sprintf("document declares version %q, a pre-release this implementation does not support", v), true, nil
	}
	return "", false, nil
}

// IsHigherMajorOrPre1MinorThanMaxTested reports whether v lies above the
// release line this SDK declares support for, one of the conditions under
// which it refuses a document (OBI-T-04). §8.1 leaves the supported set to
// each processor; this SDK's is the release line of MaxTestedVersion, so v is
// above it when it has a higher major version, or, while MaxTestedVersion is
// pre-1.0, a higher minor version.
//
// Returns an error if v cannot be parsed as a SemVer 2.0.0 string.
func IsHigherMajorOrPre1MinorThanMaxTested(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return false, err
	}
	if compareNumeric(parsed.major, maxTestedSemver.major) > 0 {
		return true, nil
	}
	if maxTestedSemver.major == "0" && parsed.major == "0" && compareNumeric(parsed.minor, maxTestedSemver.minor) > 0 {
		return true, nil
	}
	return false, nil
}

// IsLowerThanMinSupported reports whether v lies below the release line this
// SDK declares support for, the other condition under which it refuses a
// document by its version number (OBI-T-04): a lower major version, or, while
// MinSupportedVersion is pre-1.0, a lower minor version (pre-1.0 minors MAY
// break, §8.1). A patch version is never a reason to refuse.
func IsLowerThanMinSupported(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return false, err
	}
	if compareNumeric(parsed.major, minSupportedSemver.major) < 0 {
		return true, nil
	}
	if minSupportedSemver.major == "0" && parsed.major == "0" && compareNumeric(parsed.minor, minSupportedSemver.minor) < 0 {
		return true, nil
	}
	return false, nil
}

// isUnsupportedPrerelease reports whether v carries a pre-release identifier
// that this SDK does not declare support for. Per OBI-T-04 and §8.1, a tool
// MUST NOT accept a prerelease unless it declares support for that specific
// prerelease; "declares support" means the prerelease falls within this SDK's
// supported range [MinSupportedVersion, MaxTestedVersion]. A prerelease sorts
// below its release, so against a non-prerelease MaxTestedVersion no prerelease
// is in range. Non-prerelease versions and build metadata are never flagged.
//
// Returns an error if v cannot be parsed as a SemVer 2.0.0 string.
func isUnsupportedPrerelease(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return false, err
	}
	if len(parsed.preRelease) == 0 {
		return false, nil
	}
	inRange := compareSemver(parsed, minSupportedSemver) >= 0 && compareSemver(parsed, maxTestedSemver) <= 0
	return !inRange, nil
}

// semver represents a parsed Semantic Versioning 2.0.0 value. Its numeric
// identifiers are kept as their digits: SemVer bounds no number, so none is
// converted to a machine integer that could overflow.
//
// Build metadata is ignored for precedence comparison per SemVer 2.0.0 §10.
type semver struct {
	major      string
	minor      string
	patch      string
	preRelease []string // empty if no pre-release; otherwise the dot-separated identifiers
	build      string   // raw build metadata; informational only
}

// semverPattern is the official SemVer 2.0.0 regex from semver.org.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// IsValidSemver reports whether v is exactly a Semantic Versioning 2.0.0
// string. Surrounding whitespace is not part of the grammar, so " 0.2.0" is
// not valid.
func IsValidSemver(v string) bool {
	return semverPattern.MatchString(v)
}

func parseSemverStrict(v string) (semver, error) {
	m := semverPattern.FindStringSubmatch(v)
	if m == nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	out := semver{major: m[1], minor: m[2], patch: m[3]}
	if m[4] != "" {
		out.preRelease = strings.Split(m[4], ".")
	}
	if m[5] != "" {
		out.build = m[5]
	}
	return out, nil
}

// compareSemver implements SemVer 2.0.0 precedence (§11). Build metadata is ignored.
//
// Returns:
//   - negative if a < b
//   - 0 if a == b (equal precedence)
//   - positive if a > b
func compareSemver(a, b semver) int {
	if c := compareNumeric(a.major, b.major); c != 0 {
		return c
	}
	if c := compareNumeric(a.minor, b.minor); c != 0 {
		return c
	}
	if c := compareNumeric(a.patch, b.patch); c != 0 {
		return c
	}
	// Equal numeric components: a version with pre-release has LOWER precedence
	// than the same normal version without pre-release.
	switch {
	case len(a.preRelease) == 0 && len(b.preRelease) == 0:
		return 0
	case len(a.preRelease) == 0:
		return 1
	case len(b.preRelease) == 0:
		return -1
	}
	// Both have pre-release: compare identifiers left-to-right.
	for i := 0; i < len(a.preRelease) && i < len(b.preRelease); i++ {
		aIsNum := isNumericIdentifier(a.preRelease[i])
		bIsNum := isNumericIdentifier(b.preRelease[i])
		switch {
		case aIsNum && bIsNum:
			if c := compareNumeric(a.preRelease[i], b.preRelease[i]); c != 0 {
				return c
			}
		case aIsNum:
			// Numeric identifiers always have lower precedence than alphanumerics.
			return -1
		case bIsNum:
			return 1
		default:
			if c := cmp.Compare(a.preRelease[i], b.preRelease[i]); c != 0 {
				return c
			}
		}
	}
	// All compared identifiers equal: shorter set has lower precedence.
	return cmp.Compare(len(a.preRelease), len(b.preRelease))
}

// isNumericIdentifier reports whether a SemVer identifier is numeric: digits
// only. The grammar already forbids a leading zero in one.
func isNumericIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareNumeric compares two SemVer numeric identifiers of any size. Neither
// has a leading zero, so the longer is the larger, and equal lengths compare
// digit by digit.
func compareNumeric(a, b string) int {
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
