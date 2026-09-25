package openbindings

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SupportedVersions states the specification versions this SDK supports
// (§8.1): every release of the 0.2 line, and no prerelease. IsSupportedVersion
// decides membership.
const SupportedVersions = "0.2.x"

// AuthoringVersion is the specification version to declare in a document
// written with this SDK: the lowest version sufficient for everything the
// document model carries, as §8.1 asks of documents. No function here writes
// it; a producer sets Interface.OpenBindings to it.
const AuthoringVersion = "0.2.0"

// supportedPrereleases lists the prerelease versions this SDK supports, each
// named explicitly: a prerelease is a draft, and supporting its release does
// not imply supporting it (§8.1). There are none; naming one means stating it
// in SupportedVersions' doc as well.
var supportedPrereleases []string

// supportedLine is SupportedVersions parsed: its major version, and while
// pre-1.0 its minor version, which are the release line's own.
var supportedLine semver

func init() {
	line, ok := strings.CutSuffix(SupportedVersions, ".x")
	major, minor, _ := strings.Cut(line, ".")
	if !ok || !isNumericIdentifier(major) || (major == "0") != (minor != "") || minor != "" && !isNumericIdentifier(minor) {
		panic(fmt.Sprintf("openbindings: SupportedVersions %q is not a release line", SupportedVersions))
	}
	supportedLine = semver{major: major, minor: minor}
	for _, prerelease := range supportedPrereleases {
		if parsed, err := parseSemverStrict(prerelease); err != nil || len(parsed.preRelease) == 0 {
			panic(fmt.Sprintf("openbindings: supported prerelease %q is not a SemVer prerelease", prerelease))
		}
	}
	if supported, err := IsSupportedVersion(AuthoringVersion); !supported {
		panic(fmt.Sprintf("openbindings: AuthoringVersion %q is not a supported version: %v", AuthoringVersion, err))
	}
}

// IsSupportedVersion reports whether this SDK interprets a document declaring
// version v rather than refusing it (OBI-T-04): whether v belongs to
// SupportedVersions. A release of the supported line is supported whatever
// its patch version, a prerelease only when it is named explicitly, and
// build metadata is ignored (§8.1). Of well-formed versions, ParseDocument,
// ValidateDocument, Interface.Validate, CompileOperationSchema,
// ValidateOperationInput, and ValidateOperationOutput refuse exactly those it
// reports false for.
//
// A malformed (non-SemVer) v is no version at all: IsSupportedVersion
// returns false and a parse error, while validation reports such a document
// under OBI-D-12 rather than refusing it.
func IsSupportedVersion(v string) (bool, error) {
	_, refused, err := versionRefusal(v)
	if err != nil {
		return false, err
	}
	return !refused, nil
}

// versionRefusal is the single OBI-T-04 decision that IsSupportedVersion and
// every refusing entry point share. When this SDK refuses a document
// declaring version v it returns (msg, true, nil), where msg is the
// diagnostic core to which callers add the "openbindings:" prefix and
// "(OBI-T-04)" suffix; when it supports v it returns ("", false, nil). An
// unparseable v yields a non-nil error.
func versionRefusal(v string) (msg string, refused bool, err error) {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		return "", false, err
	}
	if len(parsed.preRelease) > 0 && slices.ContainsFunc(supportedPrereleases, func(p string) bool {
		named, err := parseSemverStrict(p)
		return err == nil && compareSemver(parsed, named) == 0
	}) {
		return "", false, nil
	}
	switch order := compareReleaseLine(parsed); {
	case order > 0:
		return fmt.Sprintf("document declares version %q, newer than the release line this implementation supports (%s)", v, SupportedVersions), true, nil
	case order < 0:
		return fmt.Sprintf("document declares version %q, older than the release line this implementation supports (%s)", v, SupportedVersions), true, nil
	case len(parsed.preRelease) > 0:
		return fmt.Sprintf("document declares version %q, a pre-release this implementation does not support", v), true, nil
	}
	return "", false, nil
}

// compareReleaseLine orders v's release line against the supported one: by
// major version, and while pre-1.0 by minor version, since pre-1.0 minors MAY
// break (§8.1). A patch version never moves a version out of its line.
func compareReleaseLine(v semver) int {
	if order := compareNumeric(v.major, supportedLine.major); order != 0 || supportedLine.major != "0" {
		return order
	}
	return compareNumeric(v.minor, supportedLine.minor)
}

// semver represents a parsed Semantic Versioning 2.0.0 value. Its numeric
// identifiers are kept as their digits: SemVer bounds no number, so none is
// converted to a machine integer that could overflow. Build metadata is not
// kept: it takes no part in precedence (SemVer 2.0.0 §10).
type semver struct {
	major      string
	minor      string
	patch      string
	preRelease []string // empty if no pre-release; otherwise the dot-separated identifiers
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
