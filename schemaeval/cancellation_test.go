package schemaeval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/schemaeval"
)

// A deadline bounds how long Validate waits, even for a schema whose
// evaluation doubles with each level: Validate returns the ctx's error once
// the ctx is done, and core reports it as no verdict, never a verdict.
func TestValidate_ReturnsWhenTheCtxEnds(t *testing.T) {
	const levels = 20
	schemas := map[string]any{fmt.Sprintf("S%d", levels): map[string]any{"type": "string"}}
	for i := 0; i < levels; i++ {
		next := map[string]any{"$ref": fmt.Sprintf("#/schemas/S%d", i+1)}
		schemas[fmt.Sprintf("S%d", i)] = map[string]any{"allOf": []any{next, next}}
	}
	data, err := json.Marshal(map[string]any{
		"openbindings": "0.2.0",
		"schemas":      schemas,
		"operations":   map[string]any{"op": map[string]any{"input": map[string]any{"$ref": "#/schemas/S0"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openbindings.ParseDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := compiler.Resolve(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := contracts.CompileInput(context.Background(), "op")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = contract.ValidateJSON(ctx, []byte(`"x"`))
	elapsed := time.Since(start)
	if !errors.Is(err, openbindings.ErrNoVerdict) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, openbindings.ErrMismatch) {
		t.Fatalf("a done ctx: %v, want no verdict holding the ctx's error", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Validate returned %v after a 20ms deadline", elapsed)
	}
}
