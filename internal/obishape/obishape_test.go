package obishape

import "testing"

func TestLooksLikeOBI(t *testing.T) {
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
			got := LooksLikeOBI(tt.v)
			if got != tt.want {
				t.Fatalf("LooksLikeOBI() = %v, want %v", got, tt.want)
			}
		})
	}
}
