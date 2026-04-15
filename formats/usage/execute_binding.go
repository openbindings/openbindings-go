package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/shlex"
	openbindings "github.com/openbindings/openbindings-go"
)

type specLoader func(ctx context.Context, location string, content any) (*Spec, error)

func executeBindingCached(ctx context.Context, input *openbindings.BindingExecutionInput, loader specLoader) *openbindings.ExecuteOutput {
	start := time.Now()

	var binName string
	var args []string

	binary := metadataBinary(input.Options)

	if binary != "" {
		binName = binary
		var err error
		args, err = buildDirectArgsFromRef(input.Ref, input.Input)
		if err != nil {
			return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
		}
	} else {
		spec, err := loader(ctx, input.Source.Location, input.Source.Content)
		if err != nil {
			return openbindings.FailedOutput(start, openbindings.ErrCodeSourceLoadFailed, err.Error())
		}

		meta := spec.Meta()
		binName = meta.Bin
		if binName == "" {
			binName = meta.Name
		}
		if binName == "" {
			return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError, "usage spec does not define a binary name (bin or name)")
		}

		ref := strings.TrimSpace(input.Ref)
		if ref == "" {
			// Root invocation: no subcommand, use top-level flags and args.
			rootCmd := rootCommand(spec)
			if rootCmd == nil {
				rootCmd = &Command{Flags: spec.Flags(), Args: spec.Args()}
			}
			args, err = buildCLIArgs(nil, rootCmd, nil, input.Input)
		} else {
			found, err2 := findCommand(spec, ref)
			if err2 != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound, err2.Error())
			}
			args, err = buildCLIArgs(found.path, found.cmd, found.inheritedFlags, input.Input)
		}
		if err != nil {
			return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
		}
	}

	output, status, err := runCLI(ctx, binName, args, input.Options)
	duration := time.Since(start).Milliseconds()

	if ctx.Err() != nil {
		return &openbindings.ExecuteOutput{
			DurationMs: duration,
			Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeCancelled,
				Message: "operation cancelled",
			},
		}
	}

	if err != nil {
		return &openbindings.ExecuteOutput{
			Output:     output,
			Status:     status,
			DurationMs: duration,
			Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeExecutionFailed,
				Message: err.Error(),
			},
		}
	}

	return &openbindings.ExecuteOutput{
		Output:     output,
		Status:     status,
		DurationMs: duration,
	}
}

// metadataBinary extracts the "binary" hint from execution options metadata.
func metadataBinary(opts *openbindings.ExecutionOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	if b, ok := opts.Metadata["binary"].(string); ok {
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

func runCLI(ctx context.Context, binName string, args []string, opts *openbindings.ExecutionOptions) (any, int, error) {
	cmd := exec.CommandContext(ctx, binName, args...)

	if opts != nil && len(opts.Environment) > 0 {
		cmd.Env = os.Environ()
		for k, v := range opts.Environment {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, 1, err
		}
	}

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	if exitCode == 0 && len(stdoutStr) > 0 {
		trimmed := strings.TrimSpace(stdoutStr)
		if openbindings.MaybeJSON(trimmed) {
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
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command %q failed: %w", binName, err)
	}

	return stdout.String(), nil
}
