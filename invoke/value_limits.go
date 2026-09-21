package invoke

import (
	"context"
	"errors"

	codec "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
)

// ValueLimits bounds one invocation's SDK-controlled value work and retention.
// Units are 64 per logical node plus escaped string/key and number token lengths,
// not heap bytes. Zero inherits defaults: 64 MiB/value, 256 MiB live, depth 256.
// Retries and SDK Graph children share the resolved limits and live budget.
type ValueLimits struct {
	MaxValueUnits, MaxLiveUnits int64
	MaxDepth                    int
}

// Validate checks configuration without starting an invocation.
func (l ValueLimits) Validate() error { _, err := l.resolve(); return err }

func limitsFromScope(s *valueio.Scope) ValueLimits {
	l := s.Limits()
	return ValueLimits{MaxValueUnits: l.MaxValueUnits, MaxLiveUnits: l.MaxLiveUnits, MaxDepth: l.MaxDepth}
}

// InvocationValueOption propagates a binding call's process-local limits.
func (a *BindingInvocationArgs) InvocationValueOption() InvocationOption {
	if a == nil {
		return WithInvocationValueLimits(ValueLimits{})
	}
	return WithInvocationValueLimits(a.ValueLimits)
}

func (l ValueLimits) internal() valueio.Limits {
	return valueio.Limits{MaxValueUnits: l.MaxValueUnits, MaxLiveUnits: l.MaxLiveUnits, MaxDepth: l.MaxDepth}
}
func (l ValueLimits) resolve() (valueio.Limits, error) { return l.internal().Resolve() }
func mergeValueLimits(base, override ValueLimits) ValueLimits {
	if override.MaxValueUnits != 0 {
		base.MaxValueUnits = override.MaxValueUnits
	}
	if override.MaxLiveUnits != 0 {
		base.MaxLiveUnits = override.MaxLiveUnits
	}
	if override.MaxDepth != 0 {
		base.MaxDepth = override.MaxDepth
	}
	return base
}

// InvocationOption configures a low-level invocation or a foreign typed adapter.
type InvocationOption func(*invocationOptions)
type invocationOptions struct{ limits ValueLimits }

func WithInvocationValueLimits(limits ValueLimits) InvocationOption {
	return func(o *invocationOptions) { o.limits = limits }
}

// WithValueLimits overrides process-local value limits for this operation call.
func WithValueLimits(limits ValueLimits) InvokeOption {
	return func(c *invokeConfig) { c.valueLimits = limits }
}

// ValueLimitError describes a local resource refusal without payload data.
// It remains outside portable InvocationError.Data and JSON encoding.
type ValueLimitError = value.LimitError

// ValueConversionError is a failure to construct one public output. That logical
// output is consumed; later outputs and the invocation's terminal remain readable.
// Cause supports errors.As to InvocationError; no partial destination is exposed.
type ValueConversionError struct {
	Stage string
	Cause error
}

func (e *ValueConversionError) Error() string {
	return "openbindings: output value conversion failed (" + e.Stage + ")"
}
func (e *ValueConversionError) Unwrap() error { return e.Cause }

func valueError(err error, stage string) *InvocationError {
	code := ErrCodeTypeMismatch
	var limit *value.LimitError
	var encode *codec.EncodeLimitError
	if errors.As(err, &limit) || errors.As(err, &encode) {
		code = ErrCodeRuntime
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = ErrCodeCancelled
	}
	ie := &InvocationError{Code: code}
	if limit != nil {
		ie.cause = limit
	} else if encode != nil {
		ie.cause = &value.LimitError{Stage: stage, Kind: encode.Kind, Allowance: encode.Limit}
	}
	return ie
}
func resourceFailure(err error) bool {
	var l *value.LimitError
	var c *codec.EncodeLimitError
	return errors.As(err, &l) || errors.As(err, &c)
}

// invocationValueAccess is private; the promoted bridge accessor is sealed in
// internal/valueio, adding no public method to the ordinary invocation API.
type invocationValueAccess = valueio.Access
