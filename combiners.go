package openbindings

import (
	"context"
	"fmt"
	"strings"

	"github.com/openbindings/openbindings-go/formattoken"
)

// CombineExecutors returns a single BindingExecutor that routes to the
// appropriate inner executor based on the source format token.
// Format token matching uses the same range-matching rules as the SDK.
// First match wins; order matters.
func CombineExecutors(executors ...BindingExecutor) BindingExecutor {
	c := &combinedExecutor{}
	for _, ex := range executors {
		for _, fi := range ex.Formats() {
			vr, err := formattoken.ParseRange(fi.Token)
			if err != nil {
				continue
			}
			c.entries = append(c.entries, combinedExecEntry{
				vr:   vr,
				exec: ex,
				info: fi,
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
// combinedExecutor
// ---------------------------------------------------------------------------

type combinedExecEntry struct {
	vr   formattoken.VersionRange
	exec BindingExecutor
	info FormatInfo
}

type combinedExecutor struct {
	entries []combinedExecEntry
	byName  map[string][]int // name -> indices into entries
	formats []FormatInfo
}

func (c *combinedExecutor) add(ex BindingExecutor) {
	for _, fi := range ex.Formats() {
		vr, err := formattoken.ParseRange(fi.Token)
		if err != nil {
			continue
		}
		c.entries = append(c.entries, combinedExecEntry{
			vr:   vr,
			exec: ex,
			info: fi,
		})
		name := strings.ToLower(vr.Name)
		c.byName = appendToMap(c.byName, name, len(c.entries)-1)
		c.formats = append(c.formats, fi)
	}
}

func (c *combinedExecutor) Formats() []FormatInfo {
	cp := make([]FormatInfo, len(c.formats))
	copy(cp, c.formats)
	return cp
}

func (c *combinedExecutor) ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
	exec := c.findExecutor(in.Source.Format)
	if exec == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoExecutor, in.Source.Format)
	}
	return exec.ExecuteBinding(ctx, in)
}

func (c *combinedExecutor) findExecutor(sourceFormat string) BindingExecutor {
	name := formatName(sourceFormat)
	indices := c.byName[name]
	for _, idx := range indices {
		entry := &c.entries[idx]
		if formattoken.Matches(entry.vr, sourceFormat) {
			return entry.exec
		}
	}
	// Name-only fallback: handles cases where the source format is a range
	// token rather than an exact version.
	for _, idx := range indices {
		if c.entries[idx].exec != nil {
			return c.entries[idx].exec
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// combinedCreator
// ---------------------------------------------------------------------------

var _ RefLister = (*combinedCreator)(nil)

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

// ListBindableRefs implements RefLister by routing to the first underlying
// creator that matches the source format and implements RefLister.
func (c *combinedCreator) ListBindableRefs(ctx context.Context, source *Source) (*ListRefsResult, error) {
	if source == nil {
		return nil, ErrNoSources
	}
	cr := c.findCreator(source.Format)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoCreator, source.Format)
	}
	lister, ok := cr.(RefLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRefListingUnsupported, source.Format)
	}
	return lister.ListBindableRefs(ctx, source)
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
