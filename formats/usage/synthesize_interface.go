package usage

import (
	"encoding/json"
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
		BindingSpec: BindingSpec,
		Content:     openbindings.TextContent(text),
	})
}

// absolutizeArtifactLocation is the authoring-side intake rule shared by
// synthesis and inspection: a relative file path is operator convenience at
// authoring time and is absolutized against the working directory, so the
// strict loader (artifactText — one loader for every lane) accepts it.
// Inline content, exec: locators, and URLs pass through untouched (URLs get
// the loader's clear refusal). Emission is a separate rule: a file path —
// however absolute the filesystem considers it — is a relative reference
// under OBI-D-05 and is never emitted as a document location; see
// emittableAsLocation.
func absolutizeArtifactLocation(location string, content json.RawMessage) (string, error) {
	if content != nil || location == "" ||
		strings.HasPrefix(location, "exec:") || strings.Contains(location, "://") {
		return location, nil
	}
	// A bare filesystem path is authoring-time operator convenience: it
	// becomes the file:// document address the strict loader accepts
	// (USAGE-D-02 — one loader for every lane, no bare-path lane).
	abs := location
	if !filepath.IsAbs(location) {
		var err error
		abs, err = filepath.Abs(location)
		if err != nil {
			return "", fmt.Errorf("resolve usage artifact path: %w", err)
		}
	}
	return "file://" + abs, nil
}

// emittableAsLocation is the authoring-side EMISSION rule: it reports
// whether an intake location may ride a synthesized document's `location`
// verbatim. Exec addresses qualify per USAGE-D-02's own grammar (a
// bindingSpec-defined absolute address — spaces and all; the old
// URI-only restriction predates the promotion). http(s) document
// addresses qualify as absolute URIs. file:// stays on the project's
// embed-default lane: conformant but machine-coupled, so the artifact
// rides as content and the local path stays out of the document.
func emittableAsLocation(location string) bool {
	if strings.HasPrefix(location, "exec:") {
		_, err := ParseExecAddress(location)
		return err == nil
	}
	return strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://")
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// uriRefAllowedChars holds the unreserved + reserved characters allowed in
// a URI reference per RFC 3986 (the same table the core validator screens
// locations with).
var uriRefAllowedChars = func() [256]bool {
	var t [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		t[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		t[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range []byte("-._~:/?#[]@!$&'()*+,;=") {
		t[c] = true
	}
	return t
}()

// floorOutputSchema is the floor-true derived output contract: the text
// assumption always yields a string, stamped with in-schema x-ob
// provenance so the diagnostics key on it and elections self-clear it.
func floorOutputSchema() openbindings.JSONSchema {
	return map[string]any{
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
	if err := validateAcceptedUsageArtifact(spec); err != nil {
		return openbindings.Interface{}, err
	}
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
	if meta.Bin == "" {
		return iface, nil
	}
	usedOperationKeys := map[string]bool{}

	addOp := func(name, help string, path []string, cmd *Command, inherited []Flag) error {
		refs := uniquelyResolvableCommandRefs(spec, path)
		if len(path) == 0 {
			refs = []string{""}
		}
		if len(refs) == 0 {
			return nil
		}
		opKey := openbindings.UniqueKey(openbindings.SanitizeKey(name), usedOperationKeys)
		usedOperationKeys[opKey] = true
		op := openbindings.Operation{Description: help}
		if len(path) > 1 {
			op.Tags = make([]string, len(path)-1)
			copy(op.Tags, path[:len(path)-1])
		}
		if cmd != nil {
			// A usage `alias` is command-scoped CLI shorthand (`ls` under five
			// different groups is idiomatic); an OBI alias is a satisfaction
			// identifier in the document's FLAT identifier namespace
			// (OBI-D-04, OBI-T-12) — a different concept entirely. Mapping
			// them across produced documents that failed the SDK's own
			// validator on alias collisions. CLI aliases stay where they
			// live: in the usage artifact itself.
			inputSchema, serr := generateInputSchema(*cmd, inherited)
			if serr != nil {
				return serr
			}
			if inputSchema != nil {
				op.Input = inputSchema
			}
		}
		op.Output = floorOutputSchema()

		iface.Operations[opKey] = op
		for index, ref := range refs {
			bindingKey := opKey + "." + DefaultSourceName
			if index > 0 {
				bindingKey = fmt.Sprintf("%s.%s.alias%d", opKey, DefaultSourceName, index)
			}
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       ref,
			}
		}
		return nil
	}

	rc := rootCommand(spec)
	if rc == nil {
		rc = &Command{}
	}
	// The descriptor's binary is itself a callable root command even when it
	// declares no top-level fields. An omitted ref selects it under USAGE-D-03.
	// A command-local unresolvable surface is omitted here and receives an
	// explicit exclusion on the coverage surface.
	_ = addOp(meta.Bin, meta.About, nil, rc, nil)
	walkWithGlobals(spec, func(path []string, cmd Command, inherited []Flag) {
		if len(path) == 0 || cmd.SubcommandRequired {
			return
		}
		c := cmd
		_ = addOp(operationName(path), cmd.Help, path, &c, inherited)
	})

	return iface, nil
}

// commandRefAlternatives returns the Cartesian product of every declared
// command spelling along a canonical command path. Aliases remain binding
// alternatives (their exact spelling rides argv); they are never copied into
// Operation.Aliases, whose meaning is cross-interface satisfaction.
func commandRefAlternatives(spec *Spec, path []string) []string {
	if len(path) == 0 {
		return []string{""}
	}
	prefixes := [][]string{{}}
	commands := spec.Commands()
	for _, segment := range path {
		var matched *Command
		for index := range commands {
			if commands[index].Name == segment {
				matched = &commands[index]
				break
			}
		}
		if matched == nil {
			return nil
		}
		spellings := []string{matched.Name}
		for _, alias := range matched.Aliases {
			spellings = append(spellings, alias.Names...)
		}
		var next [][]string
		for _, prefix := range prefixes {
			for _, spelling := range spellings {
				candidate := append(append([]string(nil), prefix...), spelling)
				next = append(next, candidate)
			}
		}
		prefixes = next
		commands = matched.Commands
	}
	refs := make([]string, 0, len(prefixes))
	for _, path := range prefixes {
		refs = append(refs, commandRef(path))
	}
	return refs
}

// uniquelyResolvableCommandRefs keeps only artifact spellings that resolve to
// exactly one command at every segment and arrive at the requested canonical
// command path. A descriptor may declare colliding sibling aliases; declaration
// order is not target identity, so synthesis refuses those spellings.
func uniquelyResolvableCommandRefs(spec *Spec, path []string) []string {
	var refs []string
	for _, ref := range commandRefAlternatives(spec, path) {
		resolved, err := findCommand(spec, ref)
		if err == nil && strings.Join(resolved.canonicalPath, "\x00") == strings.Join(path, "\x00") {
			refs = append(refs, ref)
		}
	}
	return refs
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

// rootCommand returns the root invocation's declared field surface. Global
// top-level flags apply to the root as well as descendants.
func rootCommand(spec *Spec) *Command {
	topFlags := spec.Flags()
	topArgs := spec.Args()

	if len(topFlags) == 0 && len(topArgs) == 0 {
		return nil
	}

	// Include all top-level flags (global + non-global) since the root command uses them all.
	return &Command{
		Flags: topFlags,
		Args:  topArgs,
	}
}

func generateInputSchema(cmd Command, inheritedGlobals []Flag) (map[string]any, error) {
	if err := validateUsageRelations(&cmd, inheritedGlobals); err != nil {
		return nil, err
	}
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
		if flag.Var && flag.valueVariadic() {
			return nil, fmt.Errorf("flag %q is both repeatable and variadic; revision 1 refuses the ambiguous multiplicity", name)
		}

		prop := generateFlagSchema(flag)
		if prop != nil {
			properties[name] = prop
		}

		if flag.Required && flag.effectiveDefault() == nil && flag.effectiveEnv() == "" {
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

		if arg.IsRequired() && arg.Default == nil && arg.Env == "" {
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
		schema["required"] = stringValues(required)
	}

	return schema, nil
}

func validateUsageRelations(cmd *Command, inherited []Flag) error {
	flags := cmd.AllFlags(inherited)
	for _, flag := range flags {
		for _, declaration := range []struct {
			name  string
			value string
		}{{"required_if", flag.RequiredIf}, {"required_unless", flag.RequiredUnless}, {"overrides", flag.Overrides}} {
			for _, reference := range splitUsageReferences(declaration.value) {
				if _, ok := resolveUsageFlag(flags, reference); !ok {
					return fmt.Errorf("flag %q %s names unknown flag %q", flag.PrimaryName(), declaration.name, reference)
				}
			}
		}
	}
	return nil
}

func generateFlagSchema(flag Flag) map[string]any {
	prop := make(map[string]any)

	parsed := flag.ParseUsage()

	if flag.Count {
		prop["type"] = schemaTypeInteger
		prop["minimum"] = 0
		if flag.Help != "" {
			prop["description"] = flag.Help
		}
		if value := flag.effectiveDefault(); value != nil {
			prop["default"] = value
		}
		return prop
	}

	takesValue := parsed.ArgName != "" || len(flag.Args) > 0

	if !takesValue {
		prop["type"] = schemaTypeBoolean
		if flag.Help != "" {
			prop["description"] = flag.Help
		}
		if value := flag.effectiveDefault(); value != nil {
			prop["default"] = value
		}
		return prop
	}

	choices := flag.effectiveChoices()
	if flag.Var || flag.valueVariadic() {
		itemSchema := map[string]any{"type": schemaTypeString}
		if len(choices) > 0 && flag.choicesEnvironment() == "" {
			itemSchema["enum"] = stringValues(choices)
		}
		prop["type"] = schemaTypeArray
		prop["items"] = itemSchema
		if min := flag.effectiveVarMin(); min != nil {
			prop["minItems"] = *min
		}
		if max := flag.effectiveVarMax(); max != nil {
			prop["maxItems"] = *max
		}
	} else {
		prop["type"] = schemaTypeString
		if len(choices) > 0 && flag.choicesEnvironment() == "" {
			prop["enum"] = stringValues(choices)
		}
	}

	if flag.Help != "" {
		prop["description"] = flag.Help
	}
	if value := flag.effectiveDefault(); value != nil {
		prop["default"] = value
	}

	return prop
}

func generateArgSchema(arg Arg) map[string]any {
	prop := make(map[string]any)

	if arg.IsVariadic() {
		itemSchema := map[string]any{"type": schemaTypeString}
		if len(arg.Choices) > 0 && arg.choicesEnvironment() == "" {
			itemSchema["enum"] = stringValues(arg.Choices)
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
		if len(arg.Choices) > 0 && arg.choicesEnvironment() == "" {
			prop["enum"] = stringValues(arg.Choices)
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

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
