package openbindings

import "testing"

func TestCheckInterfaceCompatibility_FullyCompatible(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Kind: OperationKindMethod,
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
				Kind: OperationKindMethod,
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
			"getStatus": {Kind: OperationKindMethod},
			"restart":   {Kind: OperationKindMethod},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {Kind: OperationKindMethod},
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

func TestCheckInterfaceCompatibility_OutputIncompatible(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {
				Kind: OperationKindMethod,
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
			"getStatus": {Kind: OperationKindMethod},
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

func TestCheckInterfaceCompatibility_InputIncompatible(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"search": {
				Kind: OperationKindMethod,
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
			"search": {Kind: OperationKindMethod},
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

func TestCheckInterfaceCompatibility_ProviderHasExtras(t *testing.T) {
	required := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {Kind: OperationKindMethod},
		},
	}
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getStatus": {Kind: OperationKindMethod},
			"restart":   {Kind: OperationKindMethod},
		},
	}

	issues := CheckInterfaceCompatibility(required, provided)
	if len(issues) != 0 {
		t.Fatalf("expected no issues (provider can have extras), got %d: %+v", len(issues), issues)
	}
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
