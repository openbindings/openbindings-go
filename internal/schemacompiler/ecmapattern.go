package schemacompiler

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// maxPatternNesting bounds how deeply a pattern nests groups. Checking a
// pattern recurses once per level, and patterns never nest near this deep, so
// a deeper one is core's own limit met (Reports and Verdicts, in the core
// package documentation), not evidence about the pattern.
const maxPatternNesting = 256

// ErrPatternNesting is the error for a pattern nesting groups deeper than
// maxPatternNesting.
var ErrPatternNesting = fmt.Errorf("a pattern nesting groups deeper than %d levels, a limit of this SDK", maxPatternNesting)

// CheckPattern returns nil for a Pattern of ECMA-262 (11th edition,
// §21.2.1) parsed with the u flag, early errors included, the grammar a JSON
// Schema pattern is read under (JSON Schema Core §6.4), with Unicode property
// escapes checked against that edition's names for Unicode 13.0. It returns
// ErrPatternNesting for a pattern nesting groups deeper than core's limit,
// and another error for a pattern that is not one. It takes time in
// proportion to the pattern's length. Many engines accept patterns the
// grammar refuses, such as a class escape bounding a range ([\w-.]) or an
// identity escape of a character that is not syntax (\-).
func CheckPattern(pattern string) error {
	p := &patternParser{src: []rune(pattern), names: map[string]bool{}}
	p.countGroups()
	if err := p.disjunction(); err != nil {
		return err
	}
	if p.i < len(p.src) {
		return p.errorf("unmatched )")
	}
	for _, n := range p.backreferences {
		if compareDecimal(n, strconv.Itoa(p.groups)) > 0 {
			return fmt.Errorf("the backreference \\%s names no capturing group", n)
		}
	}
	for _, name := range p.namedReferences {
		if !p.names[name] {
			return fmt.Errorf("the backreference \\k<%s> names no group", name)
		}
	}
	return nil
}

type patternParser struct {
	src    []rune
	i      int
	groups int
	// names holds every group name; declared the names met so far.
	names, declared map[string]bool
	// References are checked once every group is known.
	backreferences  []string
	namedReferences []string
	// depth is how many groups the parser is inside.
	depth int
}

func (p *patternParser) errorf(format string, args ...any) error {
	return fmt.Errorf("at %d: %s", p.i, fmt.Sprintf(format, args...))
}

func (p *patternParser) peek(offset int) rune {
	if p.i+offset < len(p.src) {
		return p.src[p.i+offset]
	}
	return -1
}

func (p *patternParser) lookingAt(s string) bool {
	for k, r := range []rune(s) {
		if p.peek(k) != r {
			return false
		}
	}
	return true
}

// countGroups counts the capturing groups and collects the group names, as
// the grammar's early errors for references need (§21.2.1.1).
func (p *patternParser) countGroups() {
	inClass := false
	for k := 0; k < len(p.src); k++ {
		switch r := p.src[k]; {
		case r == '\\':
			k++
		case inClass:
			inClass = r != ']'
		case r == '[':
			inClass = true
		case r == '(' && (k+1 >= len(p.src) || p.src[k+1] != '?'):
			p.groups++
		case r == '(' && k+2 < len(p.src) && p.src[k+1] == '?' && p.src[k+2] == '<' && (k+3 >= len(p.src) || p.src[k+3] != '=' && p.src[k+3] != '!'):
			p.groups++
			names := &patternParser{src: p.src, i: k + 3}
			if name, err := names.groupName(); err == nil {
				p.names[name] = true
			}
		}
	}
	p.declared = map[string]bool{}
}

func (p *patternParser) disjunction() error {
	if err := p.alternative(); err != nil {
		return err
	}
	for p.peek(0) == '|' {
		p.i++
		if err := p.alternative(); err != nil {
			return err
		}
	}
	return nil
}

func (p *patternParser) alternative() error {
	for p.i < len(p.src) && p.peek(0) != '|' && p.peek(0) != ')' {
		if err := p.term(); err != nil {
			return err
		}
	}
	return nil
}

// term reads an assertion, or an atom and any quantifier. In u mode no
// assertion takes a quantifier, lookaheads included.
func (p *patternParser) term() error {
	quantifiable := true
	switch r := p.peek(0); {
	case r == '^' || r == '$':
		p.i++
		quantifiable = false
	case r == '\\' && (p.peek(1) == 'b' || p.peek(1) == 'B'):
		p.i += 2
		quantifiable = false
	case r == '(':
		lookaround := p.lookingAt("(?=") || p.lookingAt("(?!") || p.lookingAt("(?<=") || p.lookingAt("(?<!")
		if err := p.group(); err != nil {
			return err
		}
		quantifiable = !lookaround
	case r == '[':
		if err := p.class(); err != nil {
			return err
		}
	case r == '.':
		p.i++
	case r == '\\':
		p.i++
		if err := p.atomEscape(); err != nil {
			return err
		}
	case r == '*' || r == '+' || r == '?' || r == '{':
		return p.errorf("nothing to repeat")
	case r == ']' || r == '}':
		return p.errorf("a lone %q", r)
	default:
		p.i++
	}
	if isQuantifierStart(p.peek(0)) {
		if !quantifiable {
			return p.errorf("nothing to repeat")
		}
		return p.quantifier()
	}
	return nil
}

func isQuantifierStart(r rune) bool { return r == '*' || r == '+' || r == '?' || r == '{' }

// quantifier reads * + ? {n} {n,} or {n,m}, then an optional ?. In u mode a
// { that begins no quantifier is an error.
func (p *patternParser) quantifier() error {
	if p.peek(0) == '{' {
		p.i++
		low, ok := p.digits()
		if !ok {
			return p.errorf("incomplete quantifier")
		}
		high := low
		if p.peek(0) == ',' {
			p.i++
			high = ""
			if p.peek(0) != '}' {
				if high, ok = p.digits(); !ok {
					return p.errorf("incomplete quantifier")
				}
			}
		}
		if p.peek(0) != '}' {
			return p.errorf("incomplete quantifier")
		}
		p.i++
		if high != "" && compareDecimal(low, high) > 0 {
			return p.errorf("numbers out of order in {} quantifier")
		}
	} else {
		p.i++
	}
	if p.peek(0) == '?' {
		p.i++
	}
	return nil
}

// digits reads a decimal number, returning its digits without leading zeros
// ("0" for zero). Numbers are compared as digit strings (compareDecimal), so
// one of any length costs time in proportion to it.
func (p *patternParser) digits() (string, bool) {
	start := p.i
	for '0' <= p.peek(0) && p.peek(0) <= '9' {
		p.i++
	}
	if p.i == start {
		return "", false
	}
	if n := strings.TrimLeft(string(p.src[start:p.i]), "0"); n != "" {
		return n, true
	}
	return "0", true
}

// compareDecimal compares two decimal numbers written without leading zeros,
// returning -1, 0, or +1.
func compareDecimal(a, b string) int {
	if len(a) != len(b) {
		return cmp.Compare(len(a), len(b))
	}
	return strings.Compare(a, b)
}

func (p *patternParser) group() error {
	if p.depth++; p.depth > maxPatternNesting {
		return ErrPatternNesting
	}
	defer func() { p.depth-- }()
	switch {
	case p.lookingAt("(?=") || p.lookingAt("(?!") || p.lookingAt("(?:"):
		p.i += 3
	case p.lookingAt("(?<=") || p.lookingAt("(?<!"):
		p.i += 4
	case p.lookingAt("(?<"):
		p.i += 3
		name, err := p.groupName()
		if err != nil {
			return err
		}
		if p.declared[name] {
			return p.errorf("duplicate group name %q", name)
		}
		p.declared[name] = true
	case p.lookingAt("(?"):
		return p.errorf("invalid group")
	default:
		p.i++
	}
	if err := p.disjunction(); err != nil {
		return err
	}
	if p.peek(0) != ')' {
		return p.errorf("unterminated group")
	}
	p.i++
	return nil
}

// groupName reads a RegExpIdentifierName and the > after it.
func (p *patternParser) groupName() (string, error) {
	var name []rune
	for {
		r := p.peek(0)
		if r == '>' {
			p.i++
			break
		}
		if r == -1 {
			return "", p.errorf("unterminated group name")
		}
		if r == '\\' {
			p.i++
			if p.peek(0) != 'u' {
				return "", p.errorf("invalid escape in a group name")
			}
			p.i++
			value, err := p.unicodeEscapeValue()
			if err != nil {
				return "", err
			}
			r = value
		} else {
			p.i++
		}
		if len(name) == 0 && !isIDStart(r) || len(name) > 0 && !isIDContinue(r) {
			return "", p.errorf("invalid group name")
		}
		name = append(name, r)
	}
	if len(name) == 0 {
		return "", p.errorf("empty group name")
	}
	return string(name), nil
}

func isIDStart(r rune) bool {
	return r == '$' || r == '_' || unicode.In(r, unicode.L, unicode.Nl, unicode.Other_ID_Start) && !unicode.In(r, unicode.Pattern_Syntax, unicode.Pattern_White_Space)
}

func isIDContinue(r rune) bool {
	return isIDStart(r) || r == '‌' || r == '‍' || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc, unicode.Other_ID_Continue) && !unicode.In(r, unicode.Pattern_Syntax, unicode.Pattern_White_Space)
}

// atomEscape reads what follows a \ outside a class (AtomEscape[U, N]).
func (p *patternParser) atomEscape() error {
	r := p.peek(0)
	switch {
	case '1' <= r && r <= '9':
		n, _ := p.digits()
		p.backreferences = append(p.backreferences, n)
		return nil
	case r == 'k':
		p.i++
		if p.peek(0) != '<' {
			return p.errorf("invalid named reference")
		}
		p.i++
		name, err := p.groupName()
		if err != nil {
			return err
		}
		p.namedReferences = append(p.namedReferences, name)
		return nil
	}
	_, err := p.characterOrClassEscape(false)
	return err
}

// characterOrClassEscape reads a CharacterClassEscape or CharacterEscape
// after a \, and within a class also \b and \-. It returns whether the escape
// is a class escape, which can bound no range.
func (p *patternParser) characterOrClassEscape(inClass bool) (classEscape bool, err error) {
	r := p.peek(0)
	if r == -1 {
		return false, p.errorf("\\ at end of pattern")
	}
	p.i++
	switch {
	case r == 'd' || r == 'D' || r == 's' || r == 'S' || r == 'w' || r == 'W':
		return true, nil
	case r == 'p' || r == 'P':
		return true, p.propertyEscape()
	case r == 'f' || r == 'n' || r == 'r' || r == 't' || r == 'v':
		return false, nil
	case r == 'c':
		if c := p.peek(0); 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' {
			p.i++
			return false, nil
		}
		return false, p.errorf("invalid control escape")
	case r == '0':
		if c := p.peek(0); '0' <= c && c <= '9' {
			return false, p.errorf("invalid decimal escape")
		}
		return false, nil
	case r == 'x':
		if isHexDigit(p.peek(0)) && isHexDigit(p.peek(1)) {
			p.i += 2
			return false, nil
		}
		return false, p.errorf("invalid hexadecimal escape")
	case r == 'u':
		_, err := p.unicodeEscapeValue()
		return false, err
	case inClass && (r == 'b' || r == '-'):
		return false, nil
	case isSyntaxCharacter(r) || r == '/':
		return false, nil
	}
	return false, p.errorf("invalid escape \\%c", r)
}

// unicodeEscapeValue reads what follows \u in u mode: {CodePoint}, or four
// hexadecimal digits, a lead surrogate joined with an escaped trail surrogate
// after it.
func (p *patternParser) unicodeEscapeValue() (rune, error) {
	if p.peek(0) == '{' {
		p.i++
		start := p.i
		for isHexDigit(p.peek(0)) {
			p.i++
		}
		if p.i == start || p.peek(0) != '}' {
			return 0, p.errorf("invalid Unicode escape")
		}
		digits := strings.TrimLeft(string(p.src[start:p.i]), "0")
		p.i++
		value, err := strconv.ParseUint("0"+digits, 16, 32)
		if len(digits) > 6 || err != nil || value > 0x10FFFF {
			return 0, p.errorf("Unicode escape beyond U+10FFFF")
		}
		return rune(value), nil
	}
	unit, ok := p.hex4(0)
	if !ok {
		return 0, p.errorf("invalid Unicode escape")
	}
	p.i += 4
	if 0xD800 <= unit && unit <= 0xDBFF && p.peek(0) == '\\' && p.peek(1) == 'u' {
		if trail, ok := p.hex4(2); ok && 0xDC00 <= trail && trail <= 0xDFFF {
			p.i += 6
			return (unit-0xD800)<<10 + (trail - 0xDC00) + 0x10000, nil
		}
	}
	return unit, nil
}

func (p *patternParser) hex4(offset int) (rune, bool) {
	var value rune
	for k := 0; k < 4; k++ {
		c := p.peek(offset + k)
		if !isHexDigit(c) {
			return 0, false
		}
		value = value<<4 | hexValue(c)
	}
	return value, true
}

// class reads a CharacterClass. A class escape bounds no range, and a range's
// bounds are in order.
func (p *patternParser) class() error {
	p.i++
	if p.peek(0) == '^' {
		p.i++
	}
	for {
		if p.peek(0) == -1 {
			return p.errorf("unterminated character class")
		}
		if p.peek(0) == ']' {
			p.i++
			return nil
		}
		low, lowClass, err := p.classAtom()
		if err != nil {
			return err
		}
		if p.peek(0) == '-' && p.peek(1) != ']' && p.peek(1) != -1 {
			p.i++
			high, highClass, err := p.classAtom()
			if err != nil {
				return err
			}
			if lowClass || highClass {
				return p.errorf("a class escape bounds a range")
			}
			if low > high {
				return p.errorf("range out of order in character class")
			}
		}
	}
}

// classAtom reads a ClassAtom, returning the character it denotes, or
// whether it is a class escape.
func (p *patternParser) classAtom() (rune, bool, error) {
	r := p.peek(0)
	if r != '\\' {
		p.i++
		return r, false, nil
	}
	p.i++
	start := p.i
	switch c := p.peek(0); {
	case c == 'b':
		p.i++
		return '\b', false, nil
	case c == '-':
		p.i++
		return '-', false, nil
	case '1' <= c && c <= '9', c == 'k':
		return 0, false, p.errorf("invalid escape in a character class")
	}
	classEscape, err := p.characterOrClassEscape(true)
	if err != nil || classEscape {
		return 0, classEscape, err
	}
	return escapedCharacter(p.src[start:p.i]), false, nil
}

// escapedCharacter returns the character a CharacterEscape (without its \)
// denotes.
func escapedCharacter(escape []rune) rune {
	switch escape[0] {
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'v':
		return '\v'
	case '0':
		return 0
	case 'c':
		return escape[1] % 32
	case 'x':
		return hexValue(escape[1])<<4 | hexValue(escape[2])
	case 'u':
		p := &patternParser{src: escape[1:]}
		value, _ := p.unicodeEscapeValue()
		return value
	}
	return escape[0]
}

func isSyntaxCharacter(r rune) bool {
	switch r {
	case '^', '$', '\\', '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|':
		return true
	}
	return false
}

func isHexDigit(r rune) bool {
	return '0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F'
}

func hexValue(r rune) rune {
	switch {
	case '0' <= r && r <= '9':
		return r - '0'
	case 'a' <= r && r <= 'f':
		return r - 'a' + 10
	}
	return r - 'A' + 10
}

// propertyEscape reads what follows \p or \P in u mode: {Name}, a binary
// property or a General_Category value, or {Name=Value}, a General_Category,
// Script, or Script_Extensions value (§21.2.2.8.2), each name as ECMA-262
// 11th edition lists it for Unicode 13.0.
func (p *patternParser) propertyEscape() error {
	if p.peek(0) != '{' {
		return p.errorf("invalid property escape")
	}
	p.i++
	start := p.i
	for p.peek(0) != '}' {
		if p.peek(0) == -1 {
			return p.errorf("unterminated property escape")
		}
		p.i++
	}
	body := string(p.src[start:p.i])
	p.i++
	if name, value, pair := strings.Cut(body, "="); pair {
		if values, known := nonBinaryProperties[name]; known && values[value] {
			return nil
		}
		return p.errorf("unknown Unicode property %q", body)
	}
	if binaryProperties[body] || generalCategoryValues[body] {
		return nil
	}
	return p.errorf("unknown Unicode property %q", body)
}
