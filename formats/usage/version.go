package usage

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Supported Usage spec versions for this SDK.
const (
	MinSupportedVersion = "2.0.0"
	MaxTestedVersion    = "2.13.1"
)

// Parsed constants, validated at init time.
var (
	minSupportedSemver semver
	maxTestedSemver    semver
)

func init() {
	var err error
	minSupportedSemver, err = parseSemverLoose(MinSupportedVersion)
	if err != nil {
		panic(fmt.Sprintf("usage: invalid MinSupportedVersion constant %q: %v", MinSupportedVersion, err))
	}
	maxTestedSemver, err = parseSemverLoose(MaxTestedVersion)
	if err != nil {
		panic(fmt.Sprintf("usage: invalid MaxTestedVersion constant %q: %v", MaxTestedVersion, err))
	}
}

// SupportedRange returns the minimum and maximum Usage spec versions supported by this SDK.
func SupportedRange() (min, max string) {
	return MinSupportedVersion, MaxTestedVersion
}

// IsSupportedVersion reports whether the provided Usage spec version is within the supported range.
func IsSupportedVersion(v string) (bool, error) {
	parsed, err := parseSemverLoose(v)
	if err != nil {
		return false, err
	}
	return compareSemver(parsed, minSupportedSemver) >= 0 && compareSemver(parsed, maxTestedSemver) <= 0, nil
}

type semver struct {
	major int
	minor int
	patch int
}

// parseSemverLoose accepts MAJOR.MINOR and MAJOR.MINOR.PATCH; missing patch defaults to 0.
func parseSemverLoose(v string) (semver, error) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) != 2 && len(parts) != 3 {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	patch := 0
	if len(parts) == 3 {
		var err error
		patch, err = strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return semver{}, fmt.Errorf("invalid semver: %q", v)
		}
	}
	return semver{major: major, minor: minor, patch: patch}, nil
}

func compareSemver(a, b semver) int {
	if c := cmp.Compare(a.major, b.major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.minor, b.minor); c != 0 {
		return c
	}
	return cmp.Compare(a.patch, b.patch)
}
