package usage

import (
	"regexp"
	"strings"
)

// IsRequired returns true if the argument is required (uses <name> syntax).
// Optional arguments use [name] syntax.
func (a Arg) IsRequired() bool {
	name := a.Name
	if name == "" {
		return false
	}
	// Strip trailing ellipsis for variadic args before checking brackets
	name = strings.TrimSuffix(name, "...")
	// Required args use <name>, optional use [name]
	return strings.HasPrefix(name, "<") && strings.HasSuffix(name, ">")
}

// IsVariadic returns true if the argument accepts multiple values.
// This is indicated by trailing "..." in the name or var=#true.
func (a Arg) IsVariadic() bool {
	if a.Var {
		return true
	}
	return strings.HasSuffix(a.Name, "...")
}

// CleanName returns the argument name without brackets or ellipsis.
func (a Arg) CleanName() string {
	name := a.Name
	// Strip ellipsis first (it's always at the end)
	name = strings.TrimSuffix(name, "...")
	// Then strip brackets
	name = strings.TrimPrefix(name, "<")
	name = strings.TrimPrefix(name, "[")
	name = strings.TrimSuffix(name, ">")
	name = strings.TrimSuffix(name, "]")
	return name
}

// ParsedUsage contains the parsed components of a flag usage string.
type ParsedUsage struct {
	Short   []string // e.g., ["v", "V"]
	Long    []string // e.g., ["verbose", "version"]
	ArgName string   // e.g., "user" from "--user <user>"
}

// usagePattern matches flag usage strings like "-v --verbose" or "-u --user <user>".
// It also handles variadic shorthand like "--include..." (trailing dots are consumed
// but not included in submatch group 1).
var usagePattern = regexp.MustCompile(`(-[a-zA-Z]|--[a-zA-Z][-a-zA-Z0-9]*)(?:\.\.\.)?`)
var argPattern = regexp.MustCompile(`<([^>]+)>|\[([^\]]+)\]`)

// ParseUsage extracts short flags, long flags, and argument name from the usage string.
// For example, "-u --user <user>" returns {Short: ["u"], Long: ["user"], ArgName: "user"}.
func (f Flag) ParseUsage() ParsedUsage {
	result := ParsedUsage{}
	if f.Usage == "" {
		return result
	}

	// Extract flags (submatch[1] contains the flag without trailing "...")
	matches := usagePattern.FindAllStringSubmatch(f.Usage, -1)
	for _, m := range matches {
		flag := m[1]
		if strings.HasPrefix(flag, "--") {
			result.Long = append(result.Long, strings.TrimPrefix(flag, "--"))
		} else if strings.HasPrefix(flag, "-") {
			result.Short = append(result.Short, strings.TrimPrefix(flag, "-"))
		}
	}

	// Extract arg name
	argMatch := argPattern.FindStringSubmatch(f.Usage)
	if len(argMatch) > 1 {
		if argMatch[1] != "" {
			result.ArgName = argMatch[1]
		} else if argMatch[2] != "" {
			result.ArgName = argMatch[2]
		}
	}

	return result
}

// PrimaryName returns the best name for the flag (prefers long over short).
func (f Flag) PrimaryName() string {
	parsed := f.ParseUsage()
	if len(parsed.Long) > 0 {
		return parsed.Long[0]
	}
	if len(parsed.Short) > 0 {
		return parsed.Short[0]
	}
	return f.Usage
}

// FullPath returns the full command path from root to this command.
// The ancestors slice should contain the names of parent commands.
// The returned slice is always a new allocation, so callers may freely
// pass it to sibling commands without aliasing concerns.
func (c Command) FullPath(ancestors []string) []string {
	out := make([]string, len(ancestors)+1)
	copy(out, ancestors)
	out[len(ancestors)] = c.Name
	return out
}

// AllFlags returns all flags applicable to this command, including inherited globals.
// Pass the accumulated global flags from parent commands.
func (c Command) AllFlags(inheritedGlobals []Flag) []Flag {
	var globals []Flag
	var locals []Flag

	// First, collect this command's flags
	for _, f := range c.Flags {
		if f.Global {
			globals = append(globals, f)
		} else {
			locals = append(locals, f)
		}
	}

	// Merge inherited globals (avoid duplicates by usage string)
	seen := make(map[string]bool)
	for _, f := range globals {
		seen[f.Usage] = true
	}
	for _, f := range inheritedGlobals {
		if !seen[f.Usage] {
			globals = append(globals, f)
			seen[f.Usage] = true
		}
	}

	// Return globals first, then locals
	return append(globals, locals...)
}

// Walk calls fn for each command in the tree, depth-first.
// The path includes all ancestor command names.
func (s *Spec) Walk(fn func(path []string, cmd Command)) {
	for _, cmd := range s.Commands() {
		walkCommand([]string{}, cmd, fn)
	}
}

func walkCommand(path []string, cmd Command, fn func([]string, Command)) {
	currentPath := cmd.FullPath(path)
	fn(currentPath, cmd)
	for _, sub := range cmd.Commands {
		walkCommand(currentPath, sub, fn)
	}
}

// FindCommand finds a command by its path (e.g., ["config", "set"]).
// Returns nil if not found.
func (s *Spec) FindCommand(path []string) *Command {
	if len(path) == 0 {
		return nil
	}

	commands := s.Commands()
	var current *Command

	for i, name := range path {
		found := false
		for j := range commands {
			if commands[j].Name == name {
				current = &commands[j]
				if i < len(path)-1 {
					commands = current.Commands
				}
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}

	return current
}
