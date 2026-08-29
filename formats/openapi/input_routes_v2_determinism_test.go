package openapi

import "testing"

// Synthesized transform expressions must be byte-stable across runs: consumers
// (ob's boundgen freshness gate among them) hash generated OBI documents, so a
// map-iteration ordering leak in any emitted expression is a defect even when
// every ordering is semantically equivalent.
func TestTransformExpressionIsDeterministic(t *testing.T) {
	routes := abstractInputRoutes{
		bodyFields: map[string]string{
			"gamma": "gamma", "alpha": "alpha", "beta": "beta",
			"delta": "delta", "epsilon": "epsilon", "zeta": "zeta",
		},
	}
	first := routes.transformExpression(nil)
	for i := 0; i < 32; i++ {
		if got := routes.transformExpression(nil); got != first {
			t.Fatalf("transformExpression is nondeterministic:\nfirst: %s\n  got: %s", first, got)
		}
	}
}
