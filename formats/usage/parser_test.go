package usage

import (
	"math"
	"testing"
)

func TestValueString(t *testing.T) {
	tests := []struct {
		name     string
		raw      any
		expected string
	}{
		{"string", "hello", "hello"},
		{"int", 42, ""},
		{"nil", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Value{Raw: tt.raw}
			if got := v.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestValueBool(t *testing.T) {
	tests := []struct {
		name     string
		raw      any
		expected bool
		ok       bool
	}{
		{"true", true, true, true},
		{"false", false, false, true},
		{"string true", "true", true, true},   // KDL compat: string "true" is truthy
		{"string false", "false", false, true}, // KDL compat: string "false" is falsy
		{"string #true", "#true", true, true},  // KDL v2 boolean syntax
		{"string #false", "#false", false, true},
		{"string other", "hello", false, false}, // non-boolean string
		{"int", 1, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Value{Raw: tt.raw}
			got, ok := v.Bool()
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestValueInt(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	maxInt64 := int64(^uint64(0) >> 1)
	tests := []struct {
		name     string
		raw      any
		expected int
		ok       bool
	}{
		{"int", 42, 42, true},
		{"int64", int64(100), 100, true},
		{"float64", float64(3.14), 0, false},
		{"float64 NaN", math.NaN(), 0, false},
		{"float64 Inf", math.Inf(1), 0, false},
		{"float64 out of range", float64(maxInt) * 2, 0, false},
		{"float64 whole number", float64(12), 12, true},
		{"string", "42", 0, false},
	}
	if int64(maxInt) < maxInt64 {
		tests = append(tests, struct {
			name     string
			raw      any
			expected int
			ok       bool
		}{"int64 out of range", int64(maxInt) + 1, 0, false})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Value{Raw: tt.raw}
			got, ok := v.Int()
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDecodeMeta(t *testing.T) {
	nodes := []Node{
		{Name: "min_usage_version", Args: []Value{{Raw: "1.3"}}},
		{Name: "name", Args: []Value{{Raw: "My CLI"}}},
		{Name: "bin", Args: []Value{{Raw: "mycli"}}},
		{Name: "about", Args: []Value{{Raw: "A helpful CLI"}}},
		{Name: "version", Args: []Value{{Raw: "1.0.0"}}},
		{Name: "author", Args: []Value{{Raw: "nobody"}}},
		{Name: "license", Args: []Value{{Raw: "MIT"}}},
		{Name: "before_help", Args: []Value{{Raw: "Before"}}},
		{Name: "after_help", Args: []Value{{Raw: "After"}}},
		{Name: "long_about", Args: []Value{{Raw: "Longer description"}}},
		{Name: "include", Args: []Value{{Raw: "./overrides.kdl"}}},
		{Name: "example", Args: []Value{{Raw: "mycli --help"}}, Props: map[string]Value{"header": {Raw: "Getting help"}}},
	}

	spec := &Spec{Nodes: nodes}
	meta := spec.Meta()

	if meta.MinUsageVersion != "1.3" {
		t.Errorf("MinUsageVersion = %q, want %q", meta.MinUsageVersion, "1.3")
	}
	if meta.Name != "My CLI" {
		t.Errorf("Name = %q, want %q", meta.Name, "My CLI")
	}
	if meta.Bin != "mycli" {
		t.Errorf("Bin = %q, want %q", meta.Bin, "mycli")
	}
	if meta.About != "A helpful CLI" {
		t.Errorf("About = %q, want %q", meta.About, "A helpful CLI")
	}
	if meta.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version, "1.0.0")
	}
	if len(meta.Includes) != 1 || meta.Includes[0] != "./overrides.kdl" {
		t.Errorf("Includes = %v, want %v", meta.Includes, []string{"./overrides.kdl"})
	}
	if len(meta.Examples) != 1 {
		t.Errorf("Examples length = %d, want 1", len(meta.Examples))
	}
}

func TestDecodeCommand(t *testing.T) {
	node := Node{
		Name: "cmd",
		Args: []Value{{Raw: "config"}},
		Props: map[string]Value{
			"hide":                {Raw: true},
			"subcommand_required": {Raw: true},
			"help":                {Raw: "Manage config"},
		},
		Children: []Node{
			{Name: "alias", Args: []Value{{Raw: "cfg"}, {Raw: "cf"}}},
			{Name: "flag", Args: []Value{{Raw: "-v --verbose"}}, Props: map[string]Value{"global": {Raw: true}}},
			{Name: "arg", Args: []Value{{Raw: "<key>"}}},
			{
				Name: "cmd",
				Args: []Value{{Raw: "set"}},
				Props: map[string]Value{"help": {Raw: "Set a config value"}},
			},
		},
	}

	cmd := decodeCommand(node)

	if cmd.Name != "config" {
		t.Errorf("Name = %q, want %q", cmd.Name, "config")
	}
	if !cmd.Hide {
		t.Error("Hide should be true")
	}
	if !cmd.SubcommandRequired {
		t.Error("SubcommandRequired should be true")
	}
	if cmd.Help != "Manage config" {
		t.Errorf("Help = %q, want %q", cmd.Help, "Manage config")
	}
	if len(cmd.Aliases) != 1 {
		t.Errorf("Aliases length = %d, want 1", len(cmd.Aliases))
	}
	if len(cmd.Aliases[0].Names) != 2 {
		t.Errorf("Alias names = %v, want 2 names", cmd.Aliases[0].Names)
	}
	if len(cmd.Flags) != 1 {
		t.Errorf("Flags length = %d, want 1", len(cmd.Flags))
	}
	if !cmd.Flags[0].Global {
		t.Error("Flag should be global")
	}
	if len(cmd.Args) != 1 {
		t.Errorf("Args length = %d, want 1", len(cmd.Args))
	}
	if len(cmd.Commands) != 1 {
		t.Errorf("Commands length = %d, want 1", len(cmd.Commands))
	}
	if cmd.Commands[0].Name != "set" {
		t.Errorf("Subcommand name = %q, want %q", cmd.Commands[0].Name, "set")
	}
}

func TestDecodeFlag(t *testing.T) {
	node := Node{
		Name: "flag",
		Args: []Value{{Raw: "-v --verbose"}},
		Props: map[string]Value{
			"global":  {Raw: true},
			"count":   {Raw: true},
			"var":     {Raw: true},
			"var_min": {Raw: 1},
			"var_max": {Raw: 5},
			"default": {Raw: "info"},
			"negate":  {Raw: "--no-verbose"},
			"env":     {Raw: "VERBOSE"},
			"config":  {Raw: "settings.verbose"},
			"help":    {Raw: "Enable verbose output"},
		},
		Children: []Node{
			{Name: "alias", Args: []Value{{Raw: "-V"}}},
			{Name: "arg", Args: []Value{{Raw: "<level>"}}},
			{Name: "choices", Args: []Value{{Raw: "trace"}, {Raw: "debug"}, {Raw: "info"}}},
		},
	}

	flag := decodeFlag(node)

	if flag.Usage != "-v --verbose" {
		t.Errorf("Usage = %q, want %q", flag.Usage, "-v --verbose")
	}
	if !flag.Global {
		t.Error("Global should be true")
	}
	if !flag.Count {
		t.Error("Count should be true")
	}
	if !flag.Var {
		t.Error("Var should be true")
	}
	if flag.VarMin == nil || *flag.VarMin != 1 {
		t.Errorf("VarMin = %v, want 1", flag.VarMin)
	}
	if flag.VarMax == nil || *flag.VarMax != 5 {
		t.Errorf("VarMax = %v, want 5", flag.VarMax)
	}
	if flag.Default != "info" {
		t.Errorf("Default = %v, want %q", flag.Default, "info")
	}
	if flag.Negate != "--no-verbose" {
		t.Errorf("Negate = %q, want %q", flag.Negate, "--no-verbose")
	}
	if flag.Env != "VERBOSE" {
		t.Errorf("Env = %q, want %q", flag.Env, "VERBOSE")
	}
	if flag.ConfigKey != "settings.verbose" {
		t.Errorf("ConfigKey = %q, want %q", flag.ConfigKey, "settings.verbose")
	}
	if flag.Help != "Enable verbose output" {
		t.Errorf("Help = %q, want %q", flag.Help, "Enable verbose output")
	}
	if len(flag.Aliases) != 1 {
		t.Errorf("Aliases length = %d, want 1", len(flag.Aliases))
	}
	if len(flag.Args) != 1 {
		t.Errorf("Args length = %d, want 1", len(flag.Args))
	}
	if len(flag.Choices) != 3 {
		t.Errorf("Choices = %v, want 3 items", flag.Choices)
	}
}

func TestDecodeArg(t *testing.T) {
	node := Node{
		Name: "arg",
		Args: []Value{{Raw: "<file>"}},
		Props: map[string]Value{
			"default":     {Raw: "file.txt"},
			"env":         {Raw: "MY_FILE"},
			"parse":       {Raw: "parse-file {}"},
			"var":         {Raw: true},
			"var_min":     {Raw: 1},
			"var_max":     {Raw: 10},
			"help":        {Raw: "Input file"},
			"double_dash": {Raw: "required"},
			"hide":        {Raw: true},
		},
		Children: []Node{
			{Name: "choices", Args: []Value{{Raw: "a.txt"}, {Raw: "b.txt"}}},
		},
	}

	arg := decodeArg(node)

	if arg.Name != "<file>" {
		t.Errorf("Name = %q, want %q", arg.Name, "<file>")
	}
	if arg.Default != "file.txt" {
		t.Errorf("Default = %v, want %q", arg.Default, "file.txt")
	}
	if arg.Env != "MY_FILE" {
		t.Errorf("Env = %q, want %q", arg.Env, "MY_FILE")
	}
	if arg.Parse != "parse-file {}" {
		t.Errorf("Parse = %q, want %q", arg.Parse, "parse-file {}")
	}
	if !arg.Var {
		t.Error("Var should be true")
	}
	if arg.VarMin == nil || *arg.VarMin != 1 {
		t.Errorf("VarMin = %v, want 1", arg.VarMin)
	}
	if arg.VarMax == nil || *arg.VarMax != 10 {
		t.Errorf("VarMax = %v, want 10", arg.VarMax)
	}
	if arg.Help != "Input file" {
		t.Errorf("Help = %q, want %q", arg.Help, "Input file")
	}
	if arg.DoubleDash != "required" {
		t.Errorf("DoubleDash = %q, want %q", arg.DoubleDash, "required")
	}
	if !arg.Hide {
		t.Error("Hide should be true")
	}
	if len(arg.Choices) != 2 {
		t.Errorf("Choices = %v, want 2 items", arg.Choices)
	}
}

func TestDecodeExample(t *testing.T) {
	// Test header+code pattern
	node := Node{
		Name: "example",
		Args: []Value{{Raw: "Basic usage"}, {Raw: "mycli --help"}},
		Props: map[string]Value{
			"help": {Raw: "Shows help output"},
			"lang": {Raw: "sh"},
		},
	}

	ex := decodeExample(node)

	if ex.Header != "Basic usage" {
		t.Errorf("Header = %q, want %q", ex.Header, "Basic usage")
	}
	if ex.Code != "mycli --help" {
		t.Errorf("Code = %q, want %q", ex.Code, "mycli --help")
	}
	if ex.Help != "Shows help output" {
		t.Errorf("Help = %q, want %q", ex.Help, "Shows help output")
	}
	if ex.Lang != "sh" {
		t.Errorf("Lang = %q, want %q", ex.Lang, "sh")
	}

	// Test code-only pattern
	node2 := Node{
		Name: "example",
		Args: []Value{{Raw: "mycli run"}},
	}

	ex2 := decodeExample(node2)

	if ex2.Header != "" {
		t.Errorf("Header = %q, want empty", ex2.Header)
	}
	if ex2.Code != "mycli run" {
		t.Errorf("Code = %q, want %q", ex2.Code, "mycli run")
	}
}

func TestDecodeConfig(t *testing.T) {
	nodes := []Node{
		{
			Name: "config",
			Children: []Node{
				{Name: "file", Args: []Value{{Raw: "~/.config/mycli.toml"}}, Props: map[string]Value{"findup": {Raw: true}}},
				{Name: "default", Args: []Value{{Raw: "user"}, {Raw: "admin"}}},
				{Name: "alias", Args: []Value{{Raw: "user"}, {Raw: "username"}}},
			},
		},
	}

	spec := &Spec{Nodes: nodes}
	cfg := spec.Config()

	if cfg == nil {
		t.Fatal("Config should not be nil")
	}
	if len(cfg.Files) != 1 {
		t.Errorf("Files length = %d, want 1", len(cfg.Files))
	}
	if cfg.Files[0].Path != "~/.config/mycli.toml" {
		t.Errorf("File path = %q, want %q", cfg.Files[0].Path, "~/.config/mycli.toml")
	}
	if !cfg.Files[0].FindUp {
		t.Error("FindUp should be true")
	}
	if len(cfg.Defaults) != 1 {
		t.Errorf("Defaults length = %d, want 1", len(cfg.Defaults))
	}
	if cfg.Defaults[0].Key != "user" {
		t.Errorf("Default key = %q, want %q", cfg.Defaults[0].Key, "user")
	}
	if cfg.Defaults[0].Value != "admin" {
		t.Errorf("Default value = %v, want %q", cfg.Defaults[0].Value, "admin")
	}
	if len(cfg.Aliases) != 1 {
		t.Errorf("Aliases length = %d, want 1", len(cfg.Aliases))
	}
}

func TestDecodeTopLevelConfigFile(t *testing.T) {
	// Test top-level config_file and config_alias (alternative syntax)
	nodes := []Node{
		{Name: "config_file", Args: []Value{{Raw: ".mycli.toml"}}, Props: map[string]Value{"findup": {Raw: true}}},
		{Name: "config_alias", Args: []Value{{Raw: "user"}, {Raw: "username"}}},
	}

	spec := &Spec{Nodes: nodes}
	cfg := spec.Config()

	if cfg == nil {
		t.Fatal("Config should not be nil")
	}
	if len(cfg.Files) != 1 {
		t.Errorf("Files length = %d, want 1", len(cfg.Files))
	}
	if len(cfg.Aliases) != 1 {
		t.Errorf("Aliases length = %d, want 1", len(cfg.Aliases))
	}
}

func TestDecodeComplete(t *testing.T) {
	node := Node{
		Name: "complete",
		Args: []Value{{Raw: "plugin"}},
		Props: map[string]Value{
			"run":          {Raw: "mycli plugins list"},
			"descriptions": {Raw: true},
		},
	}

	complete := decodeComplete(node)

	if complete.Target != "plugin" {
		t.Errorf("Target = %q, want %q", complete.Target, "plugin")
	}
	if complete.Run != "mycli plugins list" {
		t.Errorf("Run = %q, want %q", complete.Run, "mycli plugins list")
	}
	if !complete.Descriptions {
		t.Error("Descriptions should be true")
	}
}

func TestDecodeMount(t *testing.T) {
	node := Node{
		Name:  "mount",
		Props: map[string]Value{"run": {Raw: "mycli mount-usage-tasks"}},
	}

	mount := decodeMount(node)

	if mount.Run != "mycli mount-usage-tasks" {
		t.Errorf("Run = %q, want %q", mount.Run, "mycli mount-usage-tasks")
	}
}

func TestDecodeCommand_HelpFromSecondArg(t *testing.T) {
	// The Usage spec allows: cmd "name" "help text"
	node := Node{
		Name: "cmd",
		Args: []Value{{Raw: "add"}, {Raw: "Add a new item"}},
	}

	cmd := decodeCommand(node)

	if cmd.Name != "add" {
		t.Errorf("Name = %q, want %q", cmd.Name, "add")
	}
	if cmd.Help != "Add a new item" {
		t.Errorf("Help = %q, want %q", cmd.Help, "Add a new item")
	}

	// Explicit help= property should take precedence over second arg
	node2 := Node{
		Name:  "cmd",
		Args:  []Value{{Raw: "add"}, {Raw: "from arg"}},
		Props: map[string]Value{"help": {Raw: "from prop"}},
	}
	cmd2 := decodeCommand(node2)
	if cmd2.Help != "from prop" {
		t.Errorf("Help = %q, want %q (prop should take precedence)", cmd2.Help, "from prop")
	}
}

func TestDecodeFlag_HelpFromSecondArg(t *testing.T) {
	// The Usage spec allows: flag "--verbose" "Enable verbose output"
	node := Node{
		Name: "flag",
		Args: []Value{{Raw: "--verbose"}, {Raw: "Enable verbose output"}},
	}

	flag := decodeFlag(node)

	if flag.Usage != "--verbose" {
		t.Errorf("Usage = %q, want %q", flag.Usage, "--verbose")
	}
	if flag.Help != "Enable verbose output" {
		t.Errorf("Help = %q, want %q", flag.Help, "Enable verbose output")
	}
}

func TestDecodeFlag_VariadicShorthand(t *testing.T) {
	// The Usage spec allows trailing "..." on flag names: flag "--include..."
	node := Node{
		Name: "flag",
		Args: []Value{{Raw: "--include..."}},
	}

	flag := decodeFlag(node)

	if !flag.Var {
		t.Error("Var should be true for --include... shorthand")
	}

	// Explicit var=#true should still work
	node2 := Node{
		Name:  "flag",
		Args:  []Value{{Raw: "--include"}},
		Props: map[string]Value{"var": {Raw: true}},
	}
	flag2 := decodeFlag(node2)
	if !flag2.Var {
		t.Error("Var should be true for explicit var=#true")
	}

	// Non-variadic flag should not have Var set
	node3 := Node{
		Name: "flag",
		Args: []Value{{Raw: "--verbose"}},
	}
	flag3 := decodeFlag(node3)
	if flag3.Var {
		t.Error("Var should be false for --verbose (no ...)")
	}
}

func TestDecodeMeta_StructuralNodesFiltered(t *testing.T) {
	// Structural nodes should NOT appear in Meta.Unknown
	nodes := []Node{
		{Name: "name", Args: []Value{{Raw: "test"}}},
		{Name: "cmd", Args: []Value{{Raw: "run"}}},
		{Name: "flag", Args: []Value{{Raw: "--verbose"}}},
		{Name: "arg", Args: []Value{{Raw: "<file>"}}},
		{Name: "complete", Args: []Value{{Raw: "plugin"}}},
		{Name: "config", Children: []Node{}},
		{Name: "config_file", Args: []Value{{Raw: "~/.config"}}},
		{Name: "config_alias", Args: []Value{{Raw: "user"}, {Raw: "username"}}},
		{Name: "custom_extension", Args: []Value{{Raw: "something"}}},
	}

	spec := &Spec{Nodes: nodes}
	meta := spec.Meta()

	if len(meta.Unknown) != 1 {
		t.Errorf("Unknown length = %d, want 1 (only custom_extension)", len(meta.Unknown))
	}
	if len(meta.Unknown) > 0 && meta.Unknown[0].Name != "custom_extension" {
		t.Errorf("Unknown[0].Name = %q, want %q", meta.Unknown[0].Name, "custom_extension")
	}
}

func TestStringPropOrChild(t *testing.T) {
	// Test property access
	node := Node{
		Name:  "cmd",
		Props: map[string]Value{"help": {Raw: "from prop"}},
	}
	if got := stringPropOrChild(node, "help"); got != "from prop" {
		t.Errorf("got %q, want %q", got, "from prop")
	}

	// Test child node access
	node2 := Node{
		Name: "cmd",
		Children: []Node{
			{Name: "help", Args: []Value{{Raw: "from child"}}},
		},
	}
	if got := stringPropOrChild(node2, "help"); got != "from child" {
		t.Errorf("got %q, want %q", got, "from child")
	}

	// Property takes precedence
	node3 := Node{
		Name:  "cmd",
		Props: map[string]Value{"help": {Raw: "from prop"}},
		Children: []Node{
			{Name: "help", Args: []Value{{Raw: "from child"}}},
		},
	}
	if got := stringPropOrChild(node3, "help"); got != "from prop" {
		t.Errorf("got %q, want %q", got, "from prop")
	}
}
