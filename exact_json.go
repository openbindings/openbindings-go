package openbindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// jsonNestingLimit is how deeply encoding/json nests the values it decodes.
// Input nesting deeper is read in full for OBI-01, but the document model
// and the generic view cannot be decoded from it.
const jsonNestingLimit = 10000

// errNestingLimit is the error for input nested deeper than jsonNestingLimit:
// a resource limit met, which is no evidence about any rule but OBI-01
// (Reports and Verdicts, in the package documentation).
var errNestingLimit = fmt.Errorf("nested deeper than the decoder reads (%d levels)", jsonNestingLimit)

// errUnexpectedEnd is the syntax error for input that ends inside a value.
var errUnexpectedEnd = errors.New("unexpected end of JSON input")

// loneSurrogateError reports a string escape of an isolated UTF-16 surrogate
// (a lone \uD800, say). RFC 8259 admits it, so it breaks no document rule,
// but a Go string cannot hold it and encoding/json would replace it with
// U+FFFD, altering the document; the document model does not carry it, a
// capability this SDK lacks (Reports and Verdicts).
type loneSurrogateError struct {
	// location is the JSON Pointer of the string value holding it, or of the
	// object whose member name holds it.
	location string
	name     bool
}

func (e *loneSurrogateError) Error() string {
	where := "a string at " + strconv.Quote(e.location)
	if e.name {
		where = "a member name of the object at " + strconv.Quote(e.location)
	}
	return where + " holds an escape of a lone UTF-16 surrogate, which this SDK does not carry"
}

// duplicateNameError reports an object that repeats a member name, which
// OBI-01 refuses.
type duplicateNameError struct {
	// location is the JSON Pointer of the object, and at the byte offset of
	// the repeated name.
	location string
	name     string
	at       int
}

func (e *duplicateNameError) Error() string {
	return "the object at " + strconv.Quote(e.location) + " repeats the member name " + strconv.Quote(e.name)
}

// byteOrderMark is the UTF-8 encoding of U+FEFF.
var byteOrderMark = []byte("\xef\xbb\xbf")

// verifyExactJSON checks what decoding JSON into Go values would otherwise
// lose without error: that the input is one valid UTF-8 JSON value with no
// leading byte-order mark, and that no object in it repeats a member name,
// which OBI-01 requires; and that no string escapes a lone UTF-16
// surrogate, which the document model cannot carry. encoding/json replaces
// invalid UTF-8 and lone surrogates and keeps only the last of repeated names.
// A syntax error is reported before a repeated name, which is reported before
// a lone surrogate, and all before nesting deeper than the decoder reads: the
// first two break OBI-01, the others only exceed what the SDK can carry.
func verifyExactJSON(b []byte) error {
	if !utf8.Valid(b) {
		return errors.New("not valid UTF-8")
	}
	if bytes.HasPrefix(b, byteOrderMark) {
		return errors.New("begins with a byte-order mark")
	}
	scan := exactScan{b: b}
	if err := scan.run(); err != nil {
		return err
	}
	switch {
	case scan.repeat != nil:
		return scan.repeat
	case scan.lone != nil:
		return scan.lone
	case scan.deepest > jsonNestingLimit:
		return errNestingLimit
	}
	return nil
}

// declaredVersion returns the version a text declares, which this SDK reads
// before any rule is decided (§8.1, Version declaration): a text declares a
// version exactly when it is UTF-8 with no leading byte-order mark and parses
// under the JSON grammar as an object with exactly one openbindings member
// whose value is a string that is a SemVer version; that value is the
// version. Repeated names elsewhere do not stop a declaration, nor does a
// string escaping a lone UTF-16 surrogate, which the grammar admits: under a
// supported version, OBI-01 judges the first, and the second is beyond what
// this SDK carries. Any other text declares no version, an ill-formed byte
// anywhere in it included. The text is read at any depth.
func declaredVersion(data []byte) (string, bool) {
	if !utf8.Valid(data) || bytes.HasPrefix(data, byteOrderMark) {
		return "", false
	}
	raw, found := versionMember(data)
	if !found || raw[0] != '"' {
		return "", false
	}
	version, _ := exactString(raw)
	if !isValidSemver(version) {
		return "", false
	}
	return version, true
}

// versionMember returns the raw value of the root object's openbindings
// member, whatever its type, when data parses under the JSON grammar as an
// object with exactly one such member, member names compared after
// unescaping (§5). It does not check that data is UTF-8.
func versionMember(data []byte) ([]byte, bool) {
	scan := exactScan{b: data, readVersion: true}
	if scan.run() != nil || len(scan.versions) != 1 {
		return nil, false
	}
	return scan.versions[0], true
}

// exactScan reads JSON a byte at a time, keeping its own stack rather than
// recursing, so input of any depth is read in constant stack space. It checks
// that the input is one JSON value (RFC 8259), and records what decoding into
// Go values would lose: the first repeated member name and the first escaped
// lone surrogate, in document order.
type exactScan struct {
	b []byte

	// open holds, for each object or array the scan is inside, the names
	// the object has had so far; nil for an array.
	open    []map[string]struct{}
	deepest int
	path    []scanStep // where the value being read lies

	repeat *duplicateNameError
	lone   *loneSurrogateError

	// With readVersion, versions holds the raw value of every member of the
	// root object named openbindings.
	readVersion bool
	versions    [][]byte
	versionAt   int // where the root member's value being read begins; -1 when none

	// With want, the scan records in located where each wanted location
	// begins (locate); nodes holds the want node of each step of path, nil
	// where nothing wanted lies at or below it.
	want    *wantNode
	nodes   []*wantNode
	located map[string]int
}

// scanStep is one reference token of the scan's path: a member name, or an
// array index, which is formatted only when a location is reported.
type scanStep struct {
	name  string
	index int // -1 for a member name
}

// location returns the scan's path as a JSON Pointer.
func (s *exactScan) location() string {
	tokens := make([]string, len(s.path))
	for i, step := range s.path {
		tokens[i] = step.name
		if step.index >= 0 {
			tokens[i] = strconv.Itoa(step.index)
		}
	}
	return jsonpointer.Format(tokens...)
}

// run reads the input as one JSON value and returns the first syntax error.
func (s *exactScan) run() error {
	b := s.b
	s.versionAt = -1
	i := skipJSONSpace(b, 0)
	if s.want != nil && s.want.wanted {
		s.located[""] = i
	}
values:
	for {
		// A value begins at b[i].
		if i >= len(b) {
			return errUnexpectedEnd
		}
		var err error
		switch c := b[i]; {
		case c == '{':
			s.enter(map[string]struct{}{})
			if i = skipJSONSpace(b, i+1); i < len(b) && b[i] == '}' {
				s.open = s.open[:len(s.open)-1]
				i++
				break
			}
			if i, err = s.member(i); err != nil {
				return err
			}
			continue values
		case c == '[':
			s.enter(nil)
			s.push(scanStep{index: 0})
			if i = skipJSONSpace(b, i+1); i < len(b) && b[i] == ']' {
				s.open = s.open[:len(s.open)-1]
				s.pop()
				i++
				break
			}
			s.reach(i)
			continue values
		case c == '"':
			var lone bool
			if i, lone, err = stringEnd(b, i); err != nil {
				return err
			}
			if lone && s.lone == nil {
				s.lone = &loneSurrogateError{location: s.location()}
			}
		case c == '-' || '0' <= c && c <= '9':
			if i, err = numberEnd(b, i); err != nil {
				return err
			}
		case c == 't':
			i, err = literalEnd(b, i, "true")
		case c == 'f':
			i, err = literalEnd(b, i, "false")
		case c == 'n':
			i, err = literalEnd(b, i, "null")
		default:
			return syntaxError(c, "looking for beginning of value")
		}
		if err != nil {
			return err
		}
		// A value has ended just before b[i]; close every container it
		// completes.
		for {
			if len(s.open) == 0 {
				if i = skipJSONSpace(b, i); i < len(b) {
					return syntaxError(b[i], "after top-level value")
				}
				return nil
			}
			if len(s.open) == 1 && s.versionAt >= 0 {
				s.versions = append(s.versions, b[s.versionAt:i])
				s.versionAt = -1
			}
			if i = skipJSONSpace(b, i); i >= len(b) {
				return errUnexpectedEnd
			}
			names := s.open[len(s.open)-1]
			switch {
			case b[i] == ',' && names == nil:
				s.nextIndex()
				i = skipJSONSpace(b, i+1)
				s.reach(i)
				continue values
			case b[i] == ',':
				s.pop()
				if i, err = s.member(skipJSONSpace(b, i+1)); err != nil {
					return err
				}
				continue values
			case b[i] == ']' && names == nil, b[i] == '}' && names != nil:
				s.open = s.open[:len(s.open)-1]
				s.pop()
				i++
			case names == nil:
				return syntaxError(b[i], "after array element")
			default:
				return syntaxError(b[i], "after object key:value pair")
			}
		}
	}
}

// push appends a step to the scan's path, with its want node.
func (s *exactScan) push(step scanStep) {
	s.path = append(s.path, step)
	if s.want == nil {
		return
	}
	parent := s.want
	if len(s.nodes) > 0 {
		parent = s.nodes[len(s.nodes)-1]
	}
	var node *wantNode
	if parent != nil {
		token := step.name
		if step.index >= 0 {
			token = strconv.Itoa(step.index)
		}
		node = parent.children[token]
	}
	s.nodes = append(s.nodes, node)
}

// pop removes the last step of the scan's path.
func (s *exactScan) pop() {
	s.path = s.path[:len(s.path)-1]
	if s.want != nil {
		s.nodes = s.nodes[:len(s.nodes)-1]
	}
}

// nextIndex advances the innermost array's index.
func (s *exactScan) nextIndex() {
	step := s.path[len(s.path)-1]
	s.pop()
	s.push(scanStep{index: step.index + 1})
}

// reach records, when it is wanted, that the location the scan's path names
// begins at b[i]: its first occurrence, should a name repeat.
func (s *exactScan) reach(i int) {
	if s.want == nil || len(s.nodes) == 0 {
		return
	}
	if node := s.nodes[len(s.nodes)-1]; node != nil && node.wanted {
		if _, seen := s.located[node.path]; !seen {
			s.located[node.path] = i
		}
	}
}

// wantNode is one reference token of the locations a scan is to find.
type wantNode struct {
	children map[string]*wantNode
	// wanted marks a location to find, path its JSON Pointer.
	wanted bool
	path   string
}

// locate returns the byte offset where each of paths begins in data: a
// member's name, or an array element's or the document's value. A path the
// input does not reach, or reaches only past a syntax error, is absent.
func locate(data []byte, paths []string) map[string]int {
	root := &wantNode{}
	for _, path := range paths {
		tokens, ok := jsonpointer.Parse(path)
		if !ok {
			continue
		}
		node := root
		for _, token := range tokens {
			if node.children == nil {
				node.children = map[string]*wantNode{}
			}
			child := node.children[token]
			if child == nil {
				child = &wantNode{}
				node.children[token] = child
			}
			node = child
		}
		node.wanted, node.path = true, path
	}
	scan := exactScan{b: data, want: root, located: map[string]int{}}
	_ = scan.run() // what it located before any syntax error stands
	return scan.located
}

// enter opens an object, with the names it has had, or an array, with nil.
func (s *exactScan) enter(names map[string]struct{}) {
	s.open = append(s.open, names)
	s.deepest = max(s.deepest, len(s.open))
}

// member reads the member name at b[i] of the innermost object and the colon
// after it, and returns the index where the member's value begins, with the
// name on the scan's path.
func (s *exactScan) member(i int) (int, error) {
	b := s.b
	if i >= len(b) {
		return 0, errUnexpectedEnd
	}
	if b[i] != '"' {
		return 0, syntaxError(b[i], "looking for beginning of object key string")
	}
	nameEnd, _, err := stringEnd(b, i)
	if err != nil {
		return 0, err
	}
	name, lone := exactString(b[i:nameEnd])
	if lone && s.lone == nil {
		s.lone = &loneSurrogateError{location: s.location(), name: true}
	}
	names := s.open[len(s.open)-1]
	if _, repeated := names[name]; repeated && s.repeat == nil {
		s.repeat = &duplicateNameError{location: s.location(), name: name, at: i}
	}
	names[name] = struct{}{}
	s.push(scanStep{name: name, index: -1})
	s.reach(i)
	if i = skipJSONSpace(b, nameEnd); i >= len(b) {
		return 0, errUnexpectedEnd
	}
	if b[i] != ':' {
		return 0, syntaxError(b[i], "after object key")
	}
	i = skipJSONSpace(b, i+1)
	if s.readVersion && len(s.open) == 1 && name == "openbindings" {
		s.versionAt = i
	}
	return i, nil
}

// stringEnd reads the string token at b[i] and returns the index just past
// it, and whether it escapes a lone UTF-16 surrogate, pairing escapes exactly
// as exactString does.
func stringEnd(b []byte, i int) (end int, lone bool, err error) {
	for i++; i < len(b); i++ {
		switch c := b[i]; {
		case c == '"':
			return i + 1, lone, nil
		case c < 0x20:
			return 0, false, syntaxError(c, "in string literal")
		case c == '\\':
			if i++; i >= len(b) {
				return 0, false, errUnexpectedEnd
			}
			switch b[i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				unit, err := hexEscape(b, i+1)
				if err != nil {
					return 0, false, err
				}
				i += 4
				if !utf16.IsSurrogate(rune(unit)) {
					continue
				}
				if unit < 0xdc00 && i+6 < len(b) && b[i+1] == '\\' && b[i+2] == 'u' {
					if low, err := hexEscape(b, i+3); err == nil && low >= 0xdc00 && low <= 0xdfff {
						i += 6
						continue
					}
				}
				lone = true
			default:
				return 0, false, syntaxError(b[i], "in string escape code")
			}
		}
	}
	return 0, false, errUnexpectedEnd
}

// hexEscape reads the four hexadecimal digits of a \u escape at b[i].
func hexEscape(b []byte, i int) (uint16, error) {
	for k := i; k < i+4; k++ {
		if k >= len(b) {
			return 0, errUnexpectedEnd
		}
		if c := b[k]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return 0, syntaxError(c, "in \\u hexadecimal character escape")
		}
	}
	return hexUnit(b[i : i+4]), nil
}

// numberEnd reads the number token at b[i] (RFC 8259 §6) and returns the
// index just past it.
func numberEnd(b []byte, i int) (int, error) {
	digits := func(i int, context string) (int, error) {
		if i >= len(b) {
			return 0, errUnexpectedEnd
		}
		if b[i] < '0' || b[i] > '9' {
			return 0, syntaxError(b[i], context)
		}
		for i < len(b) && '0' <= b[i] && b[i] <= '9' {
			i++
		}
		return i, nil
	}
	if b[i] == '-' {
		i++
	}
	var err error
	if i < len(b) && b[i] == '0' {
		i++
	} else if i, err = digits(i, "in numeric literal"); err != nil {
		return 0, err
	}
	if i < len(b) && b[i] == '.' {
		if i, err = digits(i+1, "after decimal point in numeric literal"); err != nil {
			return 0, err
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		if i++; i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i, err = digits(i, "in exponent of numeric literal"); err != nil {
			return 0, err
		}
	}
	return i, nil
}

// literalEnd reads the literal token at b[i] and returns the index just
// past it.
func literalEnd(b []byte, i int, literal string) (int, error) {
	for k := 1; k < len(literal); k++ {
		if i+k >= len(b) {
			return 0, errUnexpectedEnd
		}
		if b[i+k] != literal[k] {
			return 0, syntaxError(b[i+k], fmt.Sprintf("in literal %s (expecting %s)", literal, quoteByte(literal[k])))
		}
	}
	return i + len(literal), nil
}

// syntaxError is a syntax error at the byte c, worded as encoding/json words
// its own.
func syntaxError(c byte, context string) error {
	return fmt.Errorf("invalid character %s %s", quoteByte(c), context)
}

// quoteByte quotes a byte as encoding/json's syntax errors do.
func quoteByte(c byte) string {
	if c == '\'' {
		return `'\''`
	}
	if c == '"' {
		return `'"'`
	}
	s := strconv.Quote(string(c))
	return "'" + s[1:len(s)-1] + "'"
}

// exactString decodes a JSON string token of valid JSON exactly: an escape of
// a lone UTF-16 surrogate becomes that code unit's three-byte encoding, so
// "\uD800" and "\uFFFD" stay distinct, and lone reports whether the token
// held one.
func exactString(token []byte) (value string, lone bool) {
	body := token[1 : len(token)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		return string(body), false
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			out = append(out, body[i])
			continue
		}
		i++
		switch body[i] {
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			unit := hexUnit(body[i+1 : i+5])
			i += 4
			if utf16.IsSurrogate(rune(unit)) {
				if unit < 0xdc00 && i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
					if low := hexUnit(body[i+3 : i+7]); low >= 0xdc00 && low <= 0xdfff {
						out = utf8.AppendRune(out, utf16.DecodeRune(rune(unit), rune(low)))
						i += 6
						continue
					}
				}
				lone = true
				out = append(out, 0xe0|byte(unit>>12), 0x80|byte(unit>>6)&0x3f, 0x80|byte(unit)&0x3f)
				continue
			}
			out = utf8.AppendRune(out, rune(unit))
		default: // " \ /
			out = append(out, body[i])
		}
	}
	return string(out), lone
}

// hexUnit reads four hexadecimal digits of valid JSON.
func hexUnit(digits []byte) uint16 {
	unit, _ := strconv.ParseUint(string(digits), 16, 16)
	return uint16(unit)
}

// unmarshalJSON decodes exactly one JSON value into target, keeping numbers
// as json.Number. The decoding is exact on input verifyExactJSON accepts.
func unmarshalJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON holds more than one value")
	}
	return nil
}

type objectEntry struct {
	name  string
	value json.RawMessage // aliases the input
}

// splitObject splits a JSON object, in input verifyExactJSON has accepted,
// into its entries in document order. Entry values alias the input.
func splitObject(b []byte) ([]objectEntry, error) {
	i := skipJSONSpace(b, 0)
	if i == len(b) || b[i] != '{' {
		if isJSONNull(b) {
			return nil, errNullJSONObject
		}
		return nil, errNotJSONObject
	}
	var entries []objectEntry
	i = skipJSONSpace(b, i+1)
	for b[i] != '}' {
		nameEnd := jsonValueEnd(b, i)
		// verifyExactJSON refused a name holding a lone surrogate.
		name, _ := exactString(b[i:nameEnd])
		start := skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1) // past the colon
		end := jsonValueEnd(b, start)
		entries = append(entries, objectEntry{name: name, value: b[start:end]})
		i = skipJSONSpace(b, end)
		if b[i] == ',' {
			i = skipJSONSpace(b, i+1)
		}
	}
	return entries, nil
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// jsonValueEnd returns the index just past the JSON value starting at b[i],
// in valid JSON.
func jsonValueEnd(b []byte, i int) int {
	switch b[i] {
	case '"':
		for i++; b[i] != '"'; i++ {
			if b[i] == '\\' {
				i++
			}
		}
		return i + 1
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				i = jsonValueEnd(b, i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return i
	default:
		for i < len(b) && !strings.ContainsRune(",}] \t\n\r", rune(b[i])) {
			i++
		}
		return i
	}
}

func isJSONNull(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}
