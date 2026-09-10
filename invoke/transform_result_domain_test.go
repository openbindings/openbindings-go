package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type domainEvaluator struct {
	value any
	err   error
}

func (e domainEvaluator) Evaluate(context.Context, string, any) (any, error) { return e.value, e.err }

func TestTransformResultJSONDomain(t *testing.T) {
	invalid := []struct {
		name  string
		value any
		err   error
	}{
		{"infinity", math.Inf(1), nil}, {"negative_infinity", math.Inf(-1), nil},
		{"nan", math.NaN(), nil}, {"nested", map[string]any{"values": []any{math.Inf(1)}}, nil},
		{"function", func() {}, nil}, {"undefined", nil, ErrTransformUndefined},
		{"invalid_number", json.Number("NaN"), nil},
	}
	for _, direction := range []string{"input", "output"} {
		for _, referenced := range []bool{false, true} {
			for _, schema := range []bool{false, true} {
				for _, fixture := range invalid {
					t.Run(direction+"/"+fixture.name+"/"+map[bool]string{true: "ref", false: "inline"}[referenced]+"/"+map[bool]string{true: "schema", false: "absent"}[schema], func(t *testing.T) {
						iface := opTestInterface()
						entry := iface.Bindings["echo.transformed"]
						entry.InputTransform = nil
						transform := &openbindings.TransformOrRef{Inline: "$"}
						if referenced {
							transform = &openbindings.TransformOrRef{Ref: "#/transforms/map"}
							iface.Transforms = map[string]openbindings.Transform{"map": "$"}
						}
						if direction == "input" {
							entry.InputTransform = transform
						} else {
							entry.OutputTransform = transform
						}
						iface.Bindings["echo.transformed"] = entry
						if schema {
							iface.Operations["echo"] = openbindings.Operation{Input: map[string]any{}, Output: map[string]any{}}
						}
						mock := &mockBindingInvoker{}
						op := newOpInvoker(mock, nil)
						op.TransformEvaluator = domainEvaluator{fixture.value, fixture.err}
						call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("echo"))
						_ = call.Write(shortCtx(t), map[string]any{"id": "1"})
						values, err := drainOutputs(t, call)
						if codeOf(t, err) != ErrCodeTransformError || len(values) != 0 {
							t.Fatalf("invalid transform escaped: values=%#v error=%v", values, err)
						}
						attempts, _, reads, _ := mock.snapshot()
						if attempts != 1 {
							t.Fatalf("attempts=%d", attempts)
						}
						if direction == "input" && len(reads[0]) != 0 {
							t.Fatalf("invalid input reached binding: %#v", reads)
						}
					})
				}
			}
		}
	}
}

func TestTransformResultJSONControls(t *testing.T) {
	for _, value := range []any{nil, false, "", float64(0), []any{float64(1)}, map[string]any{"n": json.Number("1e400")}, json.Number("9223372036854775807")} {
		result, err := applyTransformRef(context.Background(), domainEvaluator{value: value}, nil, &openbindings.TransformOrRef{Inline: "$"}, nil)
		if err != nil {
			t.Fatalf("valid JSON result rejected: %#v: %v", value, err)
		}
		before, _ := json.Marshal(value)
		after, _ := json.Marshal(result)
		if string(before) != string(after) {
			t.Fatalf("result changed: %s -> %s", before, after)
		}
	}
	_, err := applyTransformRef(context.Background(), domainEvaluator{err: ErrTransformUndefined}, nil, &openbindings.TransformOrRef{Inline: "$"}, nil)
	if !errors.Is(err, ErrTransformUndefined) {
		t.Fatalf("undefined sentinel lost: %v", err)
	}
}

type streamDomainEvaluator struct{}

func (streamDomainEvaluator) Evaluate(_ context.Context, _ string, data any) (any, error) {
	if data.(map[string]any)["bad"] == true {
		return math.Inf(1), nil
	}
	return data, nil
}

func TestTransformResultJSONStreamKeepsPriorOutput(t *testing.T) {
	iface := opTestInterface()
	binding := iface.Bindings["watchTyped.main"]
	binding.OutputTransform = &openbindings.TransformOrRef{Inline: "$"}
	iface.Bindings["watchTyped.main"] = binding
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	op.TransformEvaluator = streamDomainEvaluator{}
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("watchTyped"))
	_ = call.Write(shortCtx(t), map[string]any{})
	values, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeTransformError || len(values) != 1 || values[0].(map[string]any)["n"] != float64(1) {
		t.Fatalf("values=%#v error=%v", values, err)
	}
	attempts, _, _, _ := mock.snapshot()
	if attempts != 1 {
		t.Fatalf("unexpected replay: attempts=%d", attempts)
	}
}
