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

	"github.com/google/shlex"
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
	// wrapper loaded, ref is a command string, no unit and so no delivery.
	if binary := metadataBinary(args.Context); binary != "" {
		e.runDirect(bctx, args, inv, binary, input)
		return
	}

	// Resolve the wrapper unit: the complete invocation recipe.
	w, lerr := e.cachedLoadWrapper(bctx, args.Source.Location, args.Source.Content)
	if lerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: lerr.Error()})
		return
	}
	unit, unitName, rerr := w.resolveUnitRef(args.Ref)
	if rerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: rerr.Error()})
		return
	}
	// Per-unit validation (§10): failure makes THIS binding unactionable.
	cmd, inherited, cmdPath, verr := w.validateUnit(unit, unitName)
	if verr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: verr.Error()})
		return
	}

	input, stdin, cleanup, derr := applyDelivery(input, unit)
	if derr != nil {
		// Pre-spawn value refusals (the delivery cap) are validation failures.
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: derr.Error()})
		return
	}
	defer cleanup()

	meta := w.kdl.Meta()
	binName := meta.Bin
	if binName == "" {
		binName = meta.Name
	}
	if binName == "" {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "usage artifact does not define a binary name (bin or name)"})
		return
	}
	cmdArgs, aerr := buildCLIArgs(cmdPath, cmd, inherited, input)
	if aerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: aerr.Error()})
		return
	}

	res, runErr := runCLI(bctx, binName, cmdArgs, args.Context, stdin)
	// Materialized files only need to outlive the process; the deferred call
	// (idempotent) covers the error paths above.
	cleanup()
	if bctx.Err() != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeCancelled, Message: "operation cancelled"})
		return
	}
	if runErr != nil {
		// runCLI returns a non-nil error only for spawn failures (not for a
		// clean non-zero exit). A missing binary is a configuration error.
		code := openbindings.ErrCodeExecutionFailed
		if errors.Is(runErr, exec.ErrNotFound) {
			code = openbindings.ErrCodeSourceConfigError
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: runErr.Error()})
		return
	}
	if !unit.exitOK(res.exitCode) {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", res.exitCode),
			Details: map[string]any{"exitCode": res.exitCode, "output": wrapText(res.stdout, res.stderr)},
		})
		return
	}

	// exit.values maps a declared-ok code to a literal output (§7); the unit
	// parser guarantees stdout "none" in that case.
	output, hasLit := unit.exitValue(res.exitCode)
	if !hasLit {
		var oerr *openbindings.InvocationError
		output, oerr = decodeStdout(res.stdout, unit.Stdout)
		if oerr != nil {
			inv.FireError(oerr)
			return
		}
	}

	emitWithDiagnostics(inv, output, res)
}

// emitWithDiagnostics stamps the diagnostics trailer and emits the output.
// Diagnostics ride trailing metadata, never the output value: the exit code
// and captured stderr (a filter command's human summary). The trailer is
// header-shaped, so stderr is bounded: the LAST maxStderrTrailerBytes (tails
// carry the operative lines), with an explicit truncation marker that also
// fires when the capture itself overflowed.
func emitWithDiagnostics(inv *openbindings.InvocationImpl[any, any], output any, res *cliResult) {
	trailer := openbindings.Metadata{"x-exit-code": {strconv.Itoa(res.exitCode)}}
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
// the format conventions): the ref is shlex-split as a command, every input
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
	output, oerr := decodeStdout(res.stdout, "")
	if oerr != nil {
		inv.FireError(oerr)
		return
	}
	emitWithDiagnostics(inv, output, res)
}

// loadWrapper loads an openbindings.usage document from inline content (the
// JSON-object source form, or text) or an absolute location.
func loadWrapper(_ context.Context, location string, content any) (*Wrapper, error) {
	if content != nil {
		return ParseWrapper(content)
	}
	if location == "" {
		return nil, fmt.Errorf("source must have location or content")
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return nil, fmt.Errorf("read openbindings.usage document: %w", err)
	}
	return ParseWrapper(data)
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
	args, err := shlex.Split(ref)
	if err != nil {
		return nil, err
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
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("ref is empty")
	}

	targetPath := strings.Fields(ref)
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

// decodeStdout maps a declared-ok exit's stdout to the output value per the
// unit's stdout mode (openbindings.usage.md §6). There is no heuristic: an
// undeclared lane means "text", a declaration in itself. stderr never
// participates: it is diagnostics and rides the x-stderr trailer.
func decodeStdout(stdoutStr, mode string) (any, *openbindings.InvocationError) {
	switch mode {
	case stdoutJSON:
		// Declared machine lane: stdout is one JSON value, numbers included.
		// Empty stdout is a null output (a command with nothing to say);
		// anything else that fails to parse is the command failing to honor
		// its machine lane — an error, never a silent wrap.
		trimmed := strings.TrimSpace(stdoutStr)
		if trimmed == "" {
			return nil, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeExecutionFailed,
				Message: fmt.Sprintf("binding declares JSON stdout, but the command's stdout is not valid JSON: %v", err),
				Details: map[string]any{"stdout": stdoutStr},
			}
		}
		return parsed, nil

	case stdoutNone:
		// Declared no-output lane: stdout is not consulted; exit.values may
		// supply the output upstream, else null.
		return nil, nil

	default: // "text", and the absent-mode default (a declaration, not a guess)
		// The output value is stdout with trailing newlines stripped, exactly
		// as command substitution $(...) reads a command's text output — the
		// final newline is line-termination framing, not payload
		// ("git rev-parse HEAD" means the hash, not hash-plus-newline).
		// Interior newlines are preserved.
		return strings.TrimRight(stdoutStr, "\r\n"), nil
	}
}

// wrapText is the raw-capture record used in non-zero-exit error details.
func wrapText(stdoutStr, stderrStr string) map[string]any {
	output := map[string]any{"stdout": stdoutStr}
	if stderrStr != "" {
		output["stderr"] = stderrStr
	}
	return output
}

func resolveCommandArtifact(ctx context.Context, location string) (string, error) {
	cmdStr := strings.TrimPrefix(location, "exec:")
	if cmdStr == "" {
		return "", fmt.Errorf("empty command in exec: artifact")
	}

	parts, err := shlex.Split(cmdStr)
	if err != nil {
		return "", fmt.Errorf("invalid command syntax: %w", err)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("empty command in exec: artifact")
	}

	binName := parts[0]
	args := parts[1:]

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
