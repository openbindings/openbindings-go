package usage

import (
	"context"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const DefaultSourceName = "usage"

// Invoker handles binding invocation for openbindings.usage wrapper
// documents (spec/formats/usage/openbindings.usage.md).
type Invoker struct {
	wrapperCache sync.Map // map[string]*Wrapper
}

// NewInvoker creates a new usage binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{}
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)

// cachedLoadWrapper loads a wrapper document, caching by location within a
// process. When content is provided, the cache is bypassed and updated.
func (e *Invoker) cachedLoadWrapper(ctx context.Context, location string, content any) (*Wrapper, error) {
	if location != "" && content == nil {
		if cached, ok := e.wrapperCache.Load(location); ok {
			return cached.(*Wrapper), nil
		}
	}

	w, err := loadWrapper(ctx, location, content)
	if err != nil {
		return nil, err
	}

	if location != "" {
		e.wrapperCache.Store(location, w)
	}
	return w, nil
}

// Formats returns the source formats supported by the usage invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: WrapperRange, Description: "CLI tools via openbindings.usage binding documents"}}
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

// Formats returns the source formats supported by the usage synthesizer:
// bare jdx usage-spec artifacts (derivation input) and wrapper documents.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{
		{Token: ArtifactRange, Description: "CLI tools via usage-spec KDL (derivation input; synthesis emits an openbindings.usage source)"},
		{Token: WrapperRange, Description: "CLI tools via openbindings.usage binding documents"},
	}
}

// SynthesizeInterface converts a usage source to an OpenBindings interface.
// A bare kdl artifact is wrapped (trivial units, §11 naming) so the emitted
// OBI's source is always an openbindings.usage document; a wrapper source
// synthesizes from its units directly.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	w, wrapperContent, location, err := wrapperForSource(ctx, src.Format, src.Location, src.Content)
	if err != nil {
		return nil, err
	}

	iface, err := buildInterfaceFromWrapper(w, wrapperContent, location)
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
