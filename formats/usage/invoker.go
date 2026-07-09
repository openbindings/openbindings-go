package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const DefaultSourceName = "usage"

// FormatRanges are the bare jdx usage token ranges this invoker claims —
// one caret range per supported tool major (the token grammar has no
// compound ranges): the artifact IS the source (no wrapper —
// specification + configuration = complete invocation; the artifact's
// gaps are consumer hooks).
var FormatRanges = []string{"usage@^2.0.0", "usage@^3.0.0"}

// Invoker handles binding invocation for bare jdx usage artifacts
// (usage.kdl): refs are space-separated command paths; the wire questions
// the artifact cannot answer (routing, decode, classify) are answered by
// documented assumptions overridable through the consumer hook seam.
//
// The parsed-spec cache is scoped to the Invoker instance (keyed by
// artifact location) and lives as long as it does — scope instances per
// tenant to bound growth in multi-tenant servers.
type Invoker struct {
	specCache sync.Map // map[string]*Spec
}

// NewInvoker creates a new usage binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{}
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)
var _ openbindings.BuiltinHooksProvider = (*Invoker)(nil)

// cachedLoadSpec loads and parses a bare usage artifact — inline content,
// an ABSOLUTE file location, or an exec: locator (the emitted spec of a
// binary) — caching by location within a process. The artifact's declared
// min_usage_version is checked against this implementation's supported
// range (dropping that check would be silent).
func (e *Invoker) cachedLoadSpec(ctx context.Context, location string, content any) (*Spec, error) {
	if location != "" && content == nil {
		if cached, ok := e.specCache.Load(location); ok {
			return cached.(*Spec), nil
		}
	}

	text, err := artifactText(ctx, location, content)
	if err != nil {
		return nil, err
	}
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("parse usage artifact: %w", err)
	}
	if min := spec.Meta().MinUsageVersion; min != "" {
		ok, verr := IsSupportedVersion(min)
		if verr != nil {
			return nil, fmt.Errorf("artifact min_usage_version %q: %w", min, verr)
		}
		if !ok {
			return nil, fmt.Errorf("artifact declares min_usage_version %q, outside the accepted range %s-%s", min, MinSupportedVersion, MaxTestedVersion)
		}
	}

	if location != "" {
		e.specCache.Store(location, spec)
	}
	return spec, nil
}

// artifactText resolves the artifact bytes: inline content verbatim;
// exec: locators run the binary (its emitted spec); file locations MUST
// be absolute (never resolved against a carriage base).
func artifactText(ctx context.Context, location string, content any) (string, error) {
	if content != nil {
		switch c := content.(type) {
		case string:
			return c, nil
		case []byte:
			return string(c), nil
		default:
			return "", fmt.Errorf("usage source content must be the artifact text, got %T", content)
		}
	}
	if location == "" {
		return "", fmt.Errorf("source must have location or content")
	}
	if strings.HasPrefix(location, "exec:") {
		return resolveCommandArtifact(ctx, location)
	}
	if strings.Contains(location, "://") {
		return "", fmt.Errorf("usage artifacts are local files or exec: locators; %q is not fetchable by this format", location)
	}
	if !filepath.IsAbs(location) {
		return "", fmt.Errorf("usage location %q must be absolute (never resolved against a carriage base)", location)
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return "", fmt.Errorf("read usage artifact: %w", err)
	}
	return string(data), nil
}

// Formats returns the source formats supported by the usage invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return usageFormatInfos()
}

func usageFormatInfos() []openbindings.FormatInfo {
	infos := make([]openbindings.FormatInfo, len(FormatRanges))
	for i, token := range FormatRanges {
		infos[i] = openbindings.FormatInfo{Token: token, Description: "CLI tools described by jdx usage specs"}
	}
	return infos
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

// Synthesizer handles interface synthesis from usage specs.
type Synthesizer struct{}

// NewSynthesizer creates a new usage interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{}
}

// Formats returns the source formats supported by the usage synthesizer:
// bare jdx usage-spec artifacts.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return usageFormatInfos()
}

// SynthesizeInterface converts a bare jdx usage source to an OpenBindings
// interface: one operation per bindable command, command-path refs, input
// schemas from flags/args, and FLOOR-TRUE output schemas ({"type":
// "string"} with an in-schema x-ob floor-stamp — the text assumption
// always yields a string, so the derived contract never lies; the stamp
// keys the diagnostics and self-clears when a real schema is elected).
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
	}
	src := in.Sources[0]

	location, err := absolutizeArtifactLocation(src.Location, src.Content)
	if err != nil {
		return nil, err
	}
	text, err := artifactText(ctx, location, src.Content)
	if err != nil {
		return nil, err
	}
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("parse usage content: %w", err)
	}

	// The emitted source carries the absolutized location, so the
	// synthesized document is invocable as written (the invoke lane never
	// resolves against a carriage base).
	sourceEntry := openbindings.Source{Format: src.Format}
	if location != "" {
		sourceEntry.Location = location
	} else {
		sourceEntry.Content = text
	}

	iface, err := buildInterfaceFromSpec(spec, sourceEntry)
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
