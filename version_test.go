package openbindings

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// The declaration and the version this SDK writes agree: documents built
// with the SDK declare a version it supports.
func TestSupportedVersions_AuthoringVersionIsSupported(t *testing.T) {
	if !isValidSemver(AuthoringVersion) || CheckVersion(AuthoringVersion) != nil {
		t.Fatalf("AuthoringVersion %s is not supported: %v", AuthoringVersion, CheckVersion(AuthoringVersion))
	}
	if want := supportedLine.major + "." + supportedLine.minor + ".x"; SupportedVersions != want {
		t.Fatalf("SupportedVersions %q parsed as the line %s", SupportedVersions, want)
	}
}

func TestParseReleaseLine(t *testing.T) {
	for _, declaration := range []string{"0.2.x", "1.0.x", "1.2.x", "10.20.x", "999999999999999999999.888888888888888888888.x"} {
		line, ok := parseReleaseLine(declaration)
		if !ok || line.major+"."+line.minor+".x" != declaration {
			t.Errorf("parseReleaseLine(%q) = %+v, %v", declaration, line, ok)
		}
	}
	for _, declaration := range []string{"", "0.x", "1.x", "1.0", "1.0.0.x", "01.0.x", "1.00.x", "-1.0.x", "1.a.x", "1.0.*", "1.0.x-rc.1", "1.0.x+build", " 1.0.x", "1.0.x\n"} {
		if _, ok := parseReleaseLine(declaration); ok {
			t.Errorf("parseReleaseLine accepted %q", declaration)
		}
	}
}

// Exercise future declarations without changing the versions this SDK ships.
// The public refusal decision and the independently judged corpus declaration
// must both retain the minor, even when that minor is backward-compatible.
func TestCheckVersion_MajorMinorLines(t *testing.T) {
	savedLine, savedPrereleases := supportedLine, supportedPrereleases
	t.Cleanup(func() { supportedLine, supportedPrereleases = savedLine, savedPrereleases })
	supportedPrereleases = nil
	for _, tc := range []struct {
		declaration string
		versions    map[string]bool
	}{
		{"1.0.x", map[string]bool{"1.0.0": true, "1.0.999999999999999999999": true, "1.0.2+build.1": true, "1.0.0-rc.1": false, "1.1.0": false, "0.99.0": false, "2.0.0": false}},
		{"1.10.x", map[string]bool{"1.10.0": true, "1.10.9": true, "1.2.99": false, "1.9.99": false, "1.11.0": false, "1.10.0-rc.1": false}},
		{"10.2.x", map[string]bool{"10.2.0": true, "10.2.1": true, "10.1.99": false, "10.3.0": false, "9.99.0": false, "11.0.0": false}},
	} {
		t.Run(tc.declaration, func(t *testing.T) {
			var ok bool
			supportedLine, ok = parseReleaseLine(tc.declaration)
			if !ok {
				t.Fatal("declaration was rejected")
			}
			declaration := sdkDeclaration()
			for version, supported := range tc.versions {
				if refused := refusedBy(t, version); refused == supported {
					t.Errorf("CheckVersion(%q) refuses %v, want %v", version, refused, !supported)
				}
				if got := declaration.Supports(version); got != supported {
					t.Errorf("declaration.Supports(%q) = %v, want %v", version, got, supported)
				}
			}
		})
	}
}

// refusedBy reports whether CheckVersion refuses v, failing a test whose
// refusal is not the one for v.
func refusedBy(t *testing.T, v string) bool {
	t.Helper()
	err := CheckVersion(v)
	if err == nil {
		return false
	}
	var refusal *VersionRefusalError
	if !errors.As(err, &refusal) || refusal.Version != v {
		t.Fatalf("CheckVersion(%q) = %v, want a *VersionRefusalError for it", v, err)
	}
	return true
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		refused bool
	}{
		{name: "the authoring version", version: AuthoringVersion},
		// A release of the supported line is supported whatever its patch.
		{name: "higher patch", version: "0.2.1"},
		{name: "much higher patch", version: "0.2.99"},
		{name: "build metadata is ignored", version: "0.2.0+build.1"},
		{name: "lower minor pre-1", version: "0.1.0", refused: true},
		{name: "lower minor, higher patch", version: "0.1.9", refused: true},
		{name: "much lower", version: "0.0.1", refused: true},
		{name: "higher major", version: "1.0.0", refused: true},
		{name: "higher minor pre-1", version: "0.3.0", refused: true},
		{name: "a prerelease of a supported release", version: "0.2.0-rc.1", refused: true},
		{name: "a prerelease of a later patch", version: "0.2.1-rc.1", refused: true},
		// A text declaring no version is never refused (§8.1).
		{name: "invalid empty", version: ""},
		{name: "invalid 1.0", version: "1.0"},
		{name: "invalid 0.2", version: "0.2"},
		{name: "invalid letters", version: "a.b.c"},
		{name: "invalid negative", version: "-1.0.0"},
		{name: "surrounding whitespace is not SemVer", version: " " + AuthoringVersion + " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refusedBy(t, tt.version); got != tt.refused {
				t.Errorf("CheckVersion(%q) refuses: %v, want %v", tt.version, got, tt.refused)
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
func TestCheckVersion_PrereleasesAreNamed(t *testing.T) {
	defer func(saved []string) { supportedPrereleases = saved }(supportedPrereleases)
	supportedPrereleases = []string{"0.3.0-rc.1"}
	for version, supported := range map[string]bool{
		"0.3.0-rc.1": true, "0.3.0-rc.1+build.2": true,
		"0.3.0-rc.2": false, "0.3.0": false, "0.2.0-rc.1": false,
	} {
		if refused := refusedBy(t, version); refused == supported {
			t.Errorf("CheckVersion(%q) refuses: %v; want %v", version, refused, !supported)
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

// CheckVersion's refusal is the one every entry point returns for the same
// version, and it refuses exactly what they refuse: no version refusal
// anywhere for a text declaring no version.
func TestCheckVersion_IsTheEntryPointsRefusal(t *testing.T) {
	compiler, err := NewValueContractCompiler(testEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{
		"0.2.0", "0.2.1", "0.2.99", "0.1.0", "0.1.9",
		"0.0.1", "0.3.0", "1.0.0", "0.2.0-rc.1", "0.3.0-rc.1",
		"0.2", "", " 0.2.0",
	}
	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			want := CheckVersion(v)
			data := []byte(fmt.Sprintf(`{"openbindings": %q, "operations": {}}`, v))
			doc := &Document{OpenBindings: v, Operations: map[string]Operation{}}
			_, parseErr := ParseDocument(data)
			_, _, validateDocumentErr := ValidateDocument(data)
			_, validateErr := doc.Validate()
			_, referencesErr := doc.References()
			_, resolveErr := compiler.Resolve(context.Background(), doc)
			for name, err := range map[string]error{
				"ParseDocument":    parseErr,
				"ValidateDocument": validateDocumentErr,
				"Validate":         validateErr,
				"References":       referencesErr,
				"Resolve":          resolveErr,
			} {
				var refusal *VersionRefusalError
				if !errors.As(err, &refusal) {
					refusal = nil
				}
				if want == nil && refusal != nil {
					t.Errorf("%s refuses %q, which CheckVersion does not: %v", name, v, err)
				}
				if want != nil && !reflect.DeepEqual(error(refusal), want) {
					t.Errorf("%s: %v, want CheckVersion's refusal %v", name, err, want)
				}
			}
		})
	}
}

func TestValidSemver(t *testing.T) {
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
			if got := isValidSemver(c.in); got != c.want {
				t.Errorf("isValidSemver(%q) = %v, want %v", c.in, got, c.want)
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
	for version, supported := range map[string]bool{
		huge + ".0.0":      false,
		"0." + huge + ".0": false,
		"0.2." + huge:      true,
		"0.2.0-" + huge:    false,
	} {
		if refused := refusedBy(t, version); refused == supported {
			t.Errorf("CheckVersion(%q) refuses: %v; want %v", version, refused, !supported)
		}
	}
	if _, _, err := ValidateDocument([]byte(`{"openbindings":"` + huge + `.0.0","operations":{}}`)); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("an oversized major must be refused, got %v", err)
	}
	a, _ := parseSemverStrict("1.0.0-" + huge)
	b, _ := parseSemverStrict("1.0.0-" + huge + "0")
	if compareSemver(a, b) >= 0 {
		t.Fatal("numeric pre-release identifiers compare numerically at any size")
	}
}
