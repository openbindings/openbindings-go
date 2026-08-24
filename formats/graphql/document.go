package graphql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type gqlToken struct {
	kind  byte
	value string
}

type gqlDirective struct {
	name    string
	ifValue *gqlValue
}

type gqlValue struct {
	literal  *bool
	variable string
}

type gqlSelection struct {
	fieldName     string
	responseKey   string
	fragmentName  string
	typeCondition string
	directives    []gqlDirective
	selections    []gqlSelection
	inline        bool
}

type gqlOperation struct {
	kind       string
	name       string
	selections []gqlSelection
}

type gqlFragment struct {
	typeCondition string
	directives    []gqlDirective
	selections    []gqlSelection
}

type executableDocument struct {
	operations []gqlOperation
	fragments  map[string]gqlFragment
}

type gqlParser struct {
	tokens []gqlToken
	at     int
}

func parseExecutableDocument(source string) (*executableDocument, error) {
	tokens, err := lexGraphQL(source)
	if err != nil {
		return nil, err
	}
	p := &gqlParser{tokens: tokens}
	doc := &executableDocument{fragments: map[string]gqlFragment{}}
	for !p.eof() {
		if p.peekValue("{") {
			selections, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, gqlOperation{kind: "query", selections: selections})
			continue
		}
		keyword, err := p.takeName()
		if err != nil {
			return nil, err
		}
		if keyword == "fragment" {
			name, err := p.takeName()
			if err != nil {
				return nil, err
			}
			if name == "on" {
				return nil, fmt.Errorf("fragment name cannot be on")
			}
			if _, duplicate := doc.fragments[name]; duplicate {
				return nil, fmt.Errorf("duplicate fragment %q", name)
			}
			if err := p.expectName("on"); err != nil {
				return nil, err
			}
			condition, err := p.takeName()
			if err != nil {
				return nil, err
			}
			directives, err := p.parseDirectives()
			if err != nil {
				return nil, err
			}
			selections, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			doc.fragments[name] = gqlFragment{typeCondition: condition, directives: directives, selections: selections}
			continue
		}
		if keyword != "query" && keyword != "mutation" && keyword != "subscription" {
			return nil, fmt.Errorf("unexpected definition %q", keyword)
		}
		op := gqlOperation{kind: keyword}
		if p.peekKind('n') {
			op.name, _ = p.takeName()
		}
		if p.peekValue("(") {
			if err := p.parseVariableDefinitions(); err != nil {
				return nil, err
			}
		}
		if _, err := p.parseDirectives(); err != nil {
			return nil, err
		}
		op.selections, err = p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		doc.operations = append(doc.operations, op)
	}
	if len(doc.operations) == 0 {
		return nil, fmt.Errorf("document contains no executable operation")
	}
	return doc, nil
}

func (d *executableDocument) verifySelection(operationName, wantKind, wantField string, variables map[string]any, schema *introspectionSchema) error {
	_, err := d.responseKey(operationName, wantKind, wantField, variables, schema)
	return err
}

func (d *executableDocument) responseKey(operationName, wantKind, wantField string, variables map[string]any, schema *introspectionSchema) (string, error) {
	var selected *gqlOperation
	if operationName != "" {
		for i := range d.operations {
			if d.operations[i].name == operationName {
				if selected != nil {
					return "", fmt.Errorf("operationName %q is ambiguous", operationName)
				}
				selected = &d.operations[i]
			}
		}
		if selected == nil {
			return "", fmt.Errorf("operationName %q selects no operation", operationName)
		}
	} else {
		if len(d.operations) != 1 {
			return "", fmt.Errorf("operationName is required when a document contains multiple operations")
		}
		selected = &d.operations[0]
	}
	if selected.kind != wantKind {
		return "", fmt.Errorf("selected operation kind %q does not match binding selector kind %q", selected.kind, wantKind)
	}

	rootName := schema.rootTypeName(wantKind)
	if rootName == "" {
		return "", fmt.Errorf("schema has no %s root type", wantKind)
	}
	groups := map[string][]string{}
	visiting := map[string]bool{}
	if err := d.collect(selected.selections, nil, rootName, variables, schema, groups, visiting); err != nil {
		return "", err
	}
	if len(groups) != 1 {
		return "", fmt.Errorf("selected operation must collect exactly one root response-key group, got %d", len(groups))
	}
	for responseKey, fields := range groups {
		for _, field := range fields {
			if field != wantField {
				return "", fmt.Errorf("selected root field %q does not match binding selector field %q", field, wantField)
			}
		}
		return responseKey, nil
	}
	return "", fmt.Errorf("selected operation has no root response key")
}

func (d *executableDocument) collect(selections []gqlSelection, inherited []gqlDirective, rootName string, variables map[string]any, schema *introspectionSchema, groups map[string][]string, visiting map[string]bool) error {
	for _, selection := range selections {
		directives := append(append([]gqlDirective(nil), inherited...), selection.directives...)
		if !includeSelection(directives, variables) {
			continue
		}
		switch {
		case selection.fieldName != "":
			groups[selection.responseKey] = append(groups[selection.responseKey], selection.fieldName)
		case selection.fragmentName != "":
			fragment, ok := d.fragments[selection.fragmentName]
			if !ok {
				return fmt.Errorf("fragment %q is not defined", selection.fragmentName)
			}
			if visiting[selection.fragmentName] {
				return fmt.Errorf("fragment cycle includes %q", selection.fragmentName)
			}
			applies, err := typeConditionApplies(fragment.typeCondition, rootName, schema)
			if err != nil {
				return err
			}
			if !applies {
				continue
			}
			visiting[selection.fragmentName] = true
			err = d.collect(fragment.selections, fragment.directives, rootName, variables, schema, groups, visiting)
			delete(visiting, selection.fragmentName)
			if err != nil {
				return err
			}
		case selection.inline:
			if selection.typeCondition != "" {
				applies, err := typeConditionApplies(selection.typeCondition, rootName, schema)
				if err != nil {
					return err
				}
				if !applies {
					continue
				}
			}
			if err := d.collect(selection.selections, nil, rootName, variables, schema, groups, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

func includeSelection(directives []gqlDirective, variables map[string]any) bool {
	for _, directive := range directives {
		if directive.ifValue == nil || (directive.name != "skip" && directive.name != "include") {
			continue
		}
		value, known := directive.ifValue.boolean(variables)
		if !known {
			continue
		}
		if directive.name == "skip" && value {
			return false
		}
		if directive.name == "include" && !value {
			return false
		}
	}
	return true
}

func (v gqlValue) boolean(variables map[string]any) (bool, bool) {
	if v.literal != nil {
		return *v.literal, true
	}
	if v.variable != "" {
		value, ok := variables[v.variable].(bool)
		return value, ok
	}
	return false, false
}

func typeConditionApplies(condition, rootName string, schema *introspectionSchema) (bool, error) {
	if condition == rootName {
		return true, nil
	}
	t, ok := schema.typeMap()[condition]
	if !ok {
		return false, fmt.Errorf("fragment type condition %q cannot be resolved from the schema", condition)
	}
	if t.Kind == "OBJECT" {
		return false, nil
	}
	if t.Kind != "INTERFACE" && t.Kind != "UNION" {
		return false, fmt.Errorf("fragment type condition %q is not a composite type", condition)
	}
	for _, possible := range t.PossibleTypes {
		if possible.Name == rootName {
			return true, nil
		}
	}
	if t.Kind == "INTERFACE" {
		root, ok := schema.typeMap()[rootName]
		if !ok {
			return false, fmt.Errorf("root type %q cannot be resolved from the schema", rootName)
		}
		for _, iface := range root.Interfaces {
			if iface.Name == condition {
				return true, nil
			}
		}
	}
	return false, nil
}

func (p *gqlParser) parseSelectionSet() ([]gqlSelection, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	var selections []gqlSelection
	for !p.peekValue("}") {
		if p.eof() {
			return nil, fmt.Errorf("unterminated selection set")
		}
		selection, err := p.parseSelection()
		if err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	p.at++
	if len(selections) == 0 {
		return nil, fmt.Errorf("selection set cannot be empty")
	}
	return selections, nil
}

func (p *gqlParser) parseSelection() (gqlSelection, error) {
	if p.peekValue("...") {
		p.at++
		if p.peekName("on") {
			p.at++
			condition, err := p.takeName()
			if err != nil {
				return gqlSelection{}, err
			}
			directives, err := p.parseDirectives()
			if err != nil {
				return gqlSelection{}, err
			}
			selections, err := p.parseSelectionSet()
			return gqlSelection{inline: true, typeCondition: condition, directives: directives, selections: selections}, err
		}
		if p.peekValue("@") {
			directives, err := p.parseDirectives()
			if err != nil {
				return gqlSelection{}, err
			}
			selections, err := p.parseSelectionSet()
			return gqlSelection{inline: true, directives: directives, selections: selections}, err
		}
		name, err := p.takeName()
		if err != nil {
			return gqlSelection{}, err
		}
		directives, err := p.parseDirectives()
		return gqlSelection{fragmentName: name, directives: directives}, err
	}

	first, err := p.takeName()
	if err != nil {
		return gqlSelection{}, err
	}
	fieldName, responseKey := first, first
	if p.peekValue(":") {
		p.at++
		fieldName, err = p.takeName()
		if err != nil {
			return gqlSelection{}, err
		}
		responseKey = first
	}
	if p.peekValue("(") {
		if _, err := p.parseArguments(true); err != nil {
			return gqlSelection{}, err
		}
	}
	directives, err := p.parseDirectives()
	if err != nil {
		return gqlSelection{}, err
	}
	selection := gqlSelection{fieldName: fieldName, responseKey: responseKey, directives: directives}
	if p.peekValue("{") {
		selection.selections, err = p.parseSelectionSet()
	}
	return selection, err
}

func (p *gqlParser) parseDirectives() ([]gqlDirective, error) {
	var directives []gqlDirective
	for p.peekValue("@") {
		p.at++
		name, err := p.takeName()
		if err != nil {
			return nil, err
		}
		directive := gqlDirective{name: name}
		if p.peekValue("(") {
			arguments, err := p.parseArguments(true)
			if err != nil {
				return nil, err
			}
			if value, ok := arguments["if"]; ok {
				directive.ifValue = value
			}
		}
		directives = append(directives, directive)
	}
	return directives, nil
}

func (p *gqlParser) parseVariableDefinitions() error {
	if err := p.expect("("); err != nil {
		return err
	}
	if p.peekValue(")") {
		return fmt.Errorf("variable definitions cannot be empty")
	}
	for !p.peekValue(")") {
		if p.eof() {
			return fmt.Errorf("unterminated variable definitions")
		}
		if err := p.expect("$"); err != nil {
			return err
		}
		if _, err := p.takeName(); err != nil {
			return err
		}
		if err := p.expect(":"); err != nil {
			return err
		}
		if err := p.parseTypeReference(); err != nil {
			return err
		}
		if p.peekValue("=") {
			p.at++
			if _, err := p.parseValue(false); err != nil {
				return fmt.Errorf("invalid variable default: %w", err)
			}
		}
		if _, err := p.parseDirectives(); err != nil {
			return err
		}
	}
	p.at++
	return nil
}

func (p *gqlParser) parseTypeReference() error {
	if p.peekValue("[") {
		p.at++
		if err := p.parseTypeReference(); err != nil {
			return err
		}
		if err := p.expect("]"); err != nil {
			return err
		}
	} else if _, err := p.takeName(); err != nil {
		return err
	}
	if p.peekValue("!") {
		p.at++
	}
	return nil
}

// parseArguments parses the GraphQL Arguments grammar. Its returned values
// retain only boolean literals and variables because those are the values
// whose skip/include semantics affect root-field correspondence.
func (p *gqlParser) parseArguments(allowVariables bool) (map[string]*gqlValue, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	if p.peekValue(")") {
		return nil, fmt.Errorf("argument list cannot be empty")
	}
	out := map[string]*gqlValue{}
	for !p.peekValue(")") {
		if p.eof() {
			return nil, fmt.Errorf("unterminated argument list")
		}
		name, err := p.takeName()
		if err != nil {
			return nil, err
		}
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		value, err := p.parseValue(allowVariables)
		if err != nil {
			return nil, fmt.Errorf("invalid argument %q: %w", name, err)
		}
		out[name] = value
	}
	p.at++
	return out, nil
}

func (p *gqlParser) parseValue(allowVariables bool) (*gqlValue, error) {
	switch {
	case p.peekValue("$"):
		if !allowVariables {
			return nil, fmt.Errorf("variables are not allowed in constant values")
		}
		p.at++
		name, err := p.takeName()
		if err != nil {
			return nil, err
		}
		return &gqlValue{variable: name}, nil
	case p.peekValue("["):
		p.at++
		for !p.peekValue("]") {
			if p.eof() {
				return nil, fmt.Errorf("unterminated list value")
			}
			if _, err := p.parseValue(allowVariables); err != nil {
				return nil, err
			}
		}
		p.at++
		return &gqlValue{}, nil
	case p.peekValue("{"):
		p.at++
		for !p.peekValue("}") {
			if p.eof() {
				return nil, fmt.Errorf("unterminated object value")
			}
			if _, err := p.takeName(); err != nil {
				return nil, err
			}
			if err := p.expect(":"); err != nil {
				return nil, err
			}
			if _, err := p.parseValue(allowVariables); err != nil {
				return nil, err
			}
		}
		p.at++
		return &gqlValue{}, nil
	case p.peekKind('v'):
		p.at++
		return &gqlValue{}, nil
	case p.peekKind('n'):
		name, _ := p.takeName()
		switch name {
		case "true":
			value := true
			return &gqlValue{literal: &value}, nil
		case "false":
			value := false
			return &gqlValue{literal: &value}, nil
		default:
			return &gqlValue{}, nil // null or an enum value
		}
	default:
		if p.eof() {
			return nil, fmt.Errorf("expected value at end of document")
		}
		return nil, fmt.Errorf("expected value, got %q", p.tokens[p.at].value)
	}
}

func (p *gqlParser) eof() bool { return p.at >= len(p.tokens) }
func (p *gqlParser) peekValue(value string) bool {
	return !p.eof() && p.tokens[p.at].value == value
}
func (p *gqlParser) peekKind(kind byte) bool {
	return !p.eof() && p.tokens[p.at].kind == kind
}
func (p *gqlParser) peekName(value string) bool {
	return p.peekKind('n') && p.tokens[p.at].value == value
}
func (p *gqlParser) takeName() (string, error) {
	if !p.peekKind('n') {
		if p.eof() {
			return "", fmt.Errorf("expected GraphQL Name at end of document")
		}
		return "", fmt.Errorf("expected GraphQL Name, got %q", p.tokens[p.at].value)
	}
	value := p.tokens[p.at].value
	p.at++
	return value, nil
}
func (p *gqlParser) expect(value string) error {
	if !p.peekValue(value) {
		return fmt.Errorf("expected %q", value)
	}
	p.at++
	return nil
}
func (p *gqlParser) expectName(value string) error {
	if !p.peekName(value) {
		return fmt.Errorf("expected %q", value)
	}
	p.at++
	return nil
}

var graphqlNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func lexGraphQL(source string) ([]gqlToken, error) {
	var tokens []gqlToken
	for at := 0; at < len(source); {
		r, size := utf8.DecodeRuneInString(source[at:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("document is not valid UTF-8")
		}
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == ',' || r == '\uFEFF' {
			at += size
			continue
		}
		if r == '#' {
			for at < len(source) && source[at] != '\n' && source[at] != '\r' {
				_, n := utf8.DecodeRuneInString(source[at:])
				at += n
			}
			continue
		}
		if strings.HasPrefix(source[at:], "...") {
			tokens = append(tokens, gqlToken{kind: 'p', value: "..."})
			at += 3
			continue
		}
		if r == '"' {
			start := at
			if strings.HasPrefix(source[at:], `"""`) {
				at += 3
				for {
					offset := strings.Index(source[at:], `"""`)
					if offset < 0 {
						return nil, fmt.Errorf("unterminated block string")
					}
					end := at + offset
					if end > start+3 && source[end-1] == '\\' {
						// GraphQL block strings spell an embedded triple quote
						// as \"""; that sequence is content, not the terminator.
						at = end + 3
						continue
					}
					at = end + 3
					break
				}
			} else {
				at += size
				escaped := false
				for at < len(source) {
					ch := source[at]
					if ch < 0x20 && ch != '\t' {
						return nil, fmt.Errorf("unescaped control character in string")
					}
					at++
					if escaped {
						if !strings.ContainsRune(`"\/bfnrtu`, rune(ch)) {
							return nil, fmt.Errorf("invalid string escape \\%c", ch)
						}
						if ch == 'u' {
							if at < len(source) && source[at] == '{' {
								at++
								digitStart := at
								for at < len(source) && isHexDigit(source[at]) && at-digitStart < 6 {
									at++
								}
								if at == digitStart || at >= len(source) || source[at] != '}' {
									return nil, fmt.Errorf("invalid Unicode scalar escape")
								}
								scalar, err := strconv.ParseUint(source[digitStart:at], 16, 32)
								if err != nil || scalar > utf8.MaxRune || scalar >= 0xD800 && scalar <= 0xDFFF {
									return nil, fmt.Errorf("invalid Unicode scalar escape")
								}
								at++
							} else {
								if at+4 > len(source) {
									return nil, fmt.Errorf("incomplete Unicode escape")
								}
								for _, digit := range source[at : at+4] {
									if !isHexDigit(byte(digit)) {
										return nil, fmt.Errorf("invalid Unicode escape")
									}
								}
								at += 4
							}
						}
						escaped = false
					} else if ch == '\\' {
						escaped = true
					} else if ch == '"' {
						break
					}
				}
				if at > len(source) || source[at-1] != '"' {
					return nil, fmt.Errorf("unterminated string")
				}
			}
			tokens = append(tokens, gqlToken{kind: 'v', value: source[start:at]})
			continue
		}
		if isNameStart(source[at]) {
			start := at
			at++
			for at < len(source) && isNameContinue(source[at]) {
				at++
			}
			tokens = append(tokens, gqlToken{kind: 'n', value: source[start:at]})
			continue
		}
		if strings.ContainsRune("!$():=@[]{|}&", r) {
			tokens = append(tokens, gqlToken{kind: 'p', value: string(r)})
			at += size
			continue
		}
		if r == '-' || (r >= '0' && r <= '9') {
			start := at
			at += size
			for at < len(source) {
				next := source[at]
				if strings.ContainsRune(" \t\r\n,()[]{}!$:@=|&", rune(next)) {
					break
				}
				at++
			}
			number := source[start:at]
			if !graphqlNumber.MatchString(number) {
				return nil, fmt.Errorf("invalid number %q", number)
			}
			tokens = append(tokens, gqlToken{kind: 'v', value: number})
			continue
		}
		return nil, fmt.Errorf("unexpected character %q", r)
	}
	return tokens, nil
}

func isNameStart(b byte) bool {
	return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isNameContinue(b byte) bool {
	return isNameStart(b) || b >= '0' && b <= '9'
}

func isHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'A' && b <= 'F' || b >= 'a' && b <= 'f'
}
