package value_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

type label string

func (s label) MarshalText() ([]byte, error) { return []byte("label:" + string(s)), nil }
func (s *label) UnmarshalText(b []byte) error {
	*s = label(bytes.TrimPrefix(b, []byte("label:")))
	return nil
}

type custom struct {
	Calls  *int
	Hidden any
}

func (c custom) MarshalJSON() ([]byte, error) {
	*c.Calls++
	return []byte(`{"n":123,"text":"\ud800"}`), nil
}

type embedded struct {
	Name string `json:"name"`
}
type record struct {
	embedded
	Data   []byte      `json:"data"`
	Omit   string      `json:"omit,omitempty"`
	Number json.Number `json:"number"`
}
type namedByte uint8

func normalized(t *testing.T, b []byte) any {
	t.Helper()
	var x any
	if err := jsonvalue.Unmarshal(b, &x); err != nil {
		t.Fatal(err)
	}
	return x
}
func TestProjectionMatchesMaintainedCodec(t *testing.T) {
	calls := 0
	cases := []any{
		nil, []any(nil), map[string]any(nil), []byte(nil), []byte{}, []any{}, map[string]any{},
		record{embedded: embedded{"cat"}, Data: []byte{0, 1, 254}, Number: "9007199254740993"},
		struct {
			X string `json:"x,string"`
		}{"quoted \" value"},
		map[int]any{2: label("hi"), 1: json.Number("1e+100")},
		custom{&calls, make(chan int)}, float32(0.1), math.Copysign(0, -1), uint64(math.MaxUint64),
		map[string]any{"<key>": "\u2028\n\t\x01", "bad": "\xff", "lone": "\xed\xa0\x80"},
		[]namedByte{1, 2, 3}, [3]byte{1, 2, 3},
	}
	for _, input := range cases {
		t.Run(reflect.TypeOf(&input).String(), func(t *testing.T) {
			s, err := value.Capture(context.Background(), input, value.Options{})
			if err != nil {
				t.Fatalf("%T capture: %v", input, err)
			}
			got, err := s.Export(context.Background(), value.Options{})
			if err != nil {
				t.Fatal(err)
			}
			want, err := jsonvalue.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalized(t, got), normalized(t, want)) {
				t.Fatalf("%T\ngot %s\nwant %s", input, got, want)
			}
		})
	}
	if calls != 2 {
		t.Fatalf("custom encoder invoked %d times, want one per capture plus oracle", calls)
	}
}
func checkConstruction[T any](t *testing.T, input any) {
	t.Helper()
	s, err := value.Capture(context.Background(), input, value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, gerr := value.Construct[T](context.Background(), s, value.Options{})
	raw, err := jsonvalue.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var want T
	werr := jsonvalue.Unmarshal(raw, &want)
	if (gerr == nil) != (werr == nil) {
		t.Fatalf("%T errors: got %v want %v; input %s", want, gerr, werr, raw)
	}
	if gerr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
func TestCheckedConstructionParity(t *testing.T) {
	checkConstruction[record](t, map[string]any{"name": "cat", "data": "AQID", "number": json.Number("9007199254740993")})
	checkConstruction[struct{ Name string }](t, map[string]any{"NAME": "first", "Name": "last"})
	checkConstruction[map[int]label](t, map[string]any{"7": "label:cat"})
	checkConstruction[[]namedByte](t, "AQID")
	checkConstruction[[2]int](t, []int{1, 2, 3})
	checkConstruction[[]int](t, []any{})
	checkConstruction[*int](t, nil)
	checkConstruction[int](t, json.Number("1.5"))
	checkConstruction[uint8](t, json.Number("256"))
	checkConstruction[float32](t, json.Number("0.1"))
	checkConstruction[struct {
		N int `json:"n,string"`
	}](t, map[string]any{"n": "12"})
	checkConstruction[any](t, []byte{1, 2, 3})
}
func TestSnapshotsAndDuplicateConstructionOwnStorage(t *testing.T) {
	input := map[string]any{"items": []any{map[string]any{"name": "before"}}, "image": []byte{1, 2, 3}}
	s, err := value.Capture(context.Background(), input, value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	input["image"].([]byte)[0] = 9
	input["items"].([]any)[0].(map[string]any)["name"] = "after"
	x, err := s.Logical(context.Background(), value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := x.(map[string]any)
	if m["image"] != "AQID" || m["items"].([]any)[0].(map[string]any)["name"] != "before" {
		t.Fatalf("snapshot changed: %v", m)
	}
	shared := []byte{1, 2, 3}
	s, err = value.Capture(context.Background(), map[string]any{"a": shared, "b": shared}, value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := value.Construct[struct{ A, B []byte }](context.Background(), s, value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out.A[0] = 8
	if out.B[0] != 1 || shared[0] != 1 {
		t.Fatal("duplicate occurrences alias")
	}
}
func TestLimitsAndFailureRelease(t *testing.T) {
	var held int64
	options := value.Options{Limits: value.Limits{MaxUnits: 512, MaxDepth: 8}, Adjust: func(n int64) error {
		held += n
		if held < 0 {
			t.Fatal("negative account")
		}
		return nil
	}}
	_, err := value.Capture(context.Background(), make([]any, 20), options)
	var limit *value.LimitError
	if !errors.As(err, &limit) || held != 0 {
		t.Fatalf("limit/release: %v held=%d", err, held)
	}
	s, err := value.Capture(context.Background(), map[string]any{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if held != s.Cost() {
		t.Fatalf("held %d cost %d", held, s.Cost())
	}
	type padded struct {
		Ignored [1 << 20]byte `json:"-"`
	}
	_, err = value.Construct[padded](context.Background(), s, options)
	if !errors.As(err, &limit) || held != s.Cost() {
		t.Fatalf("padded destination: %v held=%d", err, held)
	}
	options.Adjust(-s.Cost())
	cycle := map[string]any{}
	cycle["x"] = cycle
	for _, x := range []any{cycle, math.NaN(), map[string]any{"unread": json.Number("")}} {
		if _, err = value.Capture(context.Background(), x, options); err == nil {
			t.Fatal("invalid input admitted")
		}
		if held != 0 {
			t.Fatalf("leaked %d", held)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = value.Capture(ctx, []byte{1}, options); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestConversionFailureReturnsZero(t *testing.T) {
	s, err := value.Capture(context.Background(), map[string]any{"a": 1, "b": "bad"}, value.Options{})
	if err != nil {
		t.Fatal(err)
	}
	type target struct{ A, B int }
	out, err := value.Construct[target](context.Background(), s, value.Options{})
	if err == nil || out != (target{}) {
		t.Fatalf("partial target exposed: %v %v", out, err)
	}
}

func TestFallbackDoesNotRepairInvalidNumber(t *testing.T) {
	calls := 0
	input := struct {
		Number json.Number
		Custom custom
	}{"", custom{Calls: &calls}}
	if _, err := value.Capture(context.Background(), input, value.Options{}); err == nil {
		t.Fatal("fallback repaired invalid number")
	}
	if calls != 0 {
		t.Fatalf("codec invoked before carrier refusal: %d", calls)
	}
	if _, err := value.Capture(context.Background(), struct {
		N json.Number `json:"n,omitempty"`
	}{}, value.Options{}); err == nil {
		t.Fatal("omitempty repaired invalid number")
	}
}
func TestDepthAndStringConstructionParity(t *testing.T) {
	checkConstruction[struct{ S string }](t, map[string]any{"S": "\xed\xa0\x80"})
	checkConstruction[map[string]string](t, map[string]any{"\xed\xa0\x80": "\xed\xb0\x80"})
	for _, input := range []any{[]any{[]any{1}}, map[string]any{"a": map[string]any{"b": 1}}} {
		if _, err := value.Capture(context.Background(), input, value.Options{Limits: value.Limits{MaxDepth: 2}}); err == nil {
			t.Fatal("depth refusal missing")
		}
	}
}

// Malformed/deep input is bounded before capture; successful cases must preserve
// the maintained codec's logical meaning through both construction and export.
func FuzzLogicalValueRoundTrip(f *testing.F) {
	for _, seed := range []string{`null`, `{"n":9007199254740993,"s":"\ud800","b":"AAEC"}`, `[1e400,[],{},null]`, `{"x":1,"x":2}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		var input any
		if jsonvalue.Unmarshal([]byte(raw), &input) != nil {
			return
		}
		s, err := value.Capture(context.Background(), input, value.Options{Limits: value.Limits{MaxUnits: 1 << 20, MaxDepth: 256}})
		if err != nil {
			var limit *value.LimitError
			if errors.As(err, &limit) {
				return
			}
			t.Fatal(err)
		}
		out, err := value.Construct[any](context.Background(), s, value.Options{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonvalue.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		exported, err := s.Export(context.Background(), value.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized(t, encoded), input) || !reflect.DeepEqual(normalized(t, exported), input) {
			t.Fatalf("changed logical meaning: %q -> %s / %s", raw, encoded, exported)
		}
	})
}

func FuzzTypedProjectionParity(f *testing.F) {
	f.Add("sample", int64(9007199254740993), []byte{0, 255, 128})
	f.Fuzz(func(t *testing.T, name string, n int64, data []byte) {
		if len(name)+len(data) > 4096 {
			t.Skip()
		}
		type row struct {
			Name string `json:"name"`
			N    int64  `json:"n"`
			Data []byte `json:"data"`
		}
		input := row{name, n, data}
		s, err := value.Capture(context.Background(), input, value.Options{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := s.Export(context.Background(), value.Options{})
		if err != nil {
			t.Fatal(err)
		}
		want, err := jsonvalue.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized(t, encoded), normalized(t, want)) {
			t.Fatalf("got %s want %s", encoded, want)
		}
		checkConstruction[row](t, input)
	})
}
