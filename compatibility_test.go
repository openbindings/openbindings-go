package openbindings

import "testing"

func TestCheckInterfaceCompatibility_FullyCompatible(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: map[string]any{
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
				Output: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
						"uptime": map[string]any{"type": "number"},
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

func TestCheckInterfaceCompatibility_NilProvidedIssuesSorted(t *testing.T) {
	// The nil-provided lane must emit missing issues in sorted operation
	// order, like the main lane — issue order must never leak Go map
	// iteration order. Repeated runs make map-order leakage overwhelmingly
	// likely to surface.
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"epsilon": {}, "delta": {}, "charlie": {}, "bravo": {}, "alpha": {},
		},
	}
	want := []string{"alpha", "bravo", "charlie", "delta", "epsilon"}
	for run := 0; run < 20; run++ {
		issues := CheckInterfaceCompatibility(required, nil)
		if len(issues) != len(want) {
			t.Fatalf("expected %d issues, got %d: %+v", len(want), len(issues), issues)
		}
		for i, issue := range issues {
			if issue.Kind != CompatibilityMissing {
				t.Fatalf("expected missing, got %s", issue.Kind)
			}
			if issue.Operation != want[i] {
				t.Fatalf("run %d: issue %d: expected operation %q, got %q (issues must be sorted)", run, i, want[i], issue.Operation)
			}
		}
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
				Output: map[string]any{
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
				Output: map[string]any{
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
				Output: map[string]any{"type": "array"},
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
				Input: map[string]any{
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
				Input: map[string]any{
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
				Input: map[string]any{
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

func TestCheckInterfaceCompatibility_AliasCorrespondenceMatch(t *testing.T) {
	// An implementation claims to fulfill a shared contract by carrying the
	// contract's operation names as aliases on its own operations.
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
			"myListOp": {Aliases: []string{"listWorkspaces"}},
			"myGetOp":  {Aliases: []string{"getWorkspace"}},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues with alias correspondence, got %d: %+v", len(issues), issues)
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
				Output: map[string]any{"type": "object"},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Output: map[string]any{"type": "object"},
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
				"op": {Output: map[string]any{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: map[string]any{}},
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
				"op": {Output: map[string]any{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: map[string]any{"type": "string"}},
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
				"op": {Output: map[string]any{"type": "string"}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Output: map[string]any{}},
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
				"op": {Input: map[string]any{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {
					Input: map[string]any{
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
					Input: map[string]any{
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
				"op": {Input: map[string]any{}},
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
				"op": {Input: map[string]any{}},
			},
		}
		provided := &Interface{
			OpenBindings: "0.1.0",
			Operations: map[string]Operation{
				"op": {Input: map[string]any{}},
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

// Schema $refs resolve against each interface's OWN document: the required
// and provided sides both point through their schemas section (different
// names per side, so a shared root could not resolve both), and the
// comparison sees the dereferenced shapes. Regression: an unrooted shared
// normalizer turned every "#/schemas/..." ref into a RefError and reported
// the pair incompatible.
func TestCheckInterfaceCompatibility_SchemaRefsResolvePerSide(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"ReqStatus": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string"},
				},
				"required": []any{"status"},
			},
			"ReqInput": map[string]any{"type": "string"},
		},
		Operations: map[string]Operation{
			"getStatus": {
				Input:  map[string]any{"$ref": "#/schemas/ReqInput"},
				Output: map[string]any{"$ref": "#/schemas/ReqStatus"},
			},
		},
	}
	provided := &Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"ProvStatus": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string"},
				},
				"required": []any{"status"},
			},
			"ProvInput": map[string]any{"type": "string"},
		},
		Operations: map[string]Operation{
			"getStatus": {
				Input:  map[string]any{"$ref": "#/schemas/ProvInput"},
				Output: map[string]any{"$ref": "#/schemas/ProvStatus"},
			},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues (per-side $ref resolution), got %d: %+v", len(issues), issues)
	}

	// The refs must actually DEREFERENCE (a check that silently skipped
	// unresolvable refs would also report zero issues): an incompatible
	// shape behind the provided ref is caught.
	provided.Schemas["ProvStatus"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "number"},
		},
		"required": []any{"status"},
	}
	issues = CheckInterfaceCompatibility(required, provided)
	if len(issues) != 1 || issues[0].Kind != CompatibilityOutputIncompatible {
		t.Fatalf("expected one output_incompatible issue through the refs, got %+v", issues)
	}
}
