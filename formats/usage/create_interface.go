package usage

import (
	"fmt"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/formattoken"
)

const (
	schemaTypeString  = "string"
	schemaTypeBoolean = "boolean"
	schemaTypeInteger = "integer"
	schemaTypeArray   = "array"
	schemaTypeObject  = "object"
)

// convertToInterfaceWithSpec builds an interface from a pre-loaded spec.
func convertToInterfaceWithSpec(spec *Spec, location string) (openbindings.Interface, error) {
	fromTok := formattoken.FormatToken{Name: "usage", Version: MaxTestedVersion}
	return buildInterfaceFromSpec(spec, location, fromTok.String(), openbindings.MaxTestedVersion)
}

func buildInterfaceFromSpec(spec *Spec, location, formatStr, obVersion string) (openbindings.Interface, error) {
	meta := spec.Meta()

	sourceEntry := openbindings.Source{
		Format: formatStr,
	}
	if location != "" {
		sourceEntry.Location = location
	}

	iface := openbindings.Interface{
		OpenBindings: obVersion,
		Name:         meta.Name,
		Version:      meta.Version,
		Description:  meta.About,
		Operations:   map[string]openbindings.Operation{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	bindings := map[string]openbindings.BindingEntry{}
	var dupes []string
	var schemaErr error

	// Synthesize a root operation if the spec has top-level args or non-global flags.
	// This covers single-command CLIs (grep, curl, jq) that have no subcommands.
	if rootCmd := rootCommand(spec); rootCmd != nil {
		opKey := meta.Bin
		if opKey == "" {
			opKey = meta.Name
		}
		if opKey != "" {
			op := openbindings.Operation{
				Description: meta.About,
			}
			inputSchema, err := generateInputSchema(*rootCmd, nil)
			if err != nil {
				return openbindings.Interface{}, err
			}
			if inputSchema != nil {
				op.Input = inputSchema
			}
			iface.Operations[opKey] = op
			bindings[opKey+"."+DefaultSourceName] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				// Empty ref signals "root invocation" to the executor
				// (see execute_binding.go: ref == "" branch). Setting
				// it to opKey would make the executor try to resolve
				// the binary name as a subcommand path and fail.
				Ref: "",
			}
		}
	}

	walkWithGlobals(spec, func(path []string, cmd Command, inheritedGlobals []Flag) {
		if schemaErr != nil {
			return
		}
		if len(path) == 0 {
			return
		}
		if cmd.SubcommandRequired {
			return
		}
		opKey := strings.Join(path, ".")
		if override, ok := cmd.Node.Props["opKey"]; ok {
			if s := override.String(); s != "" {
				opKey = s
			}
		}
		if _, exists := iface.Operations[opKey]; exists {
			dupes = append(dupes, opKey)
			return
		}

		op := openbindings.Operation{
			Description: cmd.Help,
		}

		if len(path) > 1 {
			op.Tags = make([]string, len(path)-1)
			copy(op.Tags, path[:len(path)-1])
		}

		for _, alias := range cmd.Aliases {
			for _, name := range alias.Names {
				if !alias.Hide {
					op.Aliases = append(op.Aliases, name)
				}
			}
		}

		inputSchema, err := generateInputSchema(cmd, inheritedGlobals)
		if err != nil {
			schemaErr = err
			return
		}
		if inputSchema != nil {
			op.Input = inputSchema
		}

		iface.Operations[opKey] = op
		bindingKey := opKey + "." + DefaultSourceName
		bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       strings.Join(path, " "),
		}
	})
	if schemaErr != nil {
		return openbindings.Interface{}, schemaErr
	}
	if len(dupes) > 0 {
		return openbindings.Interface{}, fmt.Errorf("duplicate command paths: %s", strings.Join(dupes, ", "))
	}
	iface.Bindings = bindings

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
