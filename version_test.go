package openbindings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The declaration and the version this SDK writes agree: documents built
// with the SDK declare a version it supports.
func TestSupportedVersions_AuthoringVersionIsSupported(t *testing.T) {
	if supported, err := IsSupportedVersion(AuthoringVersion); !supported {
		t.Fatalf("AuthoringVersion %s is not supported: %v", AuthoringVersion, err)
	}
	if want := supportedLine.major + "." + supportedLine.minor + ".x"; SupportedVersions != want {
		t.Fatalf("SupportedVersions %q parsed as the line %s", SupportedVersions, want)
	}
}

func TestIsSupportedVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{name: "the authoring version", version: AuthoringVersion, want: true},
		// A release of the supported line is supported whatever its patch.
		{name: "higher patch", version: "0.2.1", want: true},
		{name: "much higher patch", version: "0.2.99", want: true},
		{name: "build metadata is ignored", version: "0.2.0+build.1", want: true},
		{name: "lower minor pre-1", version: "0.1.0", want: false},
		{name: "lower minor, higher patch", version: "0.1.9", want: false},
		{name: "much lower", version: "0.0.1", want: false},
		{name: "higher major", version: "1.0.0", want: false},
		{name: "higher minor pre-1", version: "0.3.0", want: false},
		{name: "a prerelease of a supported release", version: "0.2.0-rc.1", want: false},
		{name: "a prerelease of a later patch", version: "0.2.1-rc.1", want: false},
		{name: "invalid empty", version: "", wantErr: true},
		{name: "invalid 1.0", version: "1.0", wantErr: true},
		{name: "invalid letters", version: "a.b.c", wantErr: true},
		{name: "invalid negative", version: "-1.0.0", wantErr: true},
		{name: "surrounding whitespace is not SemVer", version: " " + AuthoringVersion + " ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsSupportedVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsSupportedVersion(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("IsSupportedVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

// SupportedVersions' doc says the SDK supports no prerelease; naming one
// means saying so there too.
func TestSupportedVersions_StatesEveryPrerelease(t *testing.T) {
	if len(supportedPrereleases) > 0 {
		t.Fatalf("supportedPrereleases names %v: state them in SupportedVersions' doc and update this test", supportedPrereleases)
	}
}

// A prerelease is supported only when named explicitly (§8.1): naming one
// supports it, build metadata aside, and supports nothing else.
func TestIsSupportedVersion_PrereleasesAreNamed(t *testing.T) {
	defer func(saved []string) { supportedPrereleases = saved }(supportedPrereleases)
	supportedPrereleases = []string{"0.3.0-rc.1"}
	for version, want := range map[string]bool{
		"0.3.0-rc.1": true, "0.3.0-rc.1+build.2": true,
		"0.3.0-rc.2": false, "0.3.0": false, "0.2.0-rc.1": false,
	} {
		if got, err := IsSupportedVersion(version); got != want || err != nil {
			t.Errorf("IsSupportedVersion(%q) = %v, %v; want %v", version, got, err, want)
		}
	}
}

// A refusal says which way the version misses the supported line.
func TestVersionRefusal_SaysWhy(t *testing.T) {
	for version, want := range map[string]string{
		"0.3.0":      `document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)`,
		"1.0.0":      `document declares version "1.0.0", newer than the release line this implementation supports (0.2.x)`,
		"0.1.9":      `document declares version "0.1.9", older than the release line this implementation supports (0.2.x)`,
		"0.3.0-rc.1": `document declares version "0.3.0-rc.1", newer than the release line this implementation supports (0.2.x)`,
		"0.2.0-rc.1": `document declares version "0.2.0-rc.1", a pre-release this implementation does not support`,
	} {
		if msg, refused, err := versionRefusal(version); !refused || err != nil || msg != want {
			t.Errorf("%s: %q, %v, %v", version, msg, refused, err)
		}
	}
}

// TestIsSupportedVersion_MatchesValidateAndParseRefusal pins IsSupportedVersion
// to the ACTUAL accept/refuse outcome of ParseDocument and Interface.Validate
// for the same versions, so the acceptance oracle can never drift from the
// paths it is promoted to predict (README, `ob create`). The minimal document
// is otherwise schema-valid, so on a well-formed version any refusal is the
// OBI-T-04 version refusal, tagged "(OBI-T-04)".
func TestIsSupportedVersion_MatchesValidateAndParseRefusal(t *testing.T) {
	doc := func(v string) []byte {
		return []byte(fmt.Sprintf(`{"openbindings": %q, "operations": {}}`, v))
	}
	versions := []string{
		"0.2.0", "0.2.1", "0.2.99", "0.1.0", "0.1.9",
		"0.0.1", "0.3.0", "1.0.0", "0.2.0-rc.1", "0.3.0-rc.1",
	}
	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			accepted, err := IsSupportedVersion(v)
			if err != nil {
				t.Fatalf("IsSupportedVersion(%q) unexpected error: %v", v, err)
			}

			// ParseDocument path.
			_, perr := ParseDocument(doc(v))
			parseRefuses := perr != nil
			if parseRefuses && !strings.Contains(perr.Error(), "(OBI-T-04)") {
				t.Fatalf("ParseDocument(%q) failed for a non-version reason: %v", v, perr)
			}
			if accepted == parseRefuses {
				t.Errorf("drift: IsSupportedVersion(%q)=%v but ParseDocument refuses=%v", v, accepted, parseRefuses)
			}

			// Interface.Validate path: only the version decision is tagged
			// "(OBI-T-04)", so it is isolable from any other shape problems.
			_, verr := (Interface{OpenBindings: v, Operations: map[string]Operation{}}).Validate(ValidateOptions{})
			validateVersionRefuses := verr != nil && strings.Contains(verr.Error(), "(OBI-T-04)")
			if accepted == validateVersionRefuses {
				t.Errorf("drift: IsSupportedVersion(%q)=%v but Validate version-refuses=%v (%v)", v, accepted, validateVersionRefuses, verr)
			}
		})
	}
}

func TestIsValidSemver(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0.0.0", true},
		{"1.2.3", true},
		{"10.20.30", true},
		{"1.0.0-alpha", true},
		{"1.0.0-alpha.1", true},
		{"1.0.0-0.3.7", true},
		{"1.0.0-x.7.z.92", true},
		{"1.0.0+20130313144700", true},
		{"1.0.0-beta+exp.sha.5114f85", true},
		{"  1.2.3  ", false}, // whitespace is not part of the grammar
		{" 1.2.3", false},
		{"1.2.3\n", false},
		{"", false},
		{"1.2", false},
		{"1.2.3.4", false},
		{"a.b.c", false},
		{"01.2.3", false}, // leading zero in numeric component
		{"1.2.3-", false}, // empty pre-release identifier
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := IsValidSemver(c.in); got != c.want {
				t.Errorf("IsValidSemver(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseSemverStrict(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    semver
		wantErr bool
	}{
		{name: "valid 0.1.0", input: "0.1.0", want: semver{major: "0", minor: "1", patch: "0"}},
		{name: "valid 1.2.3", input: "1.2.3", want: semver{major: "1", minor: "2", patch: "3"}},
		{name: "valid large numbers", input: "10.20.30", want: semver{major: "10", minor: "20", patch: "30"}},
		{name: "surrounding whitespace", input: "  1.2.3  ", wantErr: true},
		{name: "valid with prerelease", input: "1.0.0-alpha.1", want: semver{major: "1", minor: "0", patch: "0", preRelease: []string{"alpha", "1"}}},
		{name: "valid with build", input: "1.0.0+exp", want: semver{major: "1", minor: "0", patch: "0"}},
		{name: "valid with pre + build", input: "1.0.0-beta+exp", want: semver{major: "1", minor: "0", patch: "0", preRelease: []string{"beta"}}},
		{name: "empty string", input: "", wantErr: true},
		{name: "too few parts", input: "1.2", wantErr: true},
		{name: "too many parts", input: "1.2.3.4", wantErr: true},
		{name: "non-numeric major", input: "a.1.2", wantErr: true},
		{name: "non-numeric minor", input: "1.b.2", wantErr: true},
		{name: "non-numeric patch", input: "1.2.c", wantErr: true},
		{name: "negative major", input: "-1.2.3", wantErr: true},
		{name: "leading zero major", input: "01.2.3", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSemverStrict(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSemverStrict(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.major != tt.want.major || got.minor != tt.want.minor || got.patch != tt.want.patch {
					t.Errorf("parseSemverStrict(%q) = %+v, want %+v", tt.input, got, tt.want)
				}
				if !slices.Equal(got.preRelease, tt.want.preRelease) {
					t.Errorf("parseSemverStrict(%q).preRelease = %v, want %v", tt.input, got.preRelease, tt.want.preRelease)
				}
			}
		})
	}
}

// Build metadata takes no part in precedence (SemVer 2.0.0 §10).
func TestCompareSemver_BuildMetadataTakesNoPart(t *testing.T) {
	a, errA := parseSemverStrict("1.2.3+exp.a")
	b, errB := parseSemverStrict("1.2.3+exp.b")
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if got := compareSemver(a, b); got != 0 {
		t.Fatalf("compareSemver = %d, want 0", got)
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		name string
		a    semver
		b    semver
		want int
	}{
		{name: "equal versions", a: semver{major: "1", minor: "2", patch: "3"}, b: semver{major: "1", minor: "2", patch: "3"}, want: 0},
		{name: "a major greater", a: semver{major: "2"}, b: semver{major: "1", minor: "9", patch: "9"}, want: 1},
		{name: "a major less", a: semver{major: "1", minor: "9", patch: "9"}, b: semver{major: "2"}, want: -1},
		{name: "a minor greater", a: semver{major: "1", minor: "3"}, b: semver{major: "1", minor: "2", patch: "9"}, want: 1},
		{name: "a minor less", a: semver{major: "1", minor: "2", patch: "9"}, b: semver{major: "1", minor: "3"}, want: -1},
		{name: "a patch greater", a: semver{major: "1", minor: "2", patch: "4"}, b: semver{major: "1", minor: "2", patch: "3"}, want: 1},
		{name: "a patch less", a: semver{major: "1", minor: "2", patch: "3"}, b: semver{major: "1", minor: "2", patch: "4"}, want: -1},
		{name: "zero versions", a: semver{}, b: semver{}, want: 0},
		// SemVer 2.0.0 §11: a version with pre-release has lower precedence than the same normal version.
		{name: "prerelease lower than no prerelease", a: semver{major: "1", preRelease: []string{"alpha"}}, b: semver{major: "1"}, want: -1},
		{name: "no prerelease higher than prerelease", a: semver{major: "1"}, b: semver{major: "1", preRelease: []string{"alpha"}}, want: 1},
		{name: "alpha < beta lex", a: semver{major: "1", preRelease: []string{"alpha"}}, b: semver{major: "1", preRelease: []string{"beta"}}, want: -1},
		{name: "alpha < alpha.1 (shorter < longer)", a: semver{major: "1", preRelease: []string{"alpha"}}, b: semver{major: "1", preRelease: []string{"alpha", "1"}}, want: -1},
		{name: "numeric < alphanumeric prerelease", a: semver{major: "1", preRelease: []string{"1"}}, b: semver{major: "1", preRelease: []string{"alpha"}}, want: -1},
		{name: "numeric prerelease ordering", a: semver{major: "1", preRelease: []string{"1"}}, b: semver{major: "1", preRelease: []string{"2"}}, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if (got > 0) != (tt.want > 0) || (got < 0) != (tt.want < 0) || (got == 0) != (tt.want == 0) {
				t.Errorf("compareSemver(%+v, %+v) = %v, want sign %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// SemVer bounds no number, so a version whose numbers exceed any machine
// integer is compared exactly, and refused or accepted like any other.
func TestVersionNumbersAreUnbounded(t *testing.T) {
	huge := "999999999999999999999999999999"
	for version, want := range map[string]bool{
		huge + ".0.0":      false,
		"0." + huge + ".0": false,
		"0.2." + huge:      true,
		"0.2.0-" + huge:    false,
	} {
		supported, err := IsSupportedVersion(version)
		if err != nil || supported != want {
			t.Errorf("IsSupportedVersion(%q) = %v, %v; want %v", version, supported, err, want)
		}
	}
	if _, _, err := ValidateDocument([]byte(`{"openbindings":"`+huge+`.0.0","operations":{}}`), ValidateOptions{}); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("an oversized major must be refused (OBI-T-04), got %v", err)
	}
	a, _ := parseSemverStrict("1.0.0-" + huge)
	b, _ := parseSemverStrict("1.0.0-" + huge + "0")
	if compareSemver(a, b) >= 0 {
		t.Fatal("numeric pre-release identifiers compare numerically at any size")
	}
}
