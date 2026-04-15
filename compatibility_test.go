package openbindings

import "testing"

func TestCheckInterfaceCompatibility_FullyCompatible(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
					},
					"required": []any{"status"},
				},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"status":  map[string]any{"type": "string"},
						"uptime":  map[string]any{"type": "number"},
					},
					"required": []any{"status"},
				},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_MissingOperation(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {},
			"restart":   {},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Kind != CompatibilityMissing {
		t.Fatalf("expected missing, got %s", issues[0].Kind)
	}
	if issues[0].Operation != "restart" {
		t.Fatalf("expected operation 'restart', got %s", issues[0].Operation)
	}
}

func TestCheckInterfaceCompatibility_OutputUnspecifiedSkipped(t *testing.T) {
	// Per spec: absent/null schemas are "unspecified" and skipped in compatibility.
	// Required has output, provided has no output (nil) → skip, not incompatible.
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
					},
					"required": []any{"status"},
				},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues (unspecified output is skipped), got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_OutputIncompatible(t *testing.T) {
	// Both sides specify output; provided output doesn't satisfy required.
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
					},
					"required": []any{"status"},
				},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{"type": "array"},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Kind != CompatibilityOutputIncompatible {
		t.Fatalf("expected output_incompatible, got %s", issues[0].Kind)
	}
}

func TestCheckInterfaceCompatibility_InputUnspecifiedSkipped(t *testing.T) {
	// Per spec: absent/null schemas are "unspecified" and skipped in compatibility.
	// Required has input, provided has no input (nil) → skip, not incompatible.
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"search": {
				Input: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"search": {},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues (unspecified input is skipped), got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_InputIncompatible(t *testing.T) {
	// Both sides specify input; provided is more restrictive than required.
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"search": {
				Input: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"search": {
				Input: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
					"required": []any{"query", "limit"},
				},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Kind != CompatibilityInputIncompatible {
		t.Fatalf("expected input_incompatible, got %s", issues[0].Kind)
	}
}

func TestCheckInterfaceCompatibility_ProvidedHasExtras(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {},
			"restart":   {},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues (provided can have extras), got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_SatisfiesMatch(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"listWorkspaces": {},
			"getWorkspace":   {},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"myListOp": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "listWorkspaces"},
				},
			},
			"myGetOp": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "getWorkspace"},
				},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided,
		CheckCompatibilityOptions{RequiredInterfaceID: "openbindings.workspace-manager"})
	if len(issues) != 0 {
		t.Fatalf("expected no issues with satisfies match, got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_SatisfiesMatchRequiresInterfaceID(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"listWorkspaces": {},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"myListOp": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "listWorkspaces"},
				},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue without interface ID, got %d: %+v", len(issues), issues)
	}
	if issues[0].Kind != CompatibilityMissing {
		t.Fatalf("expected missing, got %s", issues[0].Kind)
	}
}

func TestCheckInterfaceCompatibility_AliasesMatch(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"listWorkspaces": {},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"listProjects": {
				Aliases: []string{"listWorkspaces", "listRepos"},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues with aliases match, got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_DirectKeyTakesPrecedence(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{"type": "object"},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: JSONSchema{"type": "object"},
			},
			"statusAlias": {
				Aliases: []string{"getStatus"},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues (direct key match), got %d: %+v", len(issues), issues)
	}
}

func TestCheckInterfaceCompatibility_EmptySchemaDistinctFromAbsent(t *testing.T) {
	// Per spec: {} is "accepts anything" (Top), distinct from absent/null (unspecified).
	// Empty schemas must be checked, not skipped.

	t.Run("both empty output schemas are compatible", func(t *testing.T) {
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{}},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 0 {
			t.Fatalf("expected 0 issues (both Top), got %d: %+v", len(issues), issues)
		}
	})

	t.Run("empty output target with constrained candidate is compatible", func(t *testing.T) {
		// Required output {} = Top (accepts anything). Provided output is constrained.
		// For output: candidate must be subset of target. Any type ⊆ Top. Compatible.
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{"type": "string"}},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 0 {
			t.Fatalf("expected 0 issues, got %d: %+v", len(issues), issues)
		}
	})

	t.Run("constrained output target with empty candidate is incompatible", func(t *testing.T) {
		// Required output is constrained. Provided output {} = Top (unconstrained).
		// For output: candidate Top is not subset of constrained target. Incompatible.
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{"type": "string"}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: JSONSchema{}},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
		}
		if issues[0].Kind != CompatibilityOutputIncompatible {
			t.Fatalf("expected output_incompatible, got %s", issues[0].Kind)
		}
	})

	t.Run("empty input target with constrained candidate is incompatible", func(t *testing.T) {
		// Required input {} = Top (the interface may send any value).
		// A constrained candidate (type: string) cannot handle all values, so incompatible.
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Input: JSONSchema{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {
					Input: JSONSchema{
						"type": "string",
					},
				},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
		}
		if issues[0].Kind != CompatibilityInputIncompatible {
			t.Fatalf("expected input_incompatible, got %s", issues[0].Kind)
		}
	})

	t.Run("constrained input target with empty candidate is compatible", func(t *testing.T) {
		// Required input is constrained. Provided input {} = Top (accepts everything).
		// For input: candidate accepts everything, which includes what target accepts. Compatible.
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {
					Input: JSONSchema{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
					},
				},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Input: JSONSchema{}},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 0 {
			t.Fatalf("expected 0 issues, got %d: %+v", len(issues), issues)
		}
	})

	t.Run("both empty input schemas are compatible", func(t *testing.T) {
		required := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Input: JSONSchema{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Input: JSONSchema{}},
			},
		}
		issues := CheckInterfaceCompatibility(required, provided)
		if len(issues) != 0 {
			t.Fatalf("expected 0 issues (both Top), got %d: %+v", len(issues), issues)
		}
	})
}

func TestIsOBInterface(t *testing.T) {
	tests := []struct {
		name string
		v    map[string]any
		want bool
	}{
		{
			name: "valid",
			v:    map[string]any{"openbindings": "0.1.0", "operations": map[string]any{"op": map[string]any{}}},
			want: true,
		},
		{
			name: "nil",
			v:    nil,
			want: false,
		},
		{
			name: "missing openbindings",
			v:    map[string]any{"operations": map[string]any{}},
			want: false,
		},
		{
			name: "missing operations",
			v:    map[string]any{"openbindings": "0.1.0"},
			want: false,
		},
		{
			name: "operations not a map",
			v:    map[string]any{"openbindings": "0.1.0", "operations": "nope"},
			want: false,
		},
		{
			name: "openbindings not a string",
			v:    map[string]any{"openbindings": 123, "operations": map[string]any{}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOBInterface(tt.v)
			if got != tt.want {
				t.Fatalf("IsOBInterface() = %v, want %v", got, tt.want)
			}
		})
	}
}
