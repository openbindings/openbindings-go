package jsonvalue

import (
	"context"
	"github.com/openbindings/openbindings-go/internal/value"
)

// MarshalOptions bounds explicit JSON export. Units are 64 per logical node
// plus escaped string/key and number token lengths, not a process heap bound.
// Zero selects 64 MiB of units and nesting depth 256; negatives are invalid.
type MarshalOptions struct {
	MaxValueUnits int64
	MaxDepth      int
}

// MarshalWithOptions produces JSON on explicit request under finite value,
// depth and codec scratch limits. Custom encoders retain their meaning and
// responsibility for their own allocations. Ordinary Marshal is unchanged.
func MarshalWithOptions(input any, options MarshalOptions) ([]byte, error) {
	opts := value.Options{Limits: value.Limits{MaxUnits: options.MaxValueUnits, MaxDepth: options.MaxDepth}}
	s, err := value.Capture(context.Background(), input, opts)
	if err != nil {
		return nil, err
	}
	return s.Export(context.Background(), opts)
}
