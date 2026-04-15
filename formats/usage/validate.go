package usage

import (
	"fmt"
	"sort"
	"strings"
)

type validateOptions struct {
	requireName             bool
	requireBin              bool
	rejectUnknownNodes      bool
	requireCommandHelp      bool
	requireSupportedVersion bool
}

// ValidateOption configures Spec.Validate.
type ValidateOption func(*validateOptions)

// WithRequireName controls whether Spec.Validate requires the `name` field.
// Default: false.
func WithRequireName() ValidateOption {
	return func(o *validateOptions) { o.requireName = true }
}

// WithRequireBin controls whether Spec.Validate requires the `bin` field.
// Default: false.
func WithRequireBin() ValidateOption {
	return func(o *validateOptions) { o.requireBin = true }
}

// WithRejectUnknownNodes treats unknown top-level nodes as errors.
// Default: false (forward-compatible, unknowns allowed).
func WithRejectUnknownNodes() ValidateOption {
	return func(o *validateOptions) { o.rejectUnknownNodes = true }
}

// WithRequireCommandHelp controls whether commands must have a help string.
// Default: false.
func WithRequireCommandHelp() ValidateOption {
	return func(o *validateOptions) { o.requireCommandHelp = true }
}

// WithRequireSupportedVersion requires min_usage_version to be within the SDK's supported range.
// By default, versions outside the supported range are allowed for forward compatibility.
func WithRequireSupportedVersion() ValidateOption {
	return func(o *validateOptions) { o.requireSupportedVersion = true }
}

// Validate performs structural validation of the Usage spec.
// It checks for common issues and can be configured with ValidateOption functions.
func (s *Spec) Validate(opts ...ValidateOption) error {
	o := validateOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	var errs []string
	meta := s.Meta()

	if o.requireSupportedVersion {
		if strings.TrimSpace(meta.MinUsageVersion) == "" {
			errs = append(errs, "min_usage_version: required")
		} else {
			ok, err := IsSupportedVersion(meta.MinUsageVersion)
			if err != nil {
				errs = append(errs, "min_usage_version: invalid version")
			} else if !ok {
				errs = append(errs, fmt.Sprintf("min_usage_version: unsupported version %q (supported %s-%s)", meta.MinUsageVersion, MinSupportedVersion, MaxTestedVersion))
			}
		}
	}

	if o.requireName && strings.TrimSpace(meta.Name) == "" {
		errs = append(errs, "name: required")
	}

	if o.requireBin && strings.TrimSpace(meta.Bin) == "" {
		errs = append(errs, "bin: required")
	}

	// Check for unknown nodes if strict mode.
	// Structural nodes (cmd, flag, arg, etc.) are already filtered out of Meta.Unknown
	// by decodeMeta, so this only catches truly unrecognized nodes.
	if o.rejectUnknownNodes && len(meta.Unknown) > 0 {
		names := make([]string, 0, len(meta.Unknown))
		for _, n := range meta.Unknown {
			names = append(names, n.Name)
		}
		sort.Strings(names)
		errs = append(errs, fmt.Sprintf("unknown nodes: %s", strings.Join(names, ", ")))
	}

	// Validate commands
	commands := s.Commands()
	validateDuplicateCommands(&errs, "", commands)
	for _, cmd := range commands {
		validateCommand(&errs, []string{cmd.Name}, cmd, o)
	}

	// Validate top-level flags
	for _, flag := range s.Flags() {
		validateFlag(&errs, "", flag)
	}

	// Validate top-level args
	for _, arg := range s.Args() {
		validateArg(&errs, "", arg)
	}

	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Problems: errs}
}

func validateDuplicateCommands(errs *[]string, prefix string, commands []Command) {
	if len(commands) == 0 {
		return
	}
	seen := make(map[string]bool)
	for _, cmd := range commands {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			continue
		}
		if seen[name] {
			if prefix == "" {
				*errs = append(*errs, fmt.Sprintf("cmd: duplicate command %q at top level", name))
			} else {
				*errs = append(*errs, fmt.Sprintf("cmd[%s]: duplicate subcommand %q", prefix, name))
			}
			continue
		}
		seen[name] = true
	}
}

func validateCommand(errs *[]string, path []string, cmd Command, o validateOptions) {
	pathStr := strings.Join(path, " ")

	if strings.TrimSpace(cmd.Name) == "" {
		*errs = append(*errs, fmt.Sprintf("cmd[%s]: name is empty", pathStr))
	}

	if o.requireCommandHelp && strings.TrimSpace(cmd.Help) == "" {
		*errs = append(*errs, fmt.Sprintf("cmd[%s]: help required", pathStr))
	}

	// Check for duplicate aliases
	aliasSet := make(map[string]bool)
	for _, alias := range cmd.Aliases {
		for _, name := range alias.Names {
			if aliasSet[name] {
				*errs = append(*errs, fmt.Sprintf("cmd[%s]: duplicate alias %q", pathStr, name))
			}
			aliasSet[name] = true
		}
	}

	// Validate nested commands
	validateDuplicateCommands(errs, pathStr, cmd.Commands)
	for _, sub := range cmd.Commands {
		subPath := make([]string, len(path)+1)
		copy(subPath, path)
		subPath[len(path)] = sub.Name
		validateCommand(errs, subPath, sub, o)
	}

	// Validate flags
	for _, flag := range cmd.Flags {
		validateFlag(errs, pathStr, flag)
	}

	// Validate args
	for _, arg := range cmd.Args {
		validateArg(errs, pathStr, arg)
	}
}

func validateFlag(errs *[]string, cmdPath string, flag Flag) {
	prefix := "flag"
	if cmdPath != "" {
		prefix = fmt.Sprintf("cmd[%s].flag", cmdPath)
	}

	if strings.TrimSpace(flag.Usage) == "" {
		*errs = append(*errs, fmt.Sprintf("%s: usage is empty", prefix))
		return
	}

	// Check that flag usage contains at least one flag pattern
	parsed := flag.ParseUsage()
	if len(parsed.Short) == 0 && len(parsed.Long) == 0 {
		*errs = append(*errs, fmt.Sprintf("%s[%s]: no short or long flag found in usage", prefix, flag.Usage))
	}

	// Validate var constraints
	if flag.VarMin != nil && flag.VarMax != nil && *flag.VarMin > *flag.VarMax {
		*errs = append(*errs, fmt.Sprintf("%s[%s]: var_min > var_max", prefix, flag.Usage))
	}
}

func validateArg(errs *[]string, cmdPath string, arg Arg) {
	prefix := "arg"
	if cmdPath != "" {
		prefix = fmt.Sprintf("cmd[%s].arg", cmdPath)
	}

	if strings.TrimSpace(arg.Name) == "" {
		*errs = append(*errs, fmt.Sprintf("%s: name is empty", prefix))
		return
	}

	// Validate var constraints
	if arg.VarMin != nil && arg.VarMax != nil && *arg.VarMin > *arg.VarMax {
		*errs = append(*errs, fmt.Sprintf("%s[%s]: var_min > var_max", prefix, arg.Name))
	}

	// Validate double_dash value if present
	if arg.DoubleDash != "" {
		switch arg.DoubleDash {
		case "required", "optional", "automatic", "preserve":
			// valid
		default:
			*errs = append(*errs, fmt.Sprintf("%s[%s]: invalid double_dash value %q (must be required, optional, automatic, or preserve)", prefix, arg.Name, arg.DoubleDash))
		}
	}
}

// ValidationError is a multi-problem validation error.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "invalid spec"
	}
	return "invalid spec: " + strings.Join(e.Problems, "; ")
}
