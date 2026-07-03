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

	binName, cmdArgs, ierr := e.resolveCommand(bctx, args, input)
	if ierr != nil {
		inv.FireError(ierr)
		return
	}

	output, exitCode, runErr := runCLI(bctx, binName, cmdArgs, args.Context)
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
	if exitCode != 0 {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", exitCode),
			Details: map[string]any{"exitCode": exitCode, "output": output},
		})
		return
	}

	// Surface the success exit code as trailing metadata (parity with the
	// old Status field) without polluting the bare output value.
	inv.SetTrailer(openbindings.Metadata{"x-exit-code": {strconv.Itoa(exitCode)}})
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// resolveCommand determines the binary and argv for an invocation. A
// "binary" metadata hint dispatches the ref directly; otherwise the usage
// spec is loaded and the ref resolved to a command. Returns a terminal
// *InvocationError (never a side effect) on any resolution failure.
func (e *Invoker) resolveCommand(ctx context.Context, args *openbindings.BindingInvocationArgs, input any) (string, []string, *openbindings.InvocationError) {
	if binary := metadataBinary(args.Context); binary != "" {
		cmdArgs, err := buildDirectArgsFromRef(args.Ref, input)
		if err != nil {
			return "", nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
		}
		return binary, cmdArgs, nil
	}

	spec, err := e.cachedLoadSpec(ctx, args.Source.Location, args.Source.Content)
	if err != nil {
		return "", nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: err.Error()}
	}

	meta := spec.Meta()
	binName := meta.Bin
	if binName == "" {
		binName = meta.Name
	}
	if binName == "" {
		return "", nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: "usage spec does not define a binary name (bin or name)",
		}
	}

	ref := strings.TrimSpace(args.Ref)
	if ref == "" {
		// Root invocation: no subcommand, use top-level flags and args.
		rootCmd := rootCommand(spec)
		if rootCmd == nil {
			rootCmd = &Command{Flags: spec.Flags(), Args: spec.Args()}
		}
		cmdArgs, err := buildCLIArgs(nil, rootCmd, nil, input)
		if err != nil {
			return "", nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
		}
		return binName, cmdArgs, nil
	}

	found, err := findCommand(spec, ref)
	if err != nil {
		return "", nil, &openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: err.Error()}
	}
	cmdArgs, err := buildCLIArgs(found.path, found.cmd, found.inheritedFlags, input)
	if err != nil {
		return "", nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	return binName, cmdArgs, nil
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

func loadSpec(ctx context.Context, location string, content any) (*Spec, error) {
	// Prefer inline content when provided — avoids redundant disk reads when
	// callers (e.g. Sync) already have fresh bytes.
	if content != nil {
		switch c := content.(type) {
		case string:
			spec, err := ParseKDL([]byte(c))
			if err != nil {
				return nil, fmt.Errorf("parse usage content: %w", err)
			}
			return spec, nil
		case []byte:
			spec, err := ParseKDL(c)
			if err != nil {
				return nil, fmt.Errorf("parse usage content: %w", err)
			}
			return spec, nil
		default:
			return nil, fmt.Errorf("unsupported content type %T (expected string or []byte)", content)
		}
	}

	if location != "" {
		if strings.HasPrefix(location, "exec:") {
			resolved, err := resolveCommandArtifact(ctx, location)
			if err != nil {
				return nil, fmt.Errorf("resolve cmd artifact: %w", err)
			}
			spec, err := ParseKDL([]byte(resolved))
			if err != nil {
				return nil, fmt.Errorf("parse usage content from exec: %w", err)
			}
			return spec, nil
		}

		spec, err := ParseFile(location)
		if err != nil {
			return nil, fmt.Errorf("parse usage spec: %w", err)
		}
		return spec, nil
	}

	return nil, fmt.Errorf("source must have location or content")
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
				args = append(args, fmt.Sprintf("%v", item))
			}
		case []string:
			args = append(args, v...)
		case string:
			args = append(args, v)
		case nil:
		default:
			args = append(args, fmt.Sprintf("%v", v))
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
	case float64:
		return []string{flagName, fmt.Sprintf("%v", v)}, nil
	case int, int64:
		return []string{flagName, fmt.Sprintf("%d", v)}, nil
	case []any:
		var args []string
		for _, item := range v {
			args = append(args, flagName, fmt.Sprintf("%v", item))
		}
		return args, nil
	case nil:
		return nil, nil
	default:
		return []string{flagName, fmt.Sprintf("%v", v)}, nil
	}
}

func runCLI(ctx context.Context, binName string, args []string, bindCtx map[string]any) (any, int, error) {
	cmd := exec.CommandContext(ctx, binName, args...)

	if env := openbindings.ContextEnvironment(bindCtx); len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdout := &cappedBuffer{limit: maxCLIOutputBytes}
	stderr := &cappedBuffer{limit: maxCLIOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, 1, err
		}
	}

	if stdout.overflow || stderr.overflow {
		return nil, 1, fmt.Errorf("command %q output exceeded %d bytes", binName, maxCLIOutputBytes)
	}

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	if exitCode == 0 && len(stdoutStr) > 0 {
		trimmed := strings.TrimSpace(stdoutStr)
		// Bare-parse machine-shaped stdout: objects and arrays by shape, plus
		// the JSON literals and strings a contract-shaped output can be (an
		// operation whose output is `null` — a kv-store get miss, a no-output
		// op — must round-trip as null, not as a {stdout: "null"} wrapper).
		// Bare numbers stay wrapped: a human lane printing "42" is far more
		// plausible than a number-typed operation output on this transport.
		bareParse := openbindings.MaybeJSON(trimmed) ||
			trimmed == "null" || trimmed == "true" || trimmed == "false" ||
			strings.HasPrefix(trimmed, `"`)
		if bareParse {
			var parsed any
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				if stderrStr != "" {
					return map[string]any{
						"data":   parsed,
						"stderr": stderrStr,
					}, 0, nil
				}
				return parsed, 0, nil
			}
		}
	}

	output := map[string]any{
		"stdout": stdoutStr,
	}
	if stderrStr != "" {
		output["stderr"] = stderrStr
	}

	return output, exitCode, nil
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
