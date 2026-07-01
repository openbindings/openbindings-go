package openbindings

import (
	"context"
	"fmt"
	"strings"

	"github.com/openbindings/openbindings-go/formattoken"
)

// CombineInvokers returns a single BindingInvoker that routes to the
// appropriate inner invoker based on the source format token.
// Format token matching uses the same range-matching rules as the SDK.
// First match wins; order matters.
func CombineInvokers(invokers ...BindingInvoker) BindingInvoker {
	c := &combinedInvoker{}
	for _, iv := range invokers {
		for _, fi := range iv.Formats() {
			vr, err := formattoken.ParseRange(fi.Token)
			if err != nil {
				continue
			}
			c.entries = append(c.entries, combinedInvokerEntry{
				vr:      vr,
				invoker: iv,
				info:    fi,
			})
			name := strings.ToLower(vr.Name)
			c.byName = appendToMap(c.byName, name, len(c.entries)-1)
			c.formats = append(c.formats, fi)
		}
	}
	return c
}

// CombineSynthesizers returns a single InterfaceSynthesizer that routes to the
// appropriate inner synthesizer based on the source format token.
func CombineSynthesizers(synthesizers ...InterfaceSynthesizer) InterfaceSynthesizer {
	c := &combinedSynthesizer{}
	for _, cr := range synthesizers {
		for _, fi := range cr.Formats() {
			vr, err := formattoken.ParseRange(fi.Token)
			if err != nil {
				continue
			}
			c.entries = append(c.entries, combinedSynthesizerEntry{
				vr:          vr,
				synthesizer: cr,
				info:        fi,
			})
			name := strings.ToLower(vr.Name)
			c.byName = appendToMap(c.byName, name, len(c.entries)-1)
			c.formats = append(c.formats, fi)
		}
	}
	return c
}

func appendToMap(m map[string][]int, key string, idx int) map[string][]int {
	if m == nil {
		m = make(map[string][]int)
	}
	m[key] = append(m[key], idx)
	return m
}

// ---------------------------------------------------------------------------
// combinedInvoker
// ---------------------------------------------------------------------------

type combinedInvokerEntry struct {
	vr      formattoken.VersionRange
	invoker BindingInvoker
	info    FormatInfo
}

type combinedInvoker struct {
	entries []combinedInvokerEntry
	byName  map[string][]int // name -> indices into entries
	formats []FormatInfo
}

func (c *combinedInvoker) add(iv BindingInvoker) {
	for _, fi := range iv.Formats() {
		vr, err := formattoken.ParseRange(fi.Token)
		if err != nil {
			continue
		}
		c.entries = append(c.entries, combinedInvokerEntry{
			vr:      vr,
			invoker: iv,
			info:    fi,
		})
		name := strings.ToLower(vr.Name)
		c.byName = appendToMap(c.byName, name, len(c.entries)-1)
		c.formats = append(c.formats, fi)
	}
}

func (c *combinedInvoker) Formats() []FormatInfo {
	cp := make([]FormatInfo, len(c.formats))
	copy(cp, c.formats)
	return cp
}

func (c *combinedInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	invoker := c.findInvoker(args.Source.Format)
	if invoker == nil {
		return NewErroredInvocation[any, any](&InvocationError{
			Code:    ErrCodeBindingNotFound,
			Message: fmt.Sprintf("%v: %s", ErrNoInvoker, args.Source.Format),
		})
	}
	return invoker.InvokeBinding(ctx, args)
}

// prepareBinding routes the side-effect-free preflight to the matching inner
// invoker. An invoker without BindingPreparer simply reports no requirement.
func (c *combinedInvoker) prepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	invoker := c.findInvoker(args.Source.Format)
	if invoker == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoInvoker, args.Source.Format)
	}
	if p, ok := invoker.(BindingPreparer); ok {
		return p.PrepareBinding(ctx, args)
	}
	return nil, nil
}

func (c *combinedInvoker) findInvoker(sourceFormat string) BindingInvoker {
	name := formatName(sourceFormat)
	indices := c.byName[name]
	for _, idx := range indices {
		entry := &c.entries[idx]
		if entry.info.Token == sourceFormat || formattoken.Matches(entry.vr, sourceFormat) {
			return entry.invoker
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// combinedSynthesizer
// ---------------------------------------------------------------------------

var _ SourceInspector = (*combinedSynthesizer)(nil)

type combinedSynthesizerEntry struct {
	vr          formattoken.VersionRange
	synthesizer InterfaceSynthesizer
	info        FormatInfo
}

type combinedSynthesizer struct {
	entries []combinedSynthesizerEntry
	byName  map[string][]int // name -> indices into entries
	formats []FormatInfo
}

func (c *combinedSynthesizer) Formats() []FormatInfo {
	cp := make([]FormatInfo, len(c.formats))
	copy(cp, c.formats)
	return cp
}

func (c *combinedSynthesizer) SynthesizeInterface(ctx context.Context, in *SynthesizeInput) (*Interface, error) {
	if len(in.Sources) == 0 {
		return nil, ErrNoSources
	}
	cr := c.findSynthesizer(in.Sources[0].Format)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSynthesizer, in.Sources[0].Format)
	}
	return cr.SynthesizeInterface(ctx, in)
}

// InspectSource implements SourceInspector by routing to the first underlying
// synthesizer that matches the source format and implements SourceInspector.
func (c *combinedSynthesizer) InspectSource(ctx context.Context, source *Source) (*SourceInspection, error) {
	if source == nil {
		return nil, ErrNoSources
	}
	cr := c.findSynthesizer(source.Format)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSynthesizer, source.Format)
	}
	inspector, ok := cr.(SourceInspector)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSourceInspectionUnsupported, source.Format)
	}
	return inspector.InspectSource(ctx, source)
}

func (c *combinedSynthesizer) findSynthesizer(sourceFormat string) InterfaceSynthesizer {
	name := formatName(sourceFormat)
	indices := c.byName[name]
	for _, idx := range indices {
		entry := &c.entries[idx]
		if entry.info.Token == sourceFormat || formattoken.Matches(entry.vr, sourceFormat) {
			return entry.synthesizer
		}
	}
	return nil
}
