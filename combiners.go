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

// CombineCreators returns a single InterfaceCreator that routes to the
// appropriate inner creator based on the source format token.
func CombineCreators(creators ...InterfaceCreator) InterfaceCreator {
	c := &combinedCreator{}
	for _, cr := range creators {
		for _, fi := range cr.Formats() {
			vr, err := formattoken.ParseRange(fi.Token)
			if err != nil {
				continue
			}
			c.entries = append(c.entries, combinedCreatorEntry{
				vr:      vr,
				creator: cr,
				info:    fi,
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

func (c *combinedInvoker) InvokeBinding(ctx context.Context, in *BindingInvocationInput) (<-chan StreamEvent, error) {
	invoker := c.findInvoker(in.Source.Format)
	if invoker == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoInvoker, in.Source.Format)
	}
	return invoker.InvokeBinding(ctx, in)
}

func (c *combinedInvoker) findInvoker(sourceFormat string) BindingInvoker {
	name := formatName(sourceFormat)
	indices := c.byName[name]
	for _, idx := range indices {
		entry := &c.entries[idx]
		if formattoken.Matches(entry.vr, sourceFormat) {
			return entry.invoker
		}
	}
	// Name-only fallback: handles cases where the source format is a range
	// token rather than an exact version.
	for _, idx := range indices {
		if c.entries[idx].invoker != nil {
			return c.entries[idx].invoker
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// combinedCreator
// ---------------------------------------------------------------------------

var _ SourceInspector = (*combinedCreator)(nil)

type combinedCreatorEntry struct {
	vr      formattoken.VersionRange
	creator InterfaceCreator
	info    FormatInfo
}

type combinedCreator struct {
	entries []combinedCreatorEntry
	byName  map[string][]int // name -> indices into entries
	formats []FormatInfo
}

func (c *combinedCreator) Formats() []FormatInfo {
	cp := make([]FormatInfo, len(c.formats))
	copy(cp, c.formats)
	return cp
}

func (c *combinedCreator) CreateInterface(ctx context.Context, in *CreateInput) (*Interface, error) {
	if len(in.Sources) == 0 {
		return nil, ErrNoSources
	}
	cr := c.findCreator(in.Sources[0].Format)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoCreator, in.Sources[0].Format)
	}
	return cr.CreateInterface(ctx, in)
}

// InspectSource implements SourceInspector by routing to the first underlying
// creator that matches the source format and implements SourceInspector.
func (c *combinedCreator) InspectSource(ctx context.Context, source *Source) (*SourceInspection, error) {
	if source == nil {
		return nil, ErrNoSources
	}
	cr := c.findCreator(source.Format)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoCreator, source.Format)
	}
	inspector, ok := cr.(SourceInspector)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSourceInspectionUnsupported, source.Format)
	}
	return inspector.InspectSource(ctx, source)
}

func (c *combinedCreator) findCreator(sourceFormat string) InterfaceCreator {
	name := formatName(sourceFormat)
	indices := c.byName[name]
	for _, idx := range indices {
		entry := &c.entries[idx]
		if formattoken.Matches(entry.vr, sourceFormat) {
			return entry.creator
		}
	}
	// Name-only fallback: handles synthesis where the source format is the
	// creator's own range token rather than an exact version from an OBI.
	for _, idx := range indices {
		if c.entries[idx].creator != nil {
			return c.entries[idx].creator
		}
	}
	return nil
}
