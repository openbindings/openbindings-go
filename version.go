package openbindings

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
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
// decision. A malformed (non-SemVer) v is refused and returns a parse error.
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
	if pre, err := IsUnsupportedPrerelease(v); err != nil {
		return "", false, err
	} else if pre {
		return fmt.Sprintf("document declares version %q, a pre-release this implementation does not support", v), true, nil
	}
	return "", false, nil
}

// IsHigherMajorOrPre1MinorThanMaxTested reports whether v is "higher" than the SDK's MaxTestedVersion
// in the sense OBI-T-04 mandates refusal:
//   - Strictly higher major version, OR
//   - While the SDK's MaxTestedVersion is pre-1.0 (major == 0), strictly higher minor version.
//
// Returns an error if v cannot be parsed as a SemVer 2.0.0 string.
func IsHigherMajorOrPre1MinorThanMaxTested(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return false, err
	}
	if parsed.major > maxTestedSemver.major {
		return true, nil
	}
	if maxTestedSemver.major == 0 && parsed.major == 0 && parsed.minor > maxTestedSemver.minor {
		return true, nil
	}
	return false, nil
}

// IsLowerThanMinSupported reports whether v falls below the SDK's
// MinSupportedVersion in the sense OBI-T-04 mandates refusal — the downward
// half of the rule: a version outside the supported range in either
// direction is refused rather than processed under the wrong rules (pre-1.0
// minors may change field semantics both ways). Mirrors the upward rule's
// granularity: lower major always refuses; a lower minor refuses only while
// the floor is pre-1.0 (post-1.0 minors are additive, so an older minor
// within a supported major reads safely). Patch is never a refusal trigger.
func IsLowerThanMinSupported(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return false, err
	}
	if parsed.major < minSupportedSemver.major {
		return true, nil
	}
	if minSupportedSemver.major == 0 && parsed.major == 0 && parsed.minor < minSupportedSemver.minor {
		return true, nil
	}
	return false, nil
}

// IsUnsupportedPrerelease reports whether v carries a pre-release identifier
// that this SDK does not declare support for. Per OBI-T-04 and §11.1, a tool
// MUST NOT accept a prerelease unless it declares support for that specific
// prerelease; "declares support" means the prerelease falls within this SDK's
// supported range [MinSupportedVersion, MaxTestedVersion]. A prerelease sorts
// below its release, so against a non-prerelease MaxTestedVersion no prerelease
// is in range. Non-prerelease versions and build metadata are never flagged.
//
// Returns an error if v cannot be parsed as a SemVer 2.0.0 string.
func IsUnsupportedPrerelease(v string) (bool, error) {
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

// semver represents a parsed Semantic Versioning 2.0.0 value.
//
// Build metadata is ignored for precedence comparison per SemVer 2.0.0 §10.
type semver struct {
	major      int
	minor      int
	patch      int
	preRelease []string // empty if no pre-release; otherwise the dot-separated identifiers
	build      string   // raw build metadata; informational only
}

// semverPattern is the official SemVer 2.0.0 regex from semver.org.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// IsValidSemver reports whether v is a valid Semantic Versioning 2.0.0 string.
func IsValidSemver(v string) bool {
	return semverPattern.MatchString(strings.TrimSpace(v))
}

func parseSemverStrict(v string) (semver, error) {
	v = strings.TrimSpace(v)
	m := semverPattern.FindStringSubmatch(v)
	if m == nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	out := semver{major: major, minor: minor, patch: patch}
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
	if c := cmp.Compare(a.major, b.major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(a.patch, b.patch); c != 0 {
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
		ai, aIsNum := preReleaseIdentifierAsInt(a.preRelease[i])
		bi, bIsNum := preReleaseIdentifierAsInt(b.preRelease[i])
		switch {
		case aIsNum && bIsNum:
			if c := cmp.Compare(ai, bi); c != 0 {
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

// preReleaseIdentifierAsInt returns (n, true) if the identifier is a numeric identifier
// (per SemVer 2.0.0: digits only, no leading zero unless the identifier is just "0").
func preReleaseIdentifierAsInt(id string) (int, bool) {
	if id == "" {
		return 0, false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, false
	}
	return n, true
}
