package openbindings

import (
	"slices"
	"testing"
)

func TestSupportedRange(t *testing.T) {
	min, max := SupportedRange()
	if min == "" {
		t.Error("min version should not be empty")
	}
	if max == "" {
		t.Error("max version should not be empty")
	}
	minParsed, _ := parseSemverStrict(min)
	maxParsed, _ := parseSemverStrict(max)
	if compareSemver(minParsed, maxParsed) > 0 {
		t.Errorf("min (%s) should be <= max (%s)", min, max)
	}
}

func TestIsSupportedVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{name: "exact min version", version: MinSupportedVersion, want: true},
		{name: "exact max version", version: MaxTestedVersion, want: true},
		{name: "below min", version: "0.1.0", want: false},
		{name: "much below min", version: "0.0.1", want: false},
		{name: "above max major", version: "1.0.0", want: false},
		{name: "above max pre-1 minor", version: "0.3.0", want: false},
		{name: "invalid empty", version: "", wantErr: true},
		{name: "invalid 1.0", version: "1.0", wantErr: true},
		{name: "invalid letters", version: "a.b.c", wantErr: true},
		{name: "invalid negative", version: "-1.0.0", wantErr: true},
		{name: "trims whitespace", version: " " + MinSupportedVersion + " ", want: true},
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

func TestIsHigherMajorOrPre1MinorThanMaxTested(t *testing.T) {
	// MaxTestedVersion is currently "0.2.0" — pre-1.0, so OBI-T-04 also refuses higher minor.
	tests := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{name: "exact max", version: MaxTestedVersion, want: false},
		{name: "lower minor pre-1", version: "0.1.0", want: false},
		{name: "higher patch only", version: "0.2.5", want: false},
		{name: "higher minor pre-1 (OBI-T-04 refusal)", version: "0.3.0", want: true},
		{name: "higher major", version: "1.0.0", want: true},
		{name: "much higher major", version: "5.0.0", want: true},
		{name: "invalid", version: "not-a-version", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsHigherMajorOrPre1MinorThanMaxTested(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsHigherMajorOrPre1MinorThanMaxTested(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("IsHigherMajorOrPre1MinorThanMaxTested(%q) = %v, want %v", tt.version, got, tt.want)
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
		{"  1.2.3  ", true}, // trims
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
		{name: "valid 0.1.0", input: "0.1.0", want: semver{major: 0, minor: 1, patch: 0}},
		{name: "valid 1.2.3", input: "1.2.3", want: semver{major: 1, minor: 2, patch: 3}},
		{name: "valid large numbers", input: "10.20.30", want: semver{major: 10, minor: 20, patch: 30}},
		{name: "valid with whitespace", input: "  1.2.3  ", want: semver{major: 1, minor: 2, patch: 3}},
		{name: "valid with prerelease", input: "1.0.0-alpha.1", want: semver{major: 1, minor: 0, patch: 0, preRelease: []string{"alpha", "1"}}},
		{name: "valid with build", input: "1.0.0+exp", want: semver{major: 1, minor: 0, patch: 0, build: "exp"}},
		{name: "valid with pre + build", input: "1.0.0-beta+exp", want: semver{major: 1, minor: 0, patch: 0, preRelease: []string{"beta"}, build: "exp"}},
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
				if got.build != tt.want.build {
					t.Errorf("parseSemverStrict(%q).build = %q, want %q", tt.input, got.build, tt.want.build)
				}
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		name string
		a    semver
		b    semver
		want int
	}{
		{name: "equal versions", a: semver{major: 1, minor: 2, patch: 3}, b: semver{major: 1, minor: 2, patch: 3}, want: 0},
		{name: "a major greater", a: semver{major: 2}, b: semver{major: 1, minor: 9, patch: 9}, want: 1},
		{name: "a major less", a: semver{major: 1, minor: 9, patch: 9}, b: semver{major: 2}, want: -1},
		{name: "a minor greater", a: semver{major: 1, minor: 3}, b: semver{major: 1, minor: 2, patch: 9}, want: 1},
		{name: "a minor less", a: semver{major: 1, minor: 2, patch: 9}, b: semver{major: 1, minor: 3}, want: -1},
		{name: "a patch greater", a: semver{major: 1, minor: 2, patch: 4}, b: semver{major: 1, minor: 2, patch: 3}, want: 1},
		{name: "a patch less", a: semver{major: 1, minor: 2, patch: 3}, b: semver{major: 1, minor: 2, patch: 4}, want: -1},
		{name: "zero versions", a: semver{}, b: semver{}, want: 0},
		// SemVer 2.0.0 §11: a version with pre-release has lower precedence than the same normal version.
		{name: "prerelease lower than no prerelease", a: semver{major: 1, preRelease: []string{"alpha"}}, b: semver{major: 1}, want: -1},
		{name: "no prerelease higher than prerelease", a: semver{major: 1}, b: semver{major: 1, preRelease: []string{"alpha"}}, want: 1},
		{name: "alpha < beta lex", a: semver{major: 1, preRelease: []string{"alpha"}}, b: semver{major: 1, preRelease: []string{"beta"}}, want: -1},
		{name: "alpha < alpha.1 (shorter < longer)", a: semver{major: 1, preRelease: []string{"alpha"}}, b: semver{major: 1, preRelease: []string{"alpha", "1"}}, want: -1},
		{name: "numeric < alphanumeric prerelease", a: semver{major: 1, preRelease: []string{"1"}}, b: semver{major: 1, preRelease: []string{"alpha"}}, want: -1},
		{name: "numeric prerelease ordering", a: semver{major: 1, preRelease: []string{"1"}}, b: semver{major: 1, preRelease: []string{"2"}}, want: -1},
		{name: "build metadata ignored", a: semver{major: 1, build: "exp.a"}, b: semver{major: 1, build: "exp.b"}, want: 0},
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
