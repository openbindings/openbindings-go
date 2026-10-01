package openbindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Position is where a finding lies in the bytes ValidateDocument or
// ParseDocument read, counted as go/token counts: Offset in bytes from 0,
// Line and Column from 1, Column in bytes. A finding located at an object
// member is positioned at the member's name; any other, at the first byte of
// its value, or, for a syntax error, of the byte that breaks the syntax (the
// end of the input, when it ends too early). The zero Position is unknown:
// Document.Validate reads no bytes, so its findings carry none.
type Position struct {
	Offset, Line, Column int
}

// IsValid reports whether the position is known.
func (p Position) IsValid() bool { return p.Line > 0 }

// String returns the position as "line:column", or "-" when it is unknown.
func (p Position) String() string {
	if !p.IsValid() {
		return "-"
	}
	return strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column)
}

// positionFindings gives each finding without a position the position of its
// path in data, where the input reaches it.
func positionFindings(data []byte, findings []Finding) {
	var paths []string
	for _, finding := range findings {
		if !finding.Position.IsValid() {
			paths = append(paths, finding.Path)
		}
	}
	if len(paths) == 0 {
		return
	}
	offsets := locate(data, paths)
	lines := lineStarts(data)
	for i := range findings {
		if offset, located := offsets[findings[i].Path]; located && !findings[i].Position.IsValid() {
			findings[i].Position = positionAt(lines, offset)
		}
	}
}

// d01Position returns where the input breaks OBI-D-01: the repeated name,
// the byte that breaks the syntax, the first byte that is not UTF-8, or the
// byte-order mark.
func d01Position(data []byte, err error) Position {
	lines := lineStarts(data)
	var repeated *duplicateNameError
	var syntax *json.SyntaxError
	switch {
	case errors.As(err, &repeated):
		return positionAt(lines, repeated.at)
	case bytes.HasPrefix(data, byteOrderMark):
		return positionAt(lines, 0)
	case !utf8.Valid(data):
		offset := 0
		for offset < len(data) {
			r, size := utf8.DecodeRune(data[offset:])
			if r == utf8.RuneError && size <= 1 {
				break
			}
			offset += size
		}
		return positionAt(lines, offset)
	case errors.As(json.Unmarshal(data, new(json.RawMessage)), &syntax):
		// encoding/json counts the byte that breaks the syntax as read, and
		// reports input that ends too early at its end.
		offset := int(syntax.Offset)
		if !strings.HasPrefix(syntax.Error(), "unexpected end") {
			offset--
		}
		return positionAt(lines, min(max(offset, 0), len(data)))
	}
	return Position{}
}

// lineStarts returns the byte offset at which each line of data begins.
func lineStarts(data []byte) []int {
	starts := []int{0}
	for i, c := range data {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// positionAt returns the position of a byte offset, given the offsets at
// which lines begin.
func positionAt(lines []int, offset int) Position {
	line := sort.SearchInts(lines, offset+1) - 1
	return Position{Offset: offset, Line: line + 1, Column: offset - lines[line] + 1}
}
