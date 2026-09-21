package json

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type boundedFragment struct {
	text  string
	calls *int
}

func (f boundedFragment) MarshalJSON() ([]byte, error) { *f.calls++; return []byte(f.text), nil }

type boundedText string

func (s boundedText) MarshalText() ([]byte, error) { return []byte(s), nil }
func TestBoundedEncoderChecksInsideFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		limit EncodeLimits
		kind  string
	}{
		{"bytes", strings.Repeat("x", 128), EncodeLimits{MaxBytes: 64, MaxNodes: 64, MaxDepth: 8}, "bytes"},
		{"text", boundedText(strings.Repeat("x", 128)), EncodeLimits{MaxBytes: 64, MaxNodes: 64, MaxDepth: 8}, "bytes"},
		{"nodes", []int{1, 2, 3}, EncodeLimits{MaxBytes: 1024, MaxNodes: 3, MaxDepth: 8}, "nodes"},
		{"depth", [][]int{{1}}, EncodeLimits{MaxBytes: 1024, MaxNodes: 64, MaxDepth: 2}, "depth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MarshalBounded(context.Background(), tc.input, tc.limit)
			var limit *EncodeLimitError
			if !errors.As(err, &limit) || limit.Kind != tc.kind {
				t.Fatalf("%v", err)
			}
		})
	}
	calls := 0
	_, err := MarshalBounded(context.Background(), boundedFragment{"[[[0]]]", &calls}, EncodeLimits{MaxBytes: 1024, MaxNodes: 64, MaxDepth: 2})
	if err == nil || calls != 1 {
		t.Fatalf("calls %d err %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MarshalBounded(ctx, 1, EncodeLimits{MaxBytes: 1024, MaxNodes: 64, MaxDepth: 8}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	refusal := errors.New("no scratch")
	_, err = MarshalBounded(context.Background(), "x", EncodeLimits{MaxBytes: 1024, MaxNodes: 64, MaxDepth: 8, Account: func(int64) error { return refusal }})
	if !errors.Is(err, refusal) {
		t.Fatal(err)
	}
}
func TestCompleteValueDecoderDoesNotRefill(t *testing.T) {
	for _, raw := range []string{`0`, `{"a":[1,"x",null]}`, `"hello"`} {
		data := []byte(raw)
		before := cap(data)
		d := NewValueDecoder(data)
		var got any
		if err := d.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if cap(d.buf) != before {
			t.Fatal("allocated input buffer")
		}
		if err := d.Decode(&got); err != io.EOF {
			t.Fatal(err)
		}
	}
}
