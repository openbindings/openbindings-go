package usage

import (
	"context"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const FormatToken = "usage@^2.0.0"

const DefaultSourceName = "usage"

// Invoker handles binding invocation for usage-spec KDL sources.
type Invoker struct {
	specCache sync.Map // map[string]*Spec
}

// NewInvoker creates a new usage binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{}
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)

// cachedLoadSpec loads a usage spec, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Invoker) cachedLoadSpec(ctx context.Context, location string, content any) (*Spec, error) {
	if location != "" && content == nil {
		if cached, ok := e.specCache.Load(location); ok {
			return cached.(*Spec), nil
		}
	}

	spec, err := loadSpec(ctx, location, content)
	if err != nil {
		return nil, err
	}

	if location != "" {
		e.specCache.Store(location, spec)
	}
	return spec, nil
}

// Formats returns the source formats supported by the usage invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "CLI tools via usage-spec KDL"}}
}

// InvokeBinding runs a CLI command for a usage-spec binding and returns the
// invocation handle synchronously; the process runs on its own goroutine.
// The command's flag/arg object flows through the handle's Write channel; the
// process output is emitted as one output. Credentials/configuration travel
// as environment variables in the binding context (the well-known
// "environment" field), not as a separate security mechanism.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

// Synthesizer handles interface creation from usage specs.
type Synthesizer struct{}

// NewSynthesizer creates a new usage interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{}
}

// Formats returns the source formats supported by the usage synthesizer.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "CLI tools via usage-spec KDL"}}
}

// SynthesizeInterface converts a usage spec to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	spec, err := loadSpec(ctx, src.Location, src.Content)
	if err != nil {
		return nil, err
	}

	iface, err := convertToInterfaceWithSpec(spec, src.Location)
	if err != nil {
		return nil, err
	}

	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}

	return &iface, nil
}
