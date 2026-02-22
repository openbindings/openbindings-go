package openbindings

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Supported OpenBindings versions for this SDK.
const (
	MinSupportedVersion = "0.1.0"
	MaxTestedVersion    = "0.1.0"
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

// IsSupportedVersion reports whether the provided OpenBindings version is within the supported range.
func IsSupportedVersion(v string) (bool, error) {
	parsed, err := parseSemverStrict(v)
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

func parseSemverStrict(v string) (semver, error) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) != 3 {
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
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch < 0 {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	return semver{major: major, minor: minor, patch: patch}, nil
}

func compareSemver(a, b semver) int {
	if a.major != b.major {
		return cmp.Compare(a.major, b.major)
	}
	if a.minor != b.minor {
		return cmp.Compare(a.minor, b.minor)
	}
	return cmp.Compare(a.patch, b.patch)
}
