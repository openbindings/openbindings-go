package openbindings

import "testing"

func TestSupportedRange(t *testing.T) {
	min, max := SupportedRange()
	if min == "" {
		t.Error("min version should not be empty")
	}
	if max == "" {
		t.Error("max version should not be empty")
	}
	// Min should be <= Max
	minParsed, _ := parseSemverStrict(min)
	maxParsed, _ := parseSemverStrict(max)
	if compareSemver(minParsed, maxParsed) > 0 {
		t.Errorf("min (%s) should be <= max (%s)", min, max)
	}
}

func TestIsSupportedVersion(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		want      bool
		wantErr   bool
	}{
		{
			name:    "exact min version",
			version: MinSupportedVersion,
			want:    true,
		},
		{
			name:    "exact max version",
			version: MaxTestedVersion,
			want:    true,
		},
		{
			name:    "version 0.1.0",
			version: "0.1.0",
			want:    true,
		},
		{
			name:    "version too old",
			version: "0.0.1",
			want:    false,
		},
		{
			name:    "version too new",
			version: "1.0.0",
			want:    false,
		},
		{
			name:    "future minor version",
			version: "0.2.0",
			want:    false,
		},
		{
			name:    "invalid version - empty",
			version: "",
			wantErr: true,
		},
		{
			name:    "invalid version - not semver",
			version: "1.0",
			wantErr: true,
		},
		{
			name:    "invalid version - letters",
			version: "a.b.c",
			wantErr: true,
		},
		{
			name:    "invalid version - negative",
			version: "-1.0.0",
			wantErr: true,
		},
		{
			name:    "version with whitespace",
			version: " 0.1.0 ",
			want:    true, // trimmed
		},
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

func TestParseSemverStrict(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    semver
		wantErr bool
	}{
		{
			name:  "valid 0.1.0",
			input: "0.1.0",
			want:  semver{major: 0, minor: 1, patch: 0},
		},
		{
			name:  "valid 1.2.3",
			input: "1.2.3",
			want:  semver{major: 1, minor: 2, patch: 3},
		},
		{
			name:  "valid with large numbers",
			input: "10.20.30",
			want:  semver{major: 10, minor: 20, patch: 30},
		},
		{
			name:  "valid with whitespace",
			input: "  1.2.3  ",
			want:  semver{major: 1, minor: 2, patch: 3},
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "too few parts",
			input:   "1.2",
			wantErr: true,
		},
		{
			name:    "too many parts",
			input:   "1.2.3.4",
			wantErr: true,
		},
		{
			name:    "non-numeric major",
			input:   "a.1.2",
			wantErr: true,
		},
		{
			name:    "non-numeric minor",
			input:   "1.b.2",
			wantErr: true,
		},
		{
			name:    "non-numeric patch",
			input:   "1.2.c",
			wantErr: true,
		},
		{
			name:    "negative major",
			input:   "-1.2.3",
			wantErr: true,
		},
		{
			name:    "negative minor",
			input:   "1.-2.3",
			wantErr: true,
		},
		{
			name:    "negative patch",
			input:   "1.2.-3",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSemverStrict(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSemverStrict(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSemverStrict(%q) = %v, want %v", tt.input, got, tt.want)
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
		{
			name: "equal versions",
			a:    semver{1, 2, 3},
			b:    semver{1, 2, 3},
			want: 0,
		},
		{
			name: "a major greater",
			a:    semver{2, 0, 0},
			b:    semver{1, 9, 9},
			want: 1,
		},
		{
			name: "a major less",
			a:    semver{1, 9, 9},
			b:    semver{2, 0, 0},
			want: -1,
		},
		{
			name: "a minor greater",
			a:    semver{1, 3, 0},
			b:    semver{1, 2, 9},
			want: 1,
		},
		{
			name: "a minor less",
			a:    semver{1, 2, 9},
			b:    semver{1, 3, 0},
			want: -1,
		},
		{
			name: "a patch greater",
			a:    semver{1, 2, 4},
			b:    semver{1, 2, 3},
			want: 1,
		},
		{
			name: "a patch less",
			a:    semver{1, 2, 3},
			b:    semver{1, 2, 4},
			want: -1,
		},
		{
			name: "zero versions",
			a:    semver{0, 0, 0},
			b:    semver{0, 0, 0},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

