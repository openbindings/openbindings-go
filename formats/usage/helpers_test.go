package usage

import "testing"

func TestArgIsRequired(t *testing.T) {
	tests := []struct {
		name     string
		argName  string
		expected bool
	}{
		{"required angle brackets", "<file>", true},
		{"optional square brackets", "[file]", false},
		{"required with ellipsis", "<file>...", true},
		{"optional with ellipsis", "[file]...", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arg := Arg{Name: tt.argName}
			if got := arg.IsRequired(); got != tt.expected {
				t.Errorf("Arg{Name: %q}.IsRequired() = %v, want %v", tt.argName, got, tt.expected)
			}
		})
	}
}

func TestArgIsVariadic(t *testing.T) {
	tests := []struct {
		name     string
		arg      Arg
		expected bool
	}{
		{"var=true", Arg{Name: "<file>", Var: true}, true},
		{"ellipsis suffix", Arg{Name: "<file>..."}, true},
		{"neither", Arg{Name: "<file>"}, false},
		{"optional variadic", Arg{Name: "[file]..."}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.arg.IsVariadic(); got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestArgCleanName(t *testing.T) {
	tests := []struct {
		name     string
		argName  string
		expected string
	}{
		{"required", "<file>", "file"},
		{"optional", "[file]", "file"},
		{"required variadic", "<file>...", "file"},
		{"optional variadic", "[file]...", "file"},
		{"plain", "file", "file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arg := Arg{Name: tt.argName}
			if got := arg.CleanName(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFlagParseUsage(t *testing.T) {
	tests := []struct {
		name      string
		usage     string
		wantShort []string
		wantLong  []string
		wantArg   string
	}{
		{
			name:      "short and long with arg",
			usage:     "-u --user <user>",
			wantShort: []string{"u"},
			wantLong:  []string{"user"},
			wantArg:   "user",
		},
		{
			name:      "short and long no arg",
			usage:     "-v --verbose",
			wantShort: []string{"v"},
			wantLong:  []string{"verbose"},
			wantArg:   "",
		},
		{
			name:      "long only",
			usage:     "--force",
			wantShort: nil,
			wantLong:  []string{"force"},
			wantArg:   "",
		},
		{
			name:      "short only",
			usage:     "-f",
			wantShort: []string{"f"},
			wantLong:  nil,
			wantArg:   "",
		},
		{
			name:      "multiple shorts",
			usage:     "-C --cd",
			wantShort: []string{"C"},
			wantLong:  []string{"cd"},
			wantArg:   "",
		},
		{
			name:      "optional arg syntax",
			usage:     "--file [path]",
			wantShort: nil,
			wantLong:  []string{"file"},
			wantArg:   "path",
		},
		{
			name:      "empty",
			usage:     "",
			wantShort: nil,
			wantLong:  nil,
			wantArg:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Flag{Usage: tt.usage}
			got := f.ParseUsage()

			if !sliceEqual(got.Short, tt.wantShort) {
				t.Errorf("Short = %v, want %v", got.Short, tt.wantShort)
			}
			if !sliceEqual(got.Long, tt.wantLong) {
				t.Errorf("Long = %v, want %v", got.Long, tt.wantLong)
			}
			if got.ArgName != tt.wantArg {
				t.Errorf("ArgName = %q, want %q", got.ArgName, tt.wantArg)
			}
		})
	}
}

func TestFlagPrimaryName(t *testing.T) {
	tests := []struct {
		name     string
		usage    string
		expected string
	}{
		{"prefers long", "-v --verbose", "verbose"},
		{"short only", "-f", "f"},
		{"long only", "--force", "force"},
		{"empty returns usage", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Flag{Usage: tt.usage}
			if got := f.PrimaryName(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCommandFullPath(t *testing.T) {
	cmd := Command{Name: "set"}

	got := cmd.FullPath([]string{"config"})
	want := []string{"config", "set"}

	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Empty ancestors
	got = cmd.FullPath(nil)
	want = []string{"set"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCommandFullPath_NoAliasing(t *testing.T) {
	// Verify that FullPath doesn't corrupt the ancestors slice for sibling commands.
	// This was a bug where append() could alias the backing array.
	ancestors := make([]string, 0, 4) // extra capacity to trigger aliasing
	ancestors = append(ancestors, "root")

	sibling1 := Command{Name: "alpha"}
	sibling2 := Command{Name: "beta"}

	path1 := sibling1.FullPath(ancestors)
	path2 := sibling2.FullPath(ancestors)

	// path1 should be ["root", "alpha"], not corrupted by sibling2
	want1 := []string{"root", "alpha"}
	want2 := []string{"root", "beta"}

	if !sliceEqual(path1, want1) {
		t.Errorf("path1 = %v, want %v (aliasing corruption)", path1, want1)
	}
	if !sliceEqual(path2, want2) {
		t.Errorf("path2 = %v, want %v", path2, want2)
	}
}

func TestFlagParseUsage_Variadic(t *testing.T) {
	// The trailing "..." should be stripped from the flag name
	f := Flag{Usage: "--include..."}
	got := f.ParseUsage()

	if len(got.Long) != 1 || got.Long[0] != "include" {
		t.Errorf("Long = %v, want [\"include\"]", got.Long)
	}
}

func TestFlagPrimaryName_Variadic(t *testing.T) {
	f := Flag{Usage: "--include..."}
	if got := f.PrimaryName(); got != "include" {
		t.Errorf("PrimaryName() = %q, want %q", got, "include")
	}
}

func TestCommandAllFlags(t *testing.T) {
	globalFlag := Flag{Usage: "-v --verbose", Global: true}
	localFlag := Flag{Usage: "-f --force"}
	inheritedGlobal := Flag{Usage: "-q --quiet", Global: true}

	cmd := Command{
		Name:  "test",
		Flags: []Flag{globalFlag, localFlag},
	}

	all := cmd.AllFlags([]Flag{inheritedGlobal})

	// Should have 3 flags: 2 globals (verbose, quiet) + 1 local (force)
	if len(all) != 3 {
		t.Errorf("got %d flags, want 3", len(all))
	}

	// Globals should come first
	if all[0].Usage != "-v --verbose" {
		t.Errorf("first flag should be verbose, got %q", all[0].Usage)
	}
}

func TestSpecFindCommand(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "config"}},
				Children: []Node{
					{
						Name: "cmd",
						Args: []Value{{Raw: "set"}},
					},
					{
						Name: "cmd",
						Args: []Value{{Raw: "get"}},
					},
				},
			},
			{
				Name: "cmd",
				Args: []Value{{Raw: "version"}},
			},
		},
	}

	// Find top-level command
	cmd := spec.FindCommand([]string{"config"})
	if cmd == nil {
		t.Fatal("expected to find 'config' command")
	}
	if cmd.Name != "config" {
		t.Errorf("got name %q, want %q", cmd.Name, "config")
	}

	// Find nested command
	cmd = spec.FindCommand([]string{"config", "set"})
	if cmd == nil {
		t.Fatal("expected to find 'config set' command")
	}
	if cmd.Name != "set" {
		t.Errorf("got name %q, want %q", cmd.Name, "set")
	}

	// Not found
	cmd = spec.FindCommand([]string{"nonexistent"})
	if cmd != nil {
		t.Error("expected nil for nonexistent command")
	}

	// Empty path
	cmd = spec.FindCommand(nil)
	if cmd != nil {
		t.Error("expected nil for empty path")
	}
}

func TestSpecWalk(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "config"}},
				Children: []Node{
					{
						Name: "cmd",
						Args: []Value{{Raw: "set"}},
					},
				},
			},
			{
				Name: "cmd",
				Args: []Value{{Raw: "version"}},
			},
		},
	}

	var visited []string
	spec.Walk(func(path []string, cmd Command) {
		visited = append(visited, cmd.Name)
	})

	if len(visited) != 3 {
		t.Errorf("visited %d commands, want 3", len(visited))
	}

	// Should visit in order: config, set, version
	expected := []string{"config", "set", "version"}
	for i, name := range expected {
		if visited[i] != name {
			t.Errorf("visited[%d] = %q, want %q", i, visited[i], name)
		}
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
