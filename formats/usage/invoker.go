package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

const DefaultSourceName = "usage"

// BindingSpec is the proposed exact identifier this unreleased candidate
// claims—exact and opaque. openbindings.usage@1 governs the
// supported jdx usage artifact line; the artifact IS the source:
// specification + configuration = complete invocation; the artifact's
// gaps are consumer hooks.
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

	// Execute is an optional process-runtime seam. Nil uses os/exec. The
	// request preserves the observable process boundary while allowing
	// embedders and conformance runners to provide a sandboxed executor.
	Execute ProcessExecutor

	// Encoders supplies named per-field scalar token encoders selected by the
	// configuration.encode point. Each occurrence must produce exactly one
	// argv token.
	Encoders map[string]TokenEncoder
}

// ProcessRequest is one completely assembled process dispatch.
type ProcessRequest struct {
	Argv        []string
	Environment map[string]string
	Stdin       []byte
	MaxStdout   int64
}

// ProcessResult is one completed process exchange.
type ProcessResult struct {
	// Stdout and Stderr preserve the process byte sequence in Go strings.
	// The builtin output lane validates Stdout as UTF-8; configured decoders
	// receive the original bytes through RawResult.
	Stdout   string
	Stderr   string
	ExitCode int
	// Signal is the native signal name when termination was signal-driven.
	Signal          string
	StdoutTruncated bool
	StderrTruncated bool
}

// ProcessExecutor executes one assembled process request.
type ProcessExecutor func(context.Context, ProcessRequest) (ProcessResult, error)

// TokenEncoder converts one scalar value occurrence to exactly one argv token.
type TokenEncoder func(any) (string, error)

// NewInvoker creates a new usage binding invoker. Exec addresses are
// denied by default (USAGE-P-02); set AuthorizeExec to authorize.
func NewInvoker() *Invoker {
	return &Invoker{}
}

var _ invoke.BindingInvoker = (*Invoker)(nil)
var _ invoke.BuiltinHooksProvider = (*Invoker)(nil)

// cachedLoadSpec loads and parses a bare usage artifact — inline content,
// an ABSOLUTE file location, or an exec: locator (the emitted spec of a
// binary) — caching by location within a process. The artifact's declared
// min_usage_version is checked against this implementation's supported
// range (dropping that check would be silent).
func (e *Invoker) cachedLoadSpec(ctx context.Context, location string, content json.RawMessage) (*Spec, error) {
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
	if err := validateAcceptedUsageArtifact(spec); err != nil {
		return nil, err
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
//
// Content arrives as raw JSON (presence-aware: nil = absent, `null` =
// present null); this family pins a PRESENT content to a JSON string
// carrying the usage descriptor text (USAGE-D-01) and refuses anything
// else — a present null included.
func artifactText(ctx context.Context, location string, content json.RawMessage, authorizeExec func([]string) bool) (string, error) {
	if content != nil {
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return "", fmt.Errorf("usage source content must be the artifact text (a JSON string), got %s", openbindings.ContentKind(content))
		}
		return validArtifactText(text, "embedded usage artifact")
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
		text, err := resolveCommandArtifact(ctx, argv)
		if err != nil {
			return "", err
		}
		return validArtifactText(text, "generated usage artifact")
	}
	u, _ := url.Parse(location) // validated above: absolute, non-opaque
	switch u.Scheme {
	case "file":
		data, rerr := os.ReadFile(u.Path)
		if rerr != nil {
			return "", fmt.Errorf("read usage artifact: %w", rerr)
		}
		return validArtifactText(string(data), "usage artifact file")
	case "http", "https":
		text, err := fetchArtifact(ctx, location)
		if err != nil {
			return "", err
		}
		return validArtifactText(text, "fetched usage artifact")
	default:
		return "", fmt.Errorf("usage location scheme %q is not dereferenced by this processor (supported: file, http, https, exec addresses)", u.Scheme)
	}
}

func validArtifactText(text, source string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%s is not valid UTF-8 (USAGE-D-01)", source)
	}
	return text, nil
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

// validateInvocationArtifact refuses upstream lanes whose semantics require
// private filesystem, shell, discovery, or configuration policy. The
// descriptor remains faithfully parseable for inspection, but this binding
// does not silently ignore these nodes on invocation.
func validateInvocationArtifact(spec *Spec) error {
	var walk func([]Node) error
	walk = func(nodes []Node) error {
		for _, node := range nodes {
			switch node.Name {
			case "include":
				return fmt.Errorf("usage include nodes are excluded from openbindings.usage@1 invocation")
			case "mount":
				return fmt.Errorf("usage dynamic mounts are excluded from openbindings.usage@1 invocation")
			case "config", "config_file", "config_alias":
				return fmt.Errorf("usage config-file discovery is excluded from openbindings.usage@1 invocation")
			case "arg":
				if value, ok := node.Props["parse"]; ok && value.String() != "" {
					return fmt.Errorf("usage argument parse commands are excluded from openbindings.usage@1 invocation")
				}
			}
			if err := walk(node.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(spec.Nodes)
}

func validateAcceptedUsageArtifact(spec *Spec) error {
	if min := spec.Meta().MinUsageVersion; min != "" {
		ok, err := IsSupportedVersion(min)
		if err != nil {
			return fmt.Errorf("artifact min_usage_version %q: %w", min, err)
		}
		if !ok {
			return fmt.Errorf("artifact declares min_usage_version %q, outside the accepted range %s-%s", min, MinSupportedVersion, MaxTestedVersion)
		}
	}
	return validateInvocationArtifact(spec)
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

// BindingSpecs returns the binding-spec identifiers supported by the usage invoker.
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
func (e *Invoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

// Synthesizer handles interface synthesis from usage specs.
type Synthesizer struct {
	// AuthorizeExec is the USAGE-P-02 gate for synthesis-time dereference
	// of exec addresses. nil means DENY ALL (the normative default).
	AuthorizeExec func(argv []string) bool
}

var _ synthesize.InterfaceSynthesizer = (*Synthesizer)(nil)
var _ synthesize.CoverageSynthesizer = (*Synthesizer)(nil)
var _ synthesize.SourceInspector = (*Synthesizer)(nil)

// NewSynthesizer creates a new usage interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{}
}

// BindingSpecs returns the binding-spec identifiers supported by the usage synthesizer:
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
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return observation.iface, nil
}

func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return synthesize.NewSynthesisResult(
		observation.iface,
		synthesisCoverage(observation.spec, observation.iface),
		true,
	)
}

type synthesisObservation struct {
	iface *openbindings.Interface
	spec  *Spec
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesisObservation, error) {
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		if err != nil {
			return nil, err
		}
		return &synthesisObservation{iface: &skeleton}, nil
	}
	if len(in.Sources) > 1 {
		return nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec {
		return nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateLocation(src.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}

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
	// document. A valid exec: address is binding-spec-defined absolute
	// address carriage (USAGE-D-02), including a space-separated argv vector,
	// and is emitted verbatim. Either way the synthesized document validates
	// and is invocable as written: content is authoritative at the invoke
	// lane, and an emitted exec: address is resolved live.
	sourceEntry := openbindings.Source{BindingSpec: src.BindingSpec}
	if src.Content != nil {
		// Content primacy is part of the emitted document too: retaining the
		// supplied artifact prevents a co-present provenance location from
		// silently becoming authoritative after synthesis.
		sourceEntry.Content = src.Content
		if emittableAsLocation(location) {
			sourceEntry.Location = location
		}
	} else if src.Embed {
		sourceEntry.Content = openbindings.TextContent(text)
		if emittableAsLocation(location) {
			sourceEntry.Location = location
		}
	} else if emittableAsLocation(location) {
		sourceEntry.Location = location
	} else {
		sourceEntry.Content = openbindings.TextContent(text)
	}

	iface, err := buildInterfaceFromSpec(spec, sourceEntry)
	if err != nil {
		return nil, err
	}

	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, BindingSpec); err != nil {
		return nil, err
	}

	return &synthesisObservation{iface: &iface, spec: spec}, nil
}
