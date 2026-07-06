package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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

// wrapperForSource resolves any usage-family source to a Wrapper: a wrapper
// document loads directly; a bare kdl artifact is wrapped with trivial units
// (§11 naming) — derivation input, never an invocation source. Returns the
// wrapper, the JSON-object content to embed when the source must be inlined
// (nil when the wrapper has its own location), and that location.
func wrapperForSource(ctx context.Context, format, location string, content any) (*Wrapper, map[string]any, string, error) {
	if strings.HasPrefix(format, "openbindings.usage") {
		w, err := loadWrapper(ctx, location, content)
		if err != nil {
			return nil, nil, "", err
		}
		var embed map[string]any
		if content != nil {
			switch c := content.(type) {
			case map[string]any:
				embed = c
			default:
				b, merr := json.Marshal(content)
				if merr != nil {
					return nil, nil, "", fmt.Errorf("openbindings.usage content: %w", merr)
				}
				if err := json.Unmarshal(b, &embed); err != nil {
					return nil, nil, "", fmt.Errorf("openbindings.usage content: %w", err)
				}
			}
			return w, embed, "", nil
		}
		return w, nil, location, nil
	}

	// Bare artifact: wrap it.
	text, err := loadArtifactText(ctx, location, content)
	if err != nil {
		return nil, nil, "", err
	}
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse usage content: %w", err)
	}
	trivial := TrivialWrapper(text, spec)
	w, err := ParseWrapper(trivial)
	if err != nil {
		return nil, nil, "", fmt.Errorf("wrap usage artifact: %w", err)
	}
	return w, trivial, "", nil
}

// synthesizeFromArtifactText wraps bare kdl text with trivial units and
// synthesizes — the bare-artifact path of SynthesizeInterface, exported to
// tests as the old convertToInterfaceWithSpec entry.
func synthesizeFromArtifactText(text string) (openbindings.Interface, error) {
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return openbindings.Interface{}, fmt.Errorf("parse usage content: %w", err)
	}
	trivial := TrivialWrapper(text, spec)
	w, err := ParseWrapper(trivial)
	if err != nil {
		return openbindings.Interface{}, err
	}
	return buildInterfaceFromWrapper(w, trivial, "")
}

// loadArtifactText reads a bare kdl artifact's text from inline content, a
// file path, or an exec: locator.
func loadArtifactText(ctx context.Context, location string, content any) (string, error) {
	if content != nil {
		switch c := content.(type) {
		case string:
			return c, nil
		case []byte:
			return string(c), nil
		default:
			return "", fmt.Errorf("unsupported content type %T (expected string or []byte)", content)
		}
	}
	if location == "" {
		return "", fmt.Errorf("source must have location or content")
	}
	if strings.HasPrefix(location, "exec:") {
		return resolveCommandArtifact(ctx, location)
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return "", fmt.Errorf("read usage spec: %w", err)
	}
	return string(data), nil
}

// buildInterfaceFromWrapper synthesizes an interface from a wrapper's units:
// one operation per unit, bound by unit ref. The emitted source is the
// wrapper itself (its location, or its embedded JSON-object content).
func buildInterfaceFromWrapper(w *Wrapper, wrapperContent map[string]any, location string) (openbindings.Interface, error) {
	spec, err := w.loadArtifact()
	if err != nil {
		return openbindings.Interface{}, err
	}
	meta := spec.Meta()

	sourceEntry := openbindings.Source{Format: WrapperToken}
	if location != "" {
		sourceEntry.Location = location
	} else {
		sourceEntry.Content = wrapperContent
	}

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

	names := make([]string, 0, len(w.Units))
	for name := range w.Units {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		unit := w.Units[name]
		cmd, inherited, path, verr := w.validateUnit(unit, name)
		if verr != nil {
			return openbindings.Interface{}, verr
		}

		op := openbindings.Operation{Description: unit.Description}
		if op.Description == "" {
			if strings.TrimSpace(unit.Command) == "" {
				op.Description = meta.About
			} else {
				op.Description = cmd.Help
			}
		}
		if len(path) > 1 {
			op.Tags = make([]string, len(path)-1)
			copy(op.Tags, path[:len(path)-1])
		}
		for _, alias := range cmd.Aliases {
			for _, an := range alias.Names {
				if !alias.Hide {
					op.Aliases = append(op.Aliases, an)
				}
			}
		}
		inputSchema, serr := generateInputSchema(*cmd, inherited)
		if serr != nil {
			return openbindings.Interface{}, serr
		}
		if inputSchema != nil {
			op.Input = inputSchema
		}

		iface.Operations[name] = op
		iface.Bindings[name+"."+DefaultSourceName] = openbindings.BindingEntry{
			Operation: name,
			Source:    DefaultSourceName,
			Ref:       UnitRef(name),
		}
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
