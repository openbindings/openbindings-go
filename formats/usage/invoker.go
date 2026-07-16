package usage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const DefaultSourceName = "usage"

// BindingSpec is the published binding-specification identifier this
// invoker claims — exact and opaque (openbindings.usage@1 governs the
// supported jdx usage artifact line; the artifact IS the source:
// specification + configuration = complete invocation; the artifact's
// gaps are consumer hooks).
const BindingSpec = "openbindings.usage@1"

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

	// AuthorizeExec is the USAGE-P-02 gate: dereferencing an exec address
	// executes a document-supplied command, and a processor MUST NOT do so
	// without explicit prior authorization from its operator or
	// configuration for that command. nil means DENY ALL (the normative
	// default); a consumer wires its authorization policy here.
	AuthorizeExec func(argv []string) bool
}

// NewInvoker creates a new usage binding invoker. Exec addresses are
// denied by default (USAGE-P-02); set AuthorizeExec to authorize.
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

	text, err := artifactText(ctx, location, content, e.AuthorizeExec)
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

// artifactText resolves the artifact bytes per USAGE-D-02: inline content
// verbatim; an exec address runs its argv (gated by USAGE-P-02); a
// document address (absolute URI: file, http, https) is dereferenced. A
// bare filesystem path is a relative reference in form and is refused —
// point it at file:// or embed the artifact as content.
func artifactText(ctx context.Context, location string, content any, authorizeExec func([]string) bool) (string, error) {
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
	if err := validateLocation(location); err != nil {
		return "", err
	}
	if strings.HasPrefix(location, "exec:") {
		argv, err := ParseExecAddress(location)
		if err != nil {
			return "", err
		}
		// USAGE-P-02: default deny. Executing a document-supplied command
		// requires explicit prior authorization for that command.
		if authorizeExec == nil || !authorizeExec(argv) {
			return "", fmt.Errorf("exec address %q is not authorized (USAGE-P-02: dereferencing an exec address executes a document-supplied command; the default is refusal — authorize this command in your configuration)", location)
		}
		return resolveCommandArtifact(ctx, argv)
	}
	u, _ := url.Parse(location) // validated above: absolute, non-opaque
	switch u.Scheme {
	case "file":
		data, rerr := os.ReadFile(u.Path)
		if rerr != nil {
			return "", fmt.Errorf("read usage artifact: %w", rerr)
		}
		return string(data), nil
	case "http", "https":
		return fetchArtifact(ctx, location)
	default:
		return "", fmt.Errorf("usage location scheme %q is not dereferenced by this processor (supported: file, http, https, exec addresses)", u.Scheme)
	}
}

// validateLocation checks USAGE-D-02's two address forms offline, without
// dereferencing: a document address (an absolute URI) or an exec address
// (`exec:` + a single-space-separated argv vector, no quoting mechanism).
// A bare filesystem path is a relative reference in form (core OBI-D-05)
// and is not a conformant location.
func validateLocation(location string) error {
	if strings.HasPrefix(location, "exec:") {
		_, err := ParseExecAddress(location)
		return err
	}
	if u, err := url.Parse(location); err == nil && u.Scheme != "" && u.Opaque == "" {
		return nil
	}
	return fmt.Errorf("usage location %q is not a document address or exec address (USAGE-D-02): a local artifact is addressed as file:// or embedded as the source's content", location)
}

// fetchArtifact dereferences an http(s) document address, capped at the
// same output bound the exec lane uses.
func fetchArtifact(ctx context.Context, location string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch usage artifact: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch usage artifact: HTTP %d for %q", resp.StatusCode, location)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCLIOutputBytes+1))
	if err != nil {
		return "", fmt.Errorf("read usage artifact: %w", err)
	}
	if len(data) > maxCLIOutputBytes {
		return "", fmt.Errorf("usage artifact at %q exceeds %d bytes", location, maxCLIOutputBytes)
	}
	return string(data), nil
}

// ParseExecAddress parses an exec address per USAGE-D-02's grammar: the
// exec: prefix followed by one command token and zero or more argument
// tokens separated by SINGLE spaces. Tokens carry no quoting mechanism —
// a command whose arguments contain spaces is outside this form (embed
// its generated descriptor as content instead).
func ParseExecAddress(location string) ([]string, error) {
	cmdStr := strings.TrimPrefix(location, "exec:")
	if cmdStr == "" {
		return nil, fmt.Errorf("empty command in exec address")
	}
	argv := strings.Split(cmdStr, " ")
	for _, tok := range argv {
		if tok == "" {
			return nil, fmt.Errorf("exec address %q is malformed (USAGE-D-02): tokens are separated by single spaces and carry no quoting", location)
		}
	}
	return argv, nil
}

// Formats returns the source formats supported by the usage invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return usageBindingSpecInfos()
}

func usageBindingSpecInfos() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "CLI tools described by jdx usage specs"}}
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
type Synthesizer struct {
	// AuthorizeExec is the USAGE-P-02 gate for synthesis-time dereference
	// of exec addresses. nil means DENY ALL (the normative default).
	AuthorizeExec func(argv []string) bool
}

// NewSynthesizer creates a new usage interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{}
}

// Formats returns the source formats supported by the usage synthesizer:
// bare jdx usage-spec artifacts.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return usageBindingSpecInfos()
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
	text, err := artifactText(ctx, location, src.Content, c.AuthorizeExec)
	if err != nil {
		return nil, err
	}
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("parse usage content: %w", err)
	}

	// Emission follows OBI-D-05 (the embed-default ruling for local
	// artifacts): a file path — even absolutized — is a relative reference
	// and never rides the emitted location, so the artifact embeds as
	// pristine content and the machine-coupled path stays out of the
	// document. Only an exec: locator that is a well-formed URI (no
	// arguments/spaces) is emitted verbatim. Either way the synthesized
	// document validates and is invocable as written: content is
	// authoritative at the invoke lane, and an emitted exec: locator is
	// resolved live.
	sourceEntry := openbindings.Source{BindingSpec: src.BindingSpec}
	if emittableAsLocation(location) {
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
