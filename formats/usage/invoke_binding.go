package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// run drives the invocation handle: it reads the single flag/arg object the
// caller writes (or runs the bare command for no-input operations), resolves
// the binary and argv, executes the CLI, and emits the parsed output.
//
// Pre-dispatch failures (unloadable spec, unknown ref, malformed input)
// terminate the handle before the process is spawned. A non-zero exit is a
// terminal ERR_EXECUTION_FAILED carrying the exit code and captured output in
// Details; a missing binary is ERR_SOURCE_CONFIG_ERROR.
func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// Bound the process to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation kills it.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	// No-input convention: an operation-layer call for an operation that
	// declares no input (InputSchema == nil) closes input on entry and runs
	// the bare command. Otherwise read the single flag/arg object; a bare
	// close (io.EOF) also runs bare (CLI flags are optional).
	var input any
	if args.Binding != nil && args.InputSchema == nil {
		_ = inv.CloseInput()
	} else {
		v, rerr := inv.ReadInput(bctx)
		switch {
		case rerr == io.EOF:
			input = nil
		case rerr != nil:
			inv.FireError(openbindings.AsInvocationError(rerr))
			return
		default:
			input = v
		}
		_ = inv.CloseInput()
	}

	// Direct-binary dispatch (context-metadata hint): SDK-only feature, no
	// spec loaded, ref is a command string. Hooks are NOT consulted on
	// this lane in v1 (stated, not implied).
	if binary := metadataBinary(args.Context); binary != "" {
		e.runDirect(bctx, args, inv, binary, input)
		return
	}

	// Load the bare jdx usage artifact (content, absolute location, or an
	// exec: locator) — the RESTORED pre-wrapper loader, min_usage_version
	// check kept.
	spec, lerr := e.cachedLoadSpec(bctx, args.Source.Location, args.Source.Content)
	if lerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: lerr.Error()})
		return
	}

	// The ref is the format's own grammar: a space-separated command path
	// into the artifact ("context set"; absent = the root command). A
	// whitespace-bearing ref is NOT the root spelling: it flows into
	// findCommand, whose USAGE-D-03 grammar refuses empty segments.
	var cmd *Command
	var inherited []Flag
	var cmdPath []string
	if args.Ref == "" {
		rc := rootCommand(spec)
		if rc == nil {
			rc = &Command{Flags: spec.Flags(), Args: spec.Args()}
		}
		cmd = rc
	} else {
		found, ferr := findCommand(spec, args.Ref)
		if ferr != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: ferr.Error()})
			return
		}
		cmd = found.cmd
		inherited = found.inheritedFlags
		cmdPath = found.path
	}

	meta := spec.Meta()
	binName := meta.Bin
	if binName == "" {
		binName = meta.Name
	}
	if binName == "" {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "usage artifact does not define a binary name (bin or name)"})
		return
	}

	// Complete the site: Target is GUARANTEED on this lane (the kdl's
	// bin/name — consumers' site guards depend on it).
	site := siteFor(args, binName)

	// Route every input field through the seam (specification +
	// configuration: routing is a wire question the usage artifact cannot
	// answer; the assumption is argv, the consumer's FieldRouter overrides
	// per field). Enforcement replaces the wrapper's load-time validation.
	routed, rierr := routeFields(site, args.Hooks, cmd, inherited, input)
	if rierr != nil {
		inv.FireError(rierr)
		return
	}
	defer routed.cleanup()

	var argvInput any
	if input != nil {
		argvInput = routed.fields
	}
	cmdArgs, aerr := buildCLIArgs(cmdPath, cmd, inherited, argvInput)
	if aerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: aerr.Error()})
		return
	}

	res, runErr := runCLI(bctx, binName, cmdArgs, args.Context, routed.stdin)
	// Materialized files only need to outlive the process; the deferred call
	// (idempotent) covers the error paths above.
	routed.cleanup()
	if bctx.Err() != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeCancelled, Message: "operation cancelled"})
		return
	}
	if runErr != nil {
		// Consultation preconditions: spawn failure, cap overflow, and
		// cancellation never consult classify/decode (no completed
		// transport exchange). A missing binary is a configuration error.
		code := openbindings.ErrCodeExecutionFailed
		if errors.Is(runErr, exec.ErrNotFound) {
			code = openbindings.ErrCodeSourceConfigError
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: runErr.Error()})
		return
	}

	// One delivery unit: the completed process. Meta carries the FULL
	// stderr capture (tail-capping applies only to the trailer) and the
	// per-field routing record (x-ob-route provenance).
	exitCode := res.exitCode
	raw := openbindings.RawResult{
		Status: &exitCode,
		Body:   []byte(res.stdout),
		Meta:   resultMeta(res, routed.record),
	}

	// Classify through the seam: the assumption is exit 0; consumers'
	// classifiers widen it (diff-class) or read the code as the output
	// (grep-class, via a decode hook).
	ok, cerr := args.Hooks.Classify(site, raw, builtinClassify)
	if cerr != nil {
		inv.FireError(openbindings.AsInvocationError(cerr))
		return
	}
	if !ok {
		// The format's NATIVE failure: hooks change the verdict, never the
		// error vocabulary. Provenance: the deciding layer is named on the
		// error's decidedBy stamp.
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", res.exitCode),
			Details: map[string]any{"exitCode": res.exitCode, "output": wrapText(res.stdout, res.stderr)},
		})
		return
	}

	output, derr := args.Hooks.DecodeOutput(site, raw, builtinDecodeText)
	if derr != nil {
		inv.FireError(openbindings.AsInvocationError(derr))
		return
	}

	emitWithDiagnostics(inv, output, res, args.Hooks, routed.record)
}

// siteFor completes the core-stamped site with the format-known Target
// (the artifact's binary name). A nil args.Site (direct format-package
// call) gets a minimal site whose Builtin* dispatch stays loud.
func siteFor(args *openbindings.BindingInvocationArgs, binName string) openbindings.InvokeSite {
	var site openbindings.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.BindingSpec = args.Source.BindingSpec
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = binName
	}
	return site
}

// builtinClassify is the exec builtin: success iff exit 0 (the
// documented assumption; the artifact cannot declare exit meanings).
func builtinClassify(_ openbindings.InvokeSite, raw openbindings.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status == 0, nil
}

// builtinDecodeText is the exec builtin: the output value is stdout with
// trailing newlines stripped, exactly as command substitution $(...)
// reads a command's text output — content-independent, lossless,
// recoverable. Machine lanes are a consumer decode hook (or a future
// artifact-native declaration).
func builtinDecodeText(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
	return strings.TrimRight(string(raw.Body), "\r\n"), nil
}

// resultMeta assembles the per-unit Meta: the exit code, the FULL stderr
// capture, and the per-field routing record.
func resultMeta(res *cliResult, record map[string]string) openbindings.Metadata {
	meta := openbindings.Metadata{
		"x-exit-code": {strconv.Itoa(res.exitCode)},
	}
	if res.stderr != "" {
		meta["x-stderr"] = []string{res.stderr}
	}
	for field, route := range record {
		meta["x-ob-route"] = append(meta["x-ob-route"], field+"="+route)
	}
	return meta
}

// BuiltinHooks exposes the exec builtins to the seam's cross-format
// dispatch: text decode and exit-0 classification (both documented
// assumptions — usage is a surface grammar with no invocation-contract
// vocabulary; the assumptions shrink if jdx grows one).
func (e *Invoker) BuiltinHooks() (openbindings.OutputDecoder, openbindings.ResultClassifier) {
	return builtinDecodeText, builtinClassify
}

// emitWithDiagnostics stamps the diagnostics trailer and emits the output.
// The trailer carries the success provenance stamps (the conventions
// record, spec/binding-specs/README.md) — x-ob-decode
// ("assumption/text" | "hook"), x-ob-classify ("assumption/exit-0" |
// "hook"), and the provenance-qualified per-field x-ob-route — plus the
// exec carrier facts (x-exit-code, tail-capped x-stderr). Tier-blind on
// purpose: failure paths are tier-precise; success provenance is not.
// Diagnostics ride trailing metadata, never the output value: the exit code
// and captured stderr (a filter command's human summary). The trailer is
// header-shaped, so stderr is bounded: the LAST maxStderrTrailerBytes (tails
// carry the operative lines), with an explicit truncation marker that also
// fires when the capture itself overflowed.
func emitWithDiagnostics(inv *openbindings.InvocationImpl[any, any], output any, res *cliResult, hooks *openbindings.InvokeHooks, record map[string]string) {
	trailer := openbindings.Metadata{"x-exit-code": {strconv.Itoa(res.exitCode)}}
	decodeStamp, classifyStamp := "assumption/text", "assumption/exit-0"
	if hooks.DecodeDecidedBy() == "hook" {
		decodeStamp = "hook"
	}
	if hooks.ClassifyDecidedBy() == "hook" {
		classifyStamp = "hook"
	}
	trailer["x-ob-decode"] = []string{decodeStamp}
	trailer["x-ob-classify"] = []string{classifyStamp}
	for field, route := range record {
		trailer["x-ob-route"] = append(trailer["x-ob-route"], field+"="+route)
	}
	if res.stderr != "" {
		stderrOut := res.stderr
		truncated := res.stderrTruncated
		if len(stderrOut) > maxStderrTrailerBytes {
			stderrOut = stderrOut[len(stderrOut)-maxStderrTrailerBytes:]
			truncated = true
		}
		trailer["x-stderr"] = []string{stderrOut}
		if truncated {
			trailer["x-stderr-truncated"] = []string{"true"}
		}
	}
	inv.SetTrailer(trailer)
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// runDirect is the direct-binary dispatch path (SDK-only feature, outside
// the format conventions): the ref is split as a command path (USAGE-D-03), every input
// field rides argv as a flag, output decodes under the default text mode.
func (e *Invoker) runDirect(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any], binary string, input any) {
	cmdArgs, err := buildDirectArgsFromRef(args.Ref, input)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()})
		return
	}
	res, runErr := runCLI(ctx, binary, cmdArgs, args.Context, nil)
	if ctx.Err() != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeCancelled, Message: "operation cancelled"})
		return
	}
	if runErr != nil {
		code := openbindings.ErrCodeExecutionFailed
		if errors.Is(runErr, exec.ErrNotFound) {
			code = openbindings.ErrCodeSourceConfigError
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: runErr.Error()})
		return
	}
	if res.exitCode != 0 {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", res.exitCode),
			Details: map[string]any{"exitCode": res.exitCode, "output": wrapText(res.stdout, res.stderr)},
		})
		return
	}
	output := strings.TrimRight(res.stdout, "\r\n") // the text assumption
	// Direct-binary dispatch consults no hooks (stated in run); a nil
	// carrier stamps the assumptions, which is what actually decided.
	emitWithDiagnostics(inv, output, res, nil, nil)
}

// metadataBinary extracts the "binary" hint from context metadata.
func metadataBinary(ctx map[string]any) string {
	meta := openbindings.ContextMetadata(ctx)
	if meta == nil {
		return ""
	}
	if b, ok := meta["binary"].(string); ok {
		return b
	}
	return ""
}

func buildDirectArgsFromRef(ref string, input any) ([]string, error) {
	// USAGE-D-03: a ref is a space-separated command path — single spaces,
	// no quoting mechanism.
	args := strings.Split(ref, " ")
	for _, tok := range args {
		if tok == "" {
			return nil, fmt.Errorf("ref %q is malformed (USAGE-D-03): command-path segments are separated by single spaces", ref)
		}
	}

	if input == nil {
		return args, nil
	}

	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		return args, nil
	}

	names := make([]string, 0, len(inputMap))
	for name := range inputMap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		flagArgs, err := formatFlagWithDef(name, inputMap[name], Flag{})
		if err != nil {
			return nil, fmt.Errorf("format flag %q: %w", name, err)
		}
		args = append(args, flagArgs...)
	}

	return args, nil
}

type findCommandResult struct {
	path           []string
	cmd            *Command
	inheritedFlags []Flag
}

func findCommand(spec *Spec, ref string) (*findCommandResult, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty-string ref is not conformant (USAGE-D-03): the root command is addressed by omitting ref")
	}

	// USAGE-D-03: a ref is a space-separated command path — segments
	// separated by SINGLE spaces, no quoting mechanism, whitespace never
	// collapsed (parity with the direct-binary lane's grammar).
	targetPath := strings.Split(ref, " ")
	for _, seg := range targetPath {
		if seg == "" {
			return nil, fmt.Errorf("ref %q is malformed (USAGE-D-03): command-path segments are separated by single spaces", ref)
		}
	}
	commands := spec.Commands()
	var path []string

	// Seed with top-level global flags so they're inherited by all commands.
	var inheritedGlobals []Flag
	for _, f := range spec.Flags() {
		if f.Global {
			inheritedGlobals = append(inheritedGlobals, f)
		}
	}

	for i, target := range targetPath {
		matched := false
		for _, cmd := range commands {
			if !commandMatchesName(cmd, target) {
				continue
			}
			path = append(path, cmd.Name)
			if i == len(targetPath)-1 {
				cmdCopy := cmd
				return &findCommandResult{
					path:           path,
					cmd:            &cmdCopy,
					inheritedFlags: inheritedGlobals,
				}, nil
			}
			for _, f := range cmd.Flags {
				if f.Global {
					inheritedGlobals = append(inheritedGlobals, f)
				}
			}
			commands = cmd.Commands
			matched = true
			break
		}
		if !matched {
			return nil, fmt.Errorf("command %q not found in usage spec", ref)
		}
	}

	return nil, fmt.Errorf("command %q not found in usage spec", ref)
}

// commandMatchesName checks if a command matches a name by its canonical name or any alias.
func commandMatchesName(cmd Command, target string) bool {
	if cmd.Name == target {
		return true
	}
	for _, alias := range cmd.Aliases {
		for _, name := range alias.Names {
			if name == target {
				return true
			}
		}
	}
	return false
}

func buildCLIArgs(cmdPath []string, cmd *Command, inheritedGlobals []Flag, input any) ([]string, error) {
	var args []string
	args = append(args, cmdPath...)

	if input == nil {
		return args, nil
	}

	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		return nil, fmt.Errorf("input must be an object with field names matching the command's flags and args")
	}

	flagDefs := make(map[string]Flag)
	for _, f := range cmd.AllFlags(inheritedGlobals) {
		name := f.PrimaryName()
		if name != "" {
			flagDefs[name] = f
		}
		parsed := f.ParseUsage()
		for _, short := range parsed.Short {
			flagDefs[short] = f
		}
		for _, long := range parsed.Long {
			flagDefs[long] = f
		}
	}

	type argDef struct {
		name      string
		cleanName string
		def       Arg
	}
	var argDefs []argDef
	for _, a := range cmd.Args {
		argDefs = append(argDefs, argDef{
			name:      a.Name,
			cleanName: a.CleanName(),
			def:       a,
		})
	}

	processed := make(map[string]bool)

	sortedKeys := make([]string, 0, len(inputMap))
	for key := range inputMap {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		value := inputMap[key]
		if flagDef, isFlag := flagDefs[key]; isFlag {
			flagArgs, err := formatFlagWithDef(key, value, flagDef)
			if err != nil {
				return nil, fmt.Errorf("flag %q: %w", key, err)
			}
			args = append(args, flagArgs...)
			processed[key] = true
		}
	}

	doubleDashInserted := false

	for _, ad := range argDefs {
		value, exists := inputMap[ad.cleanName]
		if !exists {
			continue
		}
		processed[ad.cleanName] = true

		if !doubleDashInserted && (ad.def.DoubleDash == "required" || ad.def.DoubleDash == "optional") {
			args = append(args, "--")
			doubleDashInserted = true
		}

		switch v := value.(type) {
		case []any:
			for _, item := range v {
				args = append(args, argvToken(item))
			}
		case []string:
			args = append(args, v...)
		case string:
			args = append(args, v)
		case nil:
		default:
			args = append(args, argvToken(v))
		}
	}

	for key := range inputMap {
		if !processed[key] {
			return nil, fmt.Errorf("unknown field %q: not defined as a flag or arg in the usage spec for this command", key)
		}
	}

	return args, nil
}

func formatFlagWithDef(name string, value any, flagDef Flag) ([]string, error) {
	prefix := "--"
	if len(name) == 1 {
		prefix = "-"
	}
	flagName := prefix + name

	if flagDef.Count {
		count := 0
		switch v := value.(type) {
		case int:
			count = v
		case int64:
			count = int(v)
		case float64:
			count = int(v)
		case bool:
			if v {
				count = 1
			}
		}
		if count <= 0 {
			return nil, nil
		}
		var args []string
		for i := 0; i < count; i++ {
			args = append(args, flagName)
		}
		return args, nil
	}

	switch v := value.(type) {
	case bool:
		if v {
			return []string{flagName}, nil
		}
		if flagDef.Negate != "" {
			return []string{flagDef.Negate}, nil
		}
		return nil, nil
	case string:
		return []string{flagName, v}, nil
	case []any:
		var args []string
		for _, item := range v {
			args = append(args, flagName, argvToken(item))
		}
		return args, nil
	case nil:
		return nil, nil
	default:
		return []string{flagName, argvToken(v)}, nil
	}
}

// argvToken renders one non-string value as one argv token: canonical
// compact JSON (conventions v2 value grammar). Strings pass verbatim and are
// handled before this; JSON canonicalization replaces Go's %v (which printed
// objects as map[k:v] and large floats in exponent form).
func argvToken(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		// Values arrive from JSON, so this is unreachable for wire inputs;
		// fall back to %v rather than panic for hand-constructed ones.
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// cliResult is a completed process's raw capture; decoding stdout into an
// output value is a separate step (decodeStdout) driven by the binding's
// declared stdout mode.
type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
	// stderrTruncated records that the stderr capture overflowed its cap.
	// Diagnostics volume never fails a successful invocation (truncate, mark,
	// carry on); only stdout overflow is fatal, because a truncated document
	// cannot be decoded.
	stderrTruncated bool
}

func runCLI(ctx context.Context, binName string, args []string, bindCtx map[string]any, stdin []byte) (*cliResult, error) {
	cmd := exec.CommandContext(ctx, binName, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	if env := openbindings.ContextEnvironment(bindCtx); len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdout := &cappedBuffer{limit: maxCLIOutputBytes}
	stderr := &tailBuffer{limit: maxCLIOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	if stdout.overflow {
		return nil, fmt.Errorf("command %q output exceeded %d bytes", binName, maxCLIOutputBytes)
	}

	return &cliResult{
		stdout:          stdout.String(),
		stderr:          stderr.String(),
		exitCode:        exitCode,
		stderrTruncated: stderr.truncated,
	}, nil
}

// wrapText is the raw-capture record used in non-zero-exit error details.
func wrapText(stdoutStr, stderrStr string) map[string]any {
	output := map[string]any{"stdout": stdoutStr}
	if stderrStr != "" {
		output["stderr"] = stderrStr
	}
	return output
}

func resolveCommandArtifact(ctx context.Context, argv []string) (string, error) {
	binName := argv[0]
	args := argv[1:]

	cmd := exec.CommandContext(ctx, binName, args...)
	stdout := &cappedBuffer{limit: maxCLIOutputBytes}
	cmd.Stdout = stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command %q failed: %w", binName, err)
	}
	if stdout.overflow {
		return "", fmt.Errorf("command %q output exceeded %d bytes", binName, maxCLIOutputBytes)
	}

	return stdout.String(), nil
}

// maxCLIOutputBytes bounds stdout/stderr captured from a spawned command, so a
// runaway process cannot exhaust memory.
const maxCLIOutputBytes = 10 << 20 // 10 MiB

// maxStderrTrailerBytes bounds the x-stderr trailer value. Trailing metadata
// is header-shaped and may cross header-framed transports; the full capture
// (up to maxCLIOutputBytes) still travels in non-success error details.
const maxStderrTrailerBytes = 64 << 10 // 64 KiB

// cappedBuffer stops growing past limit and records the overflow, rather than
// letting an unbounded child fill memory. It always reports a full write so the
// child is not killed by a short-write error; the caller inspects overflow
// after the command completes.
//
// bytes.Buffer is held as a field, NOT embedded: embedding would promote
// bytes.Buffer.ReadFrom, which os/exec's io.Copy prefers over Write, bypassing
// the cap entirely.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.overflow {
		return len(p), nil
	}
	if c.buf.Len()+len(p) > c.limit {
		c.overflow = true
		if remain := c.limit - c.buf.Len(); remain > 0 {
			c.buf.Write(p[:remain])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
func (c *cappedBuffer) Len() int       { return c.buf.Len() }

// tailBuffer keeps the MOST RECENT limit bytes: stderr is diagnostics whose
// operative lines (errors, summaries) print last, and whose volume never
// fails a successful invocation — old bytes are discarded as they stream.
type tailBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.truncated = true
		// Copy down rather than re-slice so the discarded prefix is freed.
		keep := t.buf[len(t.buf)-t.limit:]
		t.buf = append(t.buf[:0], keep...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
