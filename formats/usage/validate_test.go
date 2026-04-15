package usage

import (
	"strings"
	"testing"
)

func TestValidate_Default(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "name", Args: []Value{{Raw: "Test CLI"}}},
			{Name: "bin", Args: []Value{{Raw: "testcli"}}},
			{
				Name: "cmd",
				Args: []Value{{Raw: "run"}},
				Props: map[string]Value{"help": {Raw: "Run something"}},
			},
		},
	}

	if err := spec.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_RequireName(t *testing.T) {
	spec := &Spec{Nodes: []Node{}}

	// Without option: passes
	if err := spec.Validate(); err != nil {
		t.Errorf("default should pass: %v", err)
	}

	// With option: fails
	err := spec.Validate(WithRequireName())
	if err == nil {
		t.Error("expected error with WithRequireName")
	}
	if !strings.Contains(err.Error(), "name: required") {
		t.Errorf("expected 'name: required', got: %v", err)
	}
}

func TestValidate_RequireBin(t *testing.T) {
	spec := &Spec{Nodes: []Node{}}

	err := spec.Validate(WithRequireBin())
	if err == nil {
		t.Error("expected error with WithRequireBin")
	}
	if !strings.Contains(err.Error(), "bin: required") {
		t.Errorf("expected 'bin: required', got: %v", err)
	}
}

func TestValidate_RequireSupportedVersion(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "min_usage_version", Args: []Value{{Raw: "3.0.0"}}},
		},
	}

	err := spec.Validate(WithRequireSupportedVersion())
	if err == nil {
		t.Error("expected error with WithRequireSupportedVersion")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("expected unsupported version error, got: %v", err)
	}
}

func TestValidate_RequireSupportedVersion_Missing(t *testing.T) {
	spec := &Spec{Nodes: []Node{}}

	err := spec.Validate(WithRequireSupportedVersion())
	if err == nil {
		t.Error("expected error with WithRequireSupportedVersion")
	}
	if !strings.Contains(err.Error(), "min_usage_version: required") {
		t.Errorf("expected min_usage_version required, got: %v", err)
	}
}

func TestValidate_RejectUnknownNodes(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "name", Args: []Value{{Raw: "Test"}}},
			{Name: "futureNode", Args: []Value{{Raw: "value"}}},
		},
	}

	// Default: passes
	if err := spec.Validate(); err != nil {
		t.Errorf("default should pass: %v", err)
	}

	// Strict: fails
	err := spec.Validate(WithRejectUnknownNodes())
	if err == nil {
		t.Error("expected error with WithRejectUnknownNodes")
	}
	if !strings.Contains(err.Error(), "unknown nodes") {
		t.Errorf("expected 'unknown nodes', got: %v", err)
	}
}

func TestValidate_RejectUnknownNodes_IgnoresStructuralNodes(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "cmd", Args: []Value{{Raw: "run"}}, Props: map[string]Value{"help": {Raw: "Run"}}},
			{Name: "flag", Args: []Value{{Raw: "-v --verbose"}}},
			{Name: "arg", Args: []Value{{Raw: "<file>"}}},
		},
	}

	if err := spec.Validate(WithRejectUnknownNodes()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestValidate_RequireCommandHelp(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "run"}},
				// No help
			},
		},
	}

	// Default: passes
	if err := spec.Validate(); err != nil {
		t.Errorf("default should pass: %v", err)
	}

	// With option: fails
	err := spec.Validate(WithRequireCommandHelp())
	if err == nil {
		t.Error("expected error with WithRequireCommandHelp")
	}
	if !strings.Contains(err.Error(), "help required") {
		t.Errorf("expected 'help required', got: %v", err)
	}
}

func TestValidate_DuplicateAlias(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "config"}},
				Props: map[string]Value{"help": {Raw: "Config"}},
				Children: []Node{
					{Name: "alias", Args: []Value{{Raw: "cfg"}, {Raw: "cfg"}}}, // duplicate
				},
			},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for duplicate alias")
	}
	if !strings.Contains(err.Error(), "duplicate alias") {
		t.Errorf("expected 'duplicate alias', got: %v", err)
	}
}

func TestValidate_DuplicateAliasNestedCommand(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "parent"}},
				Children: []Node{
					{
						Name: "cmd",
						Args: []Value{{Raw: "child"}},
						Props: map[string]Value{"help": {Raw: "Child"}},
						Children: []Node{
							{Name: "alias", Args: []Value{{Raw: "c"}, {Raw: "c"}}},
						},
					},
				},
			},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for duplicate alias in nested command")
	}
	if !strings.Contains(err.Error(), "cmd[parent child]") {
		t.Errorf("expected path 'cmd[parent child]', got: %v", err)
	}
}

func TestValidate_EmptyFlagUsage(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "flag", Args: []Value{{Raw: ""}}},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for empty flag usage")
	}
	if !strings.Contains(err.Error(), "usage is empty") {
		t.Errorf("expected 'usage is empty', got: %v", err)
	}
}

func TestValidate_InvalidFlagUsage(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{Name: "flag", Args: []Value{{Raw: "not a flag pattern"}}},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for invalid flag usage")
	}
	if !strings.Contains(err.Error(), "no short or long flag found") {
		t.Errorf("expected 'no short or long flag found', got: %v", err)
	}
}

func TestValidate_VarMinMax(t *testing.T) {
	min, max := 5, 2
	spec := &Spec{
		Nodes: []Node{
			{
				Name:  "flag",
				Args:  []Value{{Raw: "-v --verbose"}},
				Props: map[string]Value{"var_min": {Raw: min}, "var_max": {Raw: max}},
			},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for var_min > var_max")
	}
	if !strings.Contains(err.Error(), "var_min > var_max") {
		t.Errorf("expected 'var_min > var_max', got: %v", err)
	}
}

func TestValidate_NestedCommands(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name:  "cmd",
				Args:  []Value{{Raw: "config"}},
				Props: map[string]Value{"help": {Raw: "Config"}},
				Children: []Node{
					{
						Name: "cmd",
						Args: []Value{{Raw: "set"}},
						// No help - should fail with WithRequireCommandHelp
					},
				},
			},
		},
	}

	err := spec.Validate(WithRequireCommandHelp())
	if err == nil {
		t.Error("expected error for nested command without help")
	}
	if !strings.Contains(err.Error(), "cmd[config set]") {
		t.Errorf("expected path 'cmd[config set]', got: %v", err)
	}
}

func TestValidate_DuplicateCommands(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "cmd",
				Args: []Value{{Raw: "config"}},
			},
			{
				Name: "cmd",
				Args: []Value{{Raw: "config"}}, // duplicate top-level
			},
			{
				Name: "cmd",
				Args: []Value{{Raw: "parent"}},
				Children: []Node{
					{Name: "cmd", Args: []Value{{Raw: "child"}}},
					{Name: "cmd", Args: []Value{{Raw: "child"}}}, // duplicate subcommand
				},
			},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate commands")
	}
	if !strings.Contains(err.Error(), "duplicate command") {
		t.Errorf("expected duplicate command error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate subcommand") {
		t.Errorf("expected duplicate subcommand error, got: %v", err)
	}
}
func TestValidate_InvalidDoubleDash(t *testing.T) {
	spec := &Spec{
		Nodes: []Node{
			{
				Name: "arg",
				Args: []Value{{Raw: "<file>"}},
				Props: map[string]Value{
					"double_dash": {Raw: "bogus"},
				},
			},
		},
	}

	err := spec.Validate()
	if err == nil {
		t.Error("expected error for invalid double_dash value")
	}
	if !strings.Contains(err.Error(), "invalid double_dash value") {
		t.Errorf("expected 'invalid double_dash value', got: %v", err)
	}
}

func TestValidate_ValidDoubleDash(t *testing.T) {
	for _, dd := range []string{"required", "optional", "automatic", "preserve"} {
		spec := &Spec{
			Nodes: []Node{
				{
					Name: "arg",
					Args: []Value{{Raw: "<file>"}},
					Props: map[string]Value{
						"double_dash": {Raw: dd},
					},
				},
			},
		}

		if err := spec.Validate(); err != nil {
			t.Errorf("double_dash=%q should be valid, got: %v", dd, err)
		}
	}
}

func TestValidationError_Error(t *testing.T) {
	var nilErr *ValidationError
	if nilErr.Error() != "invalid spec" {
		t.Errorf("nil error should return 'invalid spec'")
	}

	err := &ValidationError{Problems: []string{"a", "b"}}
	if err.Error() != "invalid spec: a; b" {
		t.Errorf("got: %v", err.Error())
	}
}
