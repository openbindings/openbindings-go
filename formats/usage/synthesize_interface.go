package usage

import (
	"fmt"
	"path/filepath"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

const (
	schemaTypeString  = "string"
	schemaTypeBoolean = "boolean"
	schemaTypeInteger = "integer"
	schemaTypeArray   = "array"
	schemaTypeObject  = "object"
)

// synthesizeFromArtifactText is the bare-text entry used by tests.
func synthesizeFromArtifactText(text string) (openbindings.Interface, error) {
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return openbindings.Interface{}, fmt.Errorf("parse usage content: %w", err)
	}
	return buildInterfaceFromSpec(spec, openbindings.Source{
		Format:  "usage@" + MaxTestedVersion,
		Content: text,
	})
}

// absolutizeArtifactLocation is the authoring-side intake rule shared by
// synthesis and inspection: a relative file path is operator convenience at
// authoring time and is absolutized against the working directory, so the
// strict loader (artifactText — one loader for every lane) accepts it AND
// the location emitted into a synthesized source is invocable as written.
// Inline content, exec: locators, and URLs pass through untouched (URLs get
// the loader's clear refusal).
func absolutizeArtifactLocation(location string, content any) (string, error) {
	if content != nil || location == "" ||
		strings.HasPrefix(location, "exec:") || strings.Contains(location, "://") ||
		filepath.IsAbs(location) {
		return location, nil
	}
	abs, err := filepath.Abs(location)
	if err != nil {
		return "", fmt.Errorf("resolve usage artifact path: %w", err)
	}
	return abs, nil
}

// floorOutputSchema is the floor-true derived output contract: the text
// assumption always yields a string, stamped with in-schema x-ob
// provenance so the diagnostics key on it and elections self-clear it.
func floorOutputSchema() openbindings.JSONSchema {
	return openbindings.JSONSchema{
		"type": "string",
		"x-ob": map[string]any{"floor": "text"},
	}
}

// operationName derives the op key for a command path: dot-joined (unique
// per command tree, identifier-safe).
func operationName(path []string) string { return strings.Join(path, ".") }

// commandRef derives the binding ref for a command path: the format's own
// grammar, space-separated ("" = the root command).
func commandRef(path []string) string { return strings.Join(path, " ") }

// buildInterfaceFromSpec synthesizes an interface from a bare artifact:
// one operation per bindable command, bound by command-path ref.
func buildInterfaceFromSpec(spec *Spec, sourceEntry openbindings.Source) (openbindings.Interface, error) {
	meta := spec.Meta()

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         meta.Name,
		Version:      meta.Version,
		Description:  meta.About,
		Operations:   map[string]openbindings.Operation{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
		Bindings: map[string]openbindings.BindingEntry{},
	}

	addOp := func(name, ref, help string, path []string, cmd *Command, inherited []Flag) error {
		op := openbindings.Operation{Description: help}
		if len(path) > 1 {
			op.Tags = make([]string, len(path)-1)
			copy(op.Tags, path[:len(path)-1])
		}
		if cmd != nil {
			for _, alias := range cmd.Aliases {
				for _, an := range alias.Names {
					if !alias.Hide {
						op.Aliases = append(op.Aliases, an)
					}
				}
			}
			inputSchema, serr := generateInputSchema(*cmd, inherited)
			if serr != nil {
				return serr
			}
			if inputSchema != nil {
				op.Input = inputSchema
			}
		}
		op.Output = floorOutputSchema()

		iface.Operations[name] = op
		iface.Bindings[name+"."+DefaultSourceName] = openbindings.BindingEntry{
			Operation: name,
			Source:    DefaultSourceName,
			Ref:       ref,
		}
		return nil
	}

	if rc := rootCommand(spec); rc != nil {
		rootName := meta.Bin
		if rootName == "" {
			rootName = meta.Name
		}
		if rootName != "" {
			if err := addOp(rootName, "", meta.About, []string{rootName}, rc, nil); err != nil {
				return openbindings.Interface{}, err
			}
		}
	}
	var walkErr error
	walkWithGlobals(spec, func(path []string, cmd Command, inherited []Flag) {
		if walkErr != nil || len(path) == 0 || cmd.SubcommandRequired {
			return
		}
		c := cmd
		if err := addOp(operationName(path), commandRef(path), cmd.Help, path, &c, inherited); err != nil {
			walkErr = err
		}
	})
	if walkErr != nil {
		return openbindings.Interface{}, walkErr
	}

	return iface, nil
}

func walkWithGlobals(spec *Spec, fn func(path []string, cmd Command, inheritedGlobals []Flag)) {
	// Collect top-level global flags so they're inherited by all commands.
	var rootGlobals []Flag
	for _, f := range spec.Flags() {
		if f.Global {
			rootGlobals = append(rootGlobals, f)
		}
	}
	for _, cmd := range spec.Commands() {
		walkCommandWithGlobals(nil, cmd, rootGlobals, fn)
	}
}

func walkCommandWithGlobals(path []string, cmd Command, inheritedGlobals []Flag, fn func([]string, Command, []Flag)) {
	currentPath := make([]string, len(path)+1)
	copy(currentPath, path)
	currentPath[len(path)] = cmd.Name

	var newGlobals []Flag
	newGlobals = append(newGlobals, inheritedGlobals...)
	for _, f := range cmd.Flags {
		if f.Global {
			newGlobals = append(newGlobals, f)
		}
	}

	fn(currentPath, cmd, inheritedGlobals)

	for _, sub := range cmd.Commands {
		walkCommandWithGlobals(currentPath, sub, newGlobals, fn)
	}
}

// rootCommand returns a synthetic Command representing the root invocation if the spec
// has top-level args or non-global flags. Returns nil if there is no callable root level.
func rootCommand(spec *Spec) *Command {
	topFlags := spec.Flags()
	topArgs := spec.Args()

	var rootFlags []Flag
	for _, f := range topFlags {
		if !f.Global {
			rootFlags = append(rootFlags, f)
		}
	}

	if len(rootFlags) == 0 && len(topArgs) == 0 {
		return nil
	}

	// Include all top-level flags (global + non-global) since the root command uses them all.
	return &Command{
		Flags: topFlags,
		Args:  topArgs,
	}
}

func generateInputSchema(cmd Command, inheritedGlobals []Flag) (map[string]any, error) {
	properties := make(map[string]any)
	seen := make(map[string]string)
	var required []string

	allFlags := cmd.AllFlags(inheritedGlobals)
	for _, flag := range allFlags {
		name := flag.PrimaryName()
		if name == "" {
			continue
		}

		if existing, ok := seen[name]; ok {
			return nil, fmt.Errorf("name collision in command %q: %q is used by both %s and flag --%s",
				cmd.Name, name, existing, name)
		}
		seen[name] = fmt.Sprintf("flag --%s", name)

		prop := generateFlagSchema(flag)
		if prop != nil {
			properties[name] = prop
		}

		if flag.Required && flag.Default == nil {
			required = append(required, name)
		}
	}

	for _, arg := range cmd.Args {
		name := arg.CleanName()
		if name == "" {
			continue
		}

		if existing, ok := seen[name]; ok {
			return nil, fmt.Errorf("name collision in command %q: %q is used by both %s and arg <%s>",
				cmd.Name, name, existing, name)
		}
		seen[name] = fmt.Sprintf("arg <%s>", name)

		prop := generateArgSchema(arg)
		if prop != nil {
			properties[name] = prop
		}

		if arg.IsRequired() && arg.Default == nil {
			required = append(required, name)
		}
	}

	if len(properties) == 0 {
		return nil, nil
	}

	schema := map[string]any{
		"type":       schemaTypeObject,
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema, nil
}

func generateFlagSchema(flag Flag) map[string]any {
	prop := make(map[string]any)

	parsed := flag.ParseUsage()

	if flag.Count {
		prop["type"] = schemaTypeInteger
		if flag.Help != "" {
			prop["description"] = flag.Help
		}
		if flag.Default != nil {
			prop["default"] = flag.Default
		}
		return prop
	}

	takesValue := parsed.ArgName != "" || len(flag.Args) > 0

	if !takesValue {
		prop["type"] = schemaTypeBoolean
		if flag.Help != "" {
			prop["description"] = flag.Help
		}
		if flag.Default != nil {
			prop["default"] = flag.Default
		}
		return prop
	}

	if flag.Var {
		itemSchema := map[string]any{"type": schemaTypeString}
		if len(flag.Choices) > 0 {
			itemSchema["enum"] = flag.Choices
		}
		prop["type"] = schemaTypeArray
		prop["items"] = itemSchema
		if flag.VarMin != nil {
			prop["minItems"] = *flag.VarMin
		}
		if flag.VarMax != nil {
			prop["maxItems"] = *flag.VarMax
		}
	} else {
		prop["type"] = schemaTypeString
		if len(flag.Choices) > 0 {
			prop["enum"] = flag.Choices
		}
	}

	if flag.Help != "" {
		prop["description"] = flag.Help
	}
	if flag.Default != nil {
		prop["default"] = flag.Default
	}

	return prop
}

func generateArgSchema(arg Arg) map[string]any {
	prop := make(map[string]any)

	if arg.IsVariadic() {
		itemSchema := map[string]any{"type": schemaTypeString}
		if len(arg.Choices) > 0 {
			itemSchema["enum"] = arg.Choices
		}
		prop["type"] = schemaTypeArray
		prop["items"] = itemSchema
		if arg.VarMin != nil {
			prop["minItems"] = *arg.VarMin
		} else if arg.IsRequired() && arg.Default == nil {
			prop["minItems"] = 1
		}
		if arg.VarMax != nil {
			prop["maxItems"] = *arg.VarMax
		}
	} else {
		prop["type"] = schemaTypeString
		if len(arg.Choices) > 0 {
			prop["enum"] = arg.Choices
		}
	}

	if arg.Help != "" {
		prop["description"] = arg.Help
	}
	if arg.Default != nil {
		prop["default"] = arg.Default
	}

	return prop
}
