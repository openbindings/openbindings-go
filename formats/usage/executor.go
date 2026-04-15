package usage

import (
	"context"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const FormatToken = "usage@^2.0.0"

const DefaultSourceName = "usage"

// Executor handles binding execution for usage-spec KDL sources.
type Executor struct {
	specCache sync.Map // map[string]*Spec
}

// NewExecutor creates a new usage binding executor.
func NewExecutor() *Executor {
	return &Executor{}
}

// cachedLoadSpec loads a usage spec, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Executor) cachedLoadSpec(ctx context.Context, location string, content any) (*Spec, error) {
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

// Formats returns the source formats supported by the usage executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "CLI tools via usage-spec KDL"}}
}

// ExecuteBinding executes a CLI command based on a usage-spec binding,
// returning a single-event channel with the command's output.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	enriched := in
	if in.Store != nil {
		key := resolveUsageKey(ctx, in.Source.Location, in.Source.Content, e.cachedLoadSpec)
		if key != "" {
			if stored, err := in.Store.Get(ctx, key); err == nil && len(stored) > 0 {
				cp := *in
				if len(in.Context) == 0 {
					cp.Context = stored
				} else {
					merged := make(map[string]any, len(stored)+len(in.Context))
					for k, v := range stored {
						merged[k] = v
					}
					for k, v := range in.Context {
						merged[k] = v
					}
					cp.Context = merged
				}
				enriched = &cp
			}
		}
	}

	return openbindings.SingleEventChannel(executeBindingCached(ctx, enriched, e.cachedLoadSpec)), nil
}

func resolveUsageKey(ctx context.Context, location string, content any, loader func(context.Context, string, any) (*Spec, error)) string {
	spec, err := loader(ctx, location, content)
	if err != nil {
		return ""
	}
	meta := spec.Meta()
	binName := meta.Bin
	if binName == "" {
		binName = meta.Name
	}
	if binName == "" {
		if strings.HasPrefix(location, "exec:") {
			binName = strings.TrimPrefix(location, "exec:")
			if idx := strings.IndexByte(binName, ' '); idx > 0 {
				binName = binName[:idx]
			}
		}
	}
	if binName == "" {
		return ""
	}
	return "exec:" + binName
}

// Creator handles interface creation from usage specs.
type Creator struct{}

// NewCreator creates a new usage interface creator.
func NewCreator() *Creator {
	return &Creator{}
}

// Formats returns the source formats supported by the usage creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "CLI tools via usage-spec KDL"}}
}

// CreateInterface converts a usage spec to an OpenBindings interface.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
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
