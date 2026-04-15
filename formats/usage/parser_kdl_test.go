//go:build kdl

package usage

import (
	"strings"
	"testing"
)

// testKDL is a minimal Usage spec for testing.
const testKDL = `
min_usage_version "1.0"
name "Test CLI"
bin "testcli"
about "A test CLI"
version "1.0.0"

flag "-v --verbose" help="Enable verbose output" global=#true count=#true
flag "-f --force" help="Force the operation"
flag "-u --user" help="User to run as" {
    arg "<user>"
}

arg "<input>" help="Input file"
arg "[output]" help="Output file"

cmd "config" help="Manage configuration" {
    alias "cfg" "cf"
    
    flag "--no-header" help="Omit table header"
    
    cmd "set" help="Set a config value" {
        arg "<key>" help="Config key"
        arg "<value>" help="Config value"
    }
    
    cmd "get" help="Get a config value" {
        arg "<key>" help="Config key"
    }
    
    cmd "list" help="List all config values" {
        alias "ls"
    }
}

cmd "version" help="Print version"

config {
    file "~/.config/testcli.toml"
    file ".testcli.toml" findup=#true
    default "user" "admin"
}

complete "user" run="testcli users list"

example "Basic usage" "testcli --help"
example "testcli config set key value"
`

func TestParseKDL(t *testing.T) {
	spec, err := ParseKDL([]byte(testKDL))
	if err != nil {
		t.Fatalf("ParseKDL failed: %v", err)
	}

	// Test metadata
	meta := spec.Meta()
	if meta.MinUsageVersion != "1.0" {
		t.Errorf("MinUsageVersion = %q, want %q", meta.MinUsageVersion, "1.0")
	}
	if meta.Name != "Test CLI" {
		t.Errorf("Name = %q, want %q", meta.Name, "Test CLI")
	}
	if meta.Bin != "testcli" {
		t.Errorf("Bin = %q, want %q", meta.Bin, "testcli")
	}
	if meta.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version, "1.0.0")
	}
	if len(meta.Examples) != 2 {
		t.Errorf("Examples count = %d, want 2", len(meta.Examples))
	}

	// Test top-level flags
	flags := spec.Flags()
	if len(flags) != 3 {
		t.Errorf("Flags count = %d, want 3", len(flags))
	}
	if flags[0].Usage != "-v --verbose" {
		t.Errorf("First flag usage = %q, want %q", flags[0].Usage, "-v --verbose")
	}
	if !flags[0].Global {
		t.Error("First flag should be global")
	}
	if !flags[0].Count {
		t.Error("First flag should be count")
	}

	// Test top-level args
	args := spec.Args()
	if len(args) != 2 {
		t.Errorf("Args count = %d, want 2", len(args))
	}
	if args[0].Name != "<input>" {
		t.Errorf("First arg name = %q, want %q", args[0].Name, "<input>")
	}
	if !args[0].IsRequired() {
		t.Error("First arg should be required")
	}
	if args[1].Name != "[output]" {
		t.Errorf("Second arg name = %q, want %q", args[1].Name, "[output]")
	}
	if args[1].IsRequired() {
		t.Error("Second arg should be optional")
	}

	// Test commands
	cmds := spec.Commands()
	if len(cmds) != 2 {
		t.Errorf("Commands count = %d, want 2", len(cmds))
	}

	// Find config command
	var configCmd *Command
	for i := range cmds {
		if cmds[i].Name == "config" {
			configCmd = &cmds[i]
			break
		}
	}
	if configCmd == nil {
		t.Fatal("config command not found")
	}
	if configCmd.Help != "Manage configuration" {
		t.Errorf("config help = %q, want %q", configCmd.Help, "Manage configuration")
	}
	if len(configCmd.Aliases) != 1 {
		t.Errorf("config aliases count = %d, want 1", len(configCmd.Aliases))
	}
	if len(configCmd.Aliases[0].Names) != 2 {
		t.Errorf("config alias names = %v, want 2 names", configCmd.Aliases[0].Names)
	}
	if len(configCmd.Commands) != 3 {
		t.Errorf("config subcommands count = %d, want 3", len(configCmd.Commands))
	}

	// Test nested command
	var setCmd *Command
	for i := range configCmd.Commands {
		if configCmd.Commands[i].Name == "set" {
			setCmd = &configCmd.Commands[i]
			break
		}
	}
	if setCmd == nil {
		t.Fatal("config set command not found")
	}
	if len(setCmd.Args) != 2 {
		t.Errorf("set args count = %d, want 2", len(setCmd.Args))
	}

	// Test config
	cfg := spec.Config()
	if cfg == nil {
		t.Fatal("config should not be nil")
	}
	if len(cfg.Files) != 2 {
		t.Errorf("config files count = %d, want 2", len(cfg.Files))
	}
	if !cfg.Files[1].FindUp {
		t.Error("second config file should have findup=true")
	}
	if len(cfg.Defaults) != 1 {
		t.Errorf("config defaults count = %d, want 1", len(cfg.Defaults))
	}

	// Test complete
	completes := spec.Completes()
	if len(completes) != 1 {
		t.Errorf("completes count = %d, want 1", len(completes))
	}
	if completes[0].Target != "user" {
		t.Errorf("complete target = %q, want %q", completes[0].Target, "user")
	}
	if completes[0].Run != "testcli users list" {
		t.Errorf("complete run = %q, want %q", completes[0].Run, "testcli users list")
	}
}

func TestSpecWalkWithKDL(t *testing.T) {
	spec, err := ParseKDL([]byte(testKDL))
	if err != nil {
		t.Fatalf("ParseKDL failed: %v", err)
	}

	var paths []string
	spec.Walk(func(path []string, cmd Command) {
		paths = append(paths, cmd.Name)
	})

	// Should walk: config, set, get, list, version
	if len(paths) != 5 {
		t.Errorf("walked %d commands, want 5: %v", len(paths), paths)
	}
}

func TestFindCommandWithKDL(t *testing.T) {
	spec, err := ParseKDL([]byte(testKDL))
	if err != nil {
		t.Fatalf("ParseKDL failed: %v", err)
	}

	cmd := spec.FindCommand([]string{"config", "set"})
	if cmd == nil {
		t.Fatal("config set command not found")
	}
	if cmd.Name != "set" {
		t.Errorf("name = %q, want %q", cmd.Name, "set")
	}
	if cmd.Help != "Set a config value" {
		t.Errorf("help = %q, want %q", cmd.Help, "Set a config value")
	}
}

func TestFlagParseUsageWithKDL(t *testing.T) {
	spec, err := ParseKDL([]byte(testKDL))
	if err != nil {
		t.Fatalf("ParseKDL failed: %v", err)
	}

	flags := spec.Flags()
	if len(flags) < 3 {
		t.Fatalf("expected at least 3 flags, got %d", len(flags))
	}

	// Test -u --user <user>
	userFlag := flags[2]
	parsed := userFlag.ParseUsage()

	if len(parsed.Short) != 1 || parsed.Short[0] != "u" {
		t.Errorf("Short = %v, want [u]", parsed.Short)
	}
	if len(parsed.Long) != 1 || parsed.Long[0] != "user" {
		t.Errorf("Long = %v, want [user]", parsed.Long)
	}

	// Check nested arg
	if len(userFlag.Args) != 1 {
		t.Errorf("user flag args count = %d, want 1", len(userFlag.Args))
	}
	if userFlag.Args[0].Name != "<user>" {
		t.Errorf("user flag arg name = %q, want %q", userFlag.Args[0].Name, "<user>")
	}
}

func TestParseFile_ResolvesIncludes(t *testing.T) {
	spec, err := ParseFile("testdata/include_main.kdl")
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	cmds := spec.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (shared + local), got %d", len(cmds))
	}

	names := map[string]bool{}
	for _, c := range cmds {
		names[c.Name] = true
	}
	if !names["shared"] {
		t.Error("expected 'shared' command from included file")
	}
	if !names["local"] {
		t.Error("expected 'local' command from main file")
	}
}

func TestParseFile_IncludeCycleDetected(t *testing.T) {
	_, err := ParseFile("testdata/include_cycle_a.kdl")
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, expected to mention cycle", err.Error())
	}
}
