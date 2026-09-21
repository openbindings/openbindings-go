package json

// This file is maintained SDK support, not part of the relocated upstream API.
// It shares the codec's field and scalar rules with the private value adapter.

import (
	"context"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// ValueField is immutable metadata. Callers must not modify Index.
type ValueField struct {
	Name                        string
	Index                       []int
	Quoted, OmitEmpty, OmitZero bool
}

// ValueFields returns field selection from the maintained codec. It deliberately
// invokes no user IsZero/encoding method. OmitZero requires codec fallback.
func ValueFields(t reflect.Type) []ValueField {
	p := cachedTypeFields(t)
	out := make([]ValueField, len(p.list))
	for i, f := range p.list {
		out[i] = ValueField{f.name, f.index, f.quoted, f.omitEmpty, f.omitZero}
	}
	return out
}

func LookupValueField(t reflect.Type, name string) (ValueField, bool) {
	p := cachedTypeFields(t)
	f := p.byExactName[name]
	if f == nil {
		f = p.byFoldedName[string(foldName([]byte(name)))]
	}
	if f == nil {
		return ValueField{}, false
	}
	return ValueField{f.name, f.index, f.quoted, f.omitEmpty, f.omitZero}, true
}

func ValidNumberToken(s string) bool { return s != "" && isValidNumber(s) }

// StringContentSize counts the codec's escaped representation without encoding.
func StringContentSize(s string, escapeHTML bool) int64 { return stringContentSize(s, escapeHTML) }
func stringContentSize[S ~string | ~[]byte](s S, escapeHTML bool) int64 {
	var n int64
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '\\' || b == '"' || b == '\b' || b == '\f' || b == '\n' || b == '\r' || b == '\t':
				n += 2
			case b < 0x20 || escapeHTML && (b == '<' || b == '>' || b == '&'):
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(string(s[i:min(len(s), i+4)]))
		if r == utf8.RuneError && size == 1 {
			n += 6
			if len(s)-i >= 3 && b == 0xed && s[i+1] >= 0xa0 && s[i+1] <= 0xbf && s[i+2]&0xc0 == 0x80 {
				size = 3
			}
		} else if r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += int64(size)
		}
		i += size
	}
	return n
}

// EncodeLimitError contains no encoded data or user error text.
type EncodeLimitError struct {
	Kind  string
	Limit int64
}

func (e *EncodeLimitError) Error() string {
	return fmt.Sprintf("JSON encoding %s limit exceeded (%d)", e.Kind, e.Limit)
}

type EncodeLimits struct {
	MaxBytes int64
	MaxNodes int64
	MaxDepth int
	// Account reserves or releases SDK scratch, before buffer growth. The
	// caller owns any successful reservation until the returned bytes die.
	Account func(int64) error
}

type encodeBound struct {
	ctx             context.Context
	limits          EncodeLimits
	nodes           int64
	depth, pointers int
	reserved        int64
}

// MarshalBounded encodes with checks inside the encoder, before SDK allocation.
// Callback allocations remain callback-owned. Unlike Marshal, this does not
// retain the result in an encoder pool or make a second whole-buffer copy.
func MarshalBounded(ctx context.Context, v any, limits EncodeLimits) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limits.MaxBytes <= 0 || limits.MaxNodes <= 0 || limits.MaxDepth <= 0 {
		return nil, fmt.Errorf("invalid JSON encoding limits")
	}
	e := &encodeState{ptrSeen: make(map[any]struct{}), bound: &encodeBound{ctx: ctx, limits: limits}}
	err := e.marshal(v, encOpts{escapeHTML: false})
	if err != nil {
		return nil, err
	}
	return e.Bytes(), nil
}

func (e *encodeState) prepare(n int64) {
	if e.bound == nil {
		return
	}
	b := e.bound
	if err := b.ctx.Err(); err != nil {
		e.error(err)
	}
	if n < 0 || n > b.limits.MaxBytes-int64(e.Len()) {
		e.error(&EncodeLimitError{"bytes", b.limits.MaxBytes})
	}
	need := int64(e.Len()) + n
	if need <= int64(e.Cap()) {
		return
	}
	// bytes.Buffer can double its backing storage. Count old and new storage
	// simultaneously; runtime allocation rounding is not a byte-exact quota.
	want := max(2*int64(e.Cap()), need) + int64(e.Cap()) + 64
	if want > b.reserved {
		if b.limits.Account != nil {
			if err := b.limits.Account(want - b.reserved); err != nil {
				e.error(err)
			}
		}
		b.reserved = want
	}
}

func (e *encodeState) Write(p []byte) (int, error) {
	e.prepare(int64(len(p)))
	return e.Buffer.Write(p)
}
func (e *encodeState) WriteString(s string) (int, error) {
	e.prepare(int64(len(s)))
	return e.Buffer.WriteString(s)
}
func (e *encodeState) WriteByte(b byte) error { e.prepare(1); return e.Buffer.WriteByte(b) }
func (e *encodeState) Grow(n int)             { e.prepare(int64(n)); e.Buffer.Grow(n) }

func boundedEncoder(t reflect.Type, f encoderFunc) encoderFunc {
	return func(e *encodeState, v reflect.Value, opts encOpts) {
		if e.bound == nil {
			f(e, v, opts)
			return
		}
		b := e.bound
		if err := b.ctx.Err(); err != nil {
			e.error(err)
		}
		b.pointers++
		defer func() { b.pointers-- }()
		if b.pointers > b.limits.MaxDepth*4+32 {
			e.error(&EncodeLimitError{"depth", int64(b.limits.MaxDepth)})
		}
		if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface {
			b.nodes++
			if b.nodes > b.limits.MaxNodes {
				e.error(&EncodeLimitError{"nodes", b.limits.MaxNodes})
			}
			b.depth++
			defer func() { b.depth-- }()
			if b.depth > b.limits.MaxDepth {
				e.error(&EncodeLimitError{"depth", int64(b.limits.MaxDepth)})
			}
		}
		f(e, v, opts)
	}
}

// prepareCompact bounds SDK expansion of a user-produced JSON fragment before
// appendCompact can allocate. User whitespace is also bounded deliberately.
func (e *encodeState) prepareCompact(src []byte, escape bool) {
	if e.bound == nil {
		return
	}
	n := int64(len(src))
	depth := e.bound.depth - 1
	quoted, escaped := false, false
	for i, c := range src {
		if escape && (c == '<' || c == '>' || c == '&') {
			n += 5
		}
		if escape && c == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			n += 3
		}
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > e.bound.limits.MaxDepth {
				e.error(&EncodeLimitError{"depth", int64(e.bound.limits.MaxDepth)})
			}
		case '}', ']':
			depth--
		}
	}
	e.Grow(int(n))
}

func (e *encodeState) prepareString(s string, escape, quoted bool) {
	if e.bound == nil {
		return
	}
	n := StringContentSize(s, escape) + 2
	if quoted { // the first encoding is also escaped as a JSON string
		n = 2*n + 2
	}
	e.Grow(int(n))
}

func (e *encodeState) prepareText(s []byte, escape bool) {
	if e.bound != nil {
		e.Grow(int(stringContentSize(s, escape) + 2))
	}
}

// NewValueDecoder consumes an SDK-owned, already bounded complete buffer. It
// cannot allocate a second streaming buffer. The caller reserves materialized
// nodes/strings separately and keeps data alive until decoding finishes.
func NewValueDecoder(data []byte) *Decoder {
	d := &Decoder{buf: data, completeBuffer: true}
	d.UseNumber()
	return d
}
