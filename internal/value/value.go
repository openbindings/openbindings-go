// Package value owns private invocation snapshots. Its trees are immutable by
// contract; only detached logical values and checked typed results leave the SDK.
package value

import (
	"context"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	codec "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

const DefaultMaxUnits int64 = 64 << 20
const DefaultMaxDepth = 256
const NodeUnits int64 = 64

type Limits struct {
	MaxUnits int64
	MaxDepth int
}

func (l Limits) Resolve() (Limits, error) {
	if l.MaxUnits == 0 {
		l.MaxUnits = DefaultMaxUnits
	}
	if l.MaxDepth == 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxUnits < 0 || l.MaxUnits > math.MaxInt64/8 || l.MaxDepth < 0 || l.MaxDepth > 10000 {
		return l, errors.New("invalid value limits")
	}
	return l, nil
}

type LimitError struct {
	Stage, Kind string
	Allowance   int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("value %s exceeds %s allowance (%d)", e.Stage, e.Kind, e.Allowance)
}

// Options supplies a bounded accounting operation, not an invocation dependency.
// Adjust reserves positive deltas before allocation and releases negative deltas.
// A successful Capture leaves its Cost reserved; callers own that reservation.
// Other operations release their SDK work reservation before returning.
type Options struct {
	Limits Limits
	Adjust func(int64) error
}

type Snapshot struct {
	root   any
	cost   int64
	native bool
	height int
}

func (s *Snapshot) Cost() int64 { return s.cost }
func (s *Snapshot) Depth() int  { return s.height }

type byteString []byte

var (
	fallback          = errors.New("codec interpretation required")
	numberType        = reflect.TypeFor[json.Number]()
	marshalType       = reflect.TypeFor[json.Marshaler]()
	textMarshalType   = reflect.TypeFor[encoding.TextMarshaler]()
	unmarshalType     = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

type work struct {
	ctx     context.Context
	options Options
	used    int64
	stage   string
	native  bool
	height  int
}

func newWork(ctx context.Context, options Options, stage string) (*work, error) {
	l, err := options.Limits.Resolve()
	if err != nil {
		return nil, err
	}
	options.Limits = l
	if ctx == nil {
		ctx = context.Background()
	}
	return &work{ctx: ctx, options: options, stage: stage}, nil
}
func (w *work) add(n int64) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if n < 0 || n > w.options.Limits.MaxUnits-w.used {
		return &LimitError{w.stage, "value", w.options.Limits.MaxUnits}
	}
	if w.options.Adjust != nil {
		if err := w.options.Adjust(n); err != nil {
			return err
		}
	}
	w.used += n
	return nil
}
func (w *work) depth(d int) error {
	w.height = max(w.height, d)
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if d > w.options.Limits.MaxDepth {
		return &LimitError{w.stage, "depth", int64(w.options.Limits.MaxDepth)}
	}
	return nil
}
func (w *work) release() {
	if w.options.Adjust != nil && w.used != 0 {
		_ = w.options.Adjust(-w.used)
	}
	w.used = 0
}
func (w *work) scratch() (func(int64) error, func()) {
	var held int64
	return func(n int64) error {
			if err := w.ctx.Err(); err != nil && n > 0 {
				return err
			}
			if n > 0 && (n > w.options.Limits.MaxUnits || held > w.options.Limits.MaxUnits-n) {
				return &LimitError{w.stage, "scratch", w.options.Limits.MaxUnits}
			}
			if n < -held {
				panic("value: scratch release exceeds reservation")
			}
			if w.options.Adjust != nil {
				if err := w.options.Adjust(n); err != nil {
					return err
				}
			}
			held += n
			return nil
		}, func() {
			if w.options.Adjust != nil && held != 0 {
				_ = w.options.Adjust(-held)
			}
			held = 0
		}
}

func Capture(ctx context.Context, input any, options Options) (*Snapshot, error) {
	w, err := newWork(ctx, options, "capture")
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			w.release()
		}
	}()
	root, err := w.capture(reflect.ValueOf(input), map[visit]bool{}, 1, false, 0)
	if errors.Is(err, fallback) {
		w.release()
		w.native = false
		w.height = 0
		if w.options.Limits.MaxUnits < NodeUnits {
			return nil, &LimitError{w.stage, "value", w.options.Limits.MaxUnits}
		}
		nodes := int64(0)
		if err := checkNumberCarriers(w.ctx, reflect.ValueOf(input), map[visit]bool{}, 1, w.options.Limits, &nodes); err != nil {
			return nil, err
		}
		account, release := w.scratch()
		defer release()
		raw, e := codec.MarshalBounded(w.ctx, input, codec.EncodeLimits{MaxBytes: w.options.Limits.MaxUnits, MaxNodes: w.options.Limits.MaxUnits / NodeUnits, MaxDepth: w.options.Limits.MaxDepth, Account: account})
		if e != nil {
			err = e
		} else if e = account(int64(len(raw))); e != nil {
			err = e
		} else {
			root, err = w.parse(raw)
		}
	}
	if err == nil {
		err = w.ctx.Err()
	}
	if err != nil {
		w.release()
		return nil, err
	}
	accepted = true
	return &Snapshot{root: root, cost: w.used, native: w.native, height: w.height}, nil
}

type visit struct {
	typ    reflect.Type
	ptr    uintptr
	length int
}

func customEncode(v reflect.Value) bool {
	t := v.Type()
	return t.Implements(marshalType) || t.Implements(textMarshalType) || v.CanAddr() && (reflect.PointerTo(t).Implements(marshalType) || reflect.PointerTo(t).Implements(textMarshalType))
}
func (w *work) capture(v reflect.Value, anc map[visit]bool, depth int, paid bool, indirections int) (any, error) {
	if err := w.depth(depth); err != nil {
		return nil, err
	}
	if indirections > w.options.Limits.MaxDepth*4+32 {
		return nil, &LimitError{w.stage, "depth", int64(w.options.Limits.MaxDepth)}
	}
	if !v.IsValid() {
		if !paid {
			if err := w.add(NodeUnits); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return w.capture(reflect.Value{}, anc, depth, paid, indirections+1)
		}
		return w.capture(v.Elem(), anc, depth, paid, indirections+1)
	}
	if customEncode(v) {
		return nil, fallback
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Map || v.Kind() == reflect.Slice {
		if v.IsNil() {
			return w.capture(reflect.Value{}, anc, depth, paid, indirections+1)
		}
		k := visit{typ: v.Type(), ptr: v.Pointer()}
		if v.Kind() == reflect.Slice {
			k.length = v.Len()
		}
		if anc[k] {
			return nil, errors.New("cyclic value")
		}
		anc[k] = true
		defer delete(anc, k)
	}
	if v.Kind() == reflect.Pointer {
		return w.capture(v.Elem(), anc, depth, paid, indirections+1)
	}
	if !paid {
		if err := w.add(NodeUnits); err != nil {
			return nil, err
		}
	}
	if v.Type() == numberType {
		s := v.String()
		if !codec.ValidNumberToken(s) {
			return nil, errors.New("invalid number token")
		}
		if err := w.add(int64(len(s))); err != nil {
			return nil, err
		}
		return json.Number(strings.Clone(s)), nil
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		s := v.String()
		if !utf8.ValidString(s) {
			return nil, fallback
		}
		if err := w.add(codec.StringContentSize(s, false)); err != nil {
			return nil, err
		}
		return strings.Clone(s), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		s := strconv.FormatInt(v.Int(), 10)
		if err := w.add(int64(len(s))); err != nil {
			return nil, err
		}
		return json.Number(s), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		s := strconv.FormatUint(v.Uint(), 10)
		if err := w.add(int64(len(s))); err != nil {
			return nil, err
		}
		return json.Number(s), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("nonfinite number")
		}
		var b []byte
		var err error
		if v.Kind() == reflect.Float32 {
			b, err = codec.Marshal(float32(f))
		} else {
			b, err = codec.Marshal(f)
		}
		if err != nil {
			return nil, err
		}
		if err = w.add(int64(len(b))); err != nil {
			return nil, err
		}
		if v.Kind() == reflect.Float64 {
			return f, nil
		}
		return json.Number(b), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			et := reflect.PointerTo(v.Type().Elem())
			if et.Implements(marshalType) || et.Implements(textMarshalType) {
				return nil, fallback
			}
			n := (int64(v.Len()) + 2) / 3 * 4
			if err := w.add(n); err != nil {
				return nil, err
			}
			w.native = true
			out := make(byteString, v.Len())
			copy(out, v.Bytes())
			return out, nil
		}
		fallthrough
	case reflect.Array:
		n := int64(v.Len())
		if n > w.options.Limits.MaxUnits/NodeUnits {
			return nil, &LimitError{w.stage, "value", w.options.Limits.MaxUnits}
		}
		if err := w.add(n * NodeUnits); err != nil {
			return nil, err
		}
		out := make([]any, v.Len())
		for i := range out {
			x, err := w.capture(v.Index(i), anc, depth+1, true, 0)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, fallback
		}
		n := int64(v.Len())
		if n > w.options.Limits.MaxUnits/NodeUnits {
			return nil, &LimitError{w.stage, "value", w.options.Limits.MaxUnits}
		}
		if err := w.add(n * NodeUnits); err != nil {
			return nil, err
		}
		out := make(map[string]any, v.Len())
		it := v.MapRange()
		for it.Next() {
			key := it.Key().String()
			if !utf8.ValidString(key) {
				return nil, fallback
			}
			if err := w.add(codec.StringContentSize(key, false)); err != nil {
				return nil, err
			}
			x, err := w.capture(it.Value(), anc, depth+1, true, 0)
			if err != nil {
				return nil, err
			}
			out[strings.Clone(key)] = x
		}
		return out, nil
	case reflect.Struct:
		out := make(map[string]any)
		for _, f := range codec.ValueFields(v.Type()) {
			if f.Quoted || f.OmitZero {
				return nil, fallback
			}
			fv, ok := fieldValue(v, f.Index, false)
			if !ok {
				continue
			}
			if !fv.CanInterface() {
				return nil, fallback
			}
			if fv.Type() == numberType && !codec.ValidNumberToken(fv.String()) {
				return nil, errors.New("invalid number token")
			}
			if f.OmitEmpty && isEmpty(fv) {
				continue
			}
			if err := w.add(codec.StringContentSize(f.Name, false)); err != nil {
				return nil, err
			}
			x, err := w.capture(fv, anc, depth+1, false, 0)
			if err != nil {
				return nil, err
			}
			out[f.Name] = x
		}
		return out, nil
	default:
		return nil, fallback
	}
}
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}
func fieldValue(v reflect.Value, index []int, allocate bool) (reflect.Value, bool) {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !allocate || !v.CanSet() {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

// parse checks/charges each logical node before storing it. The encoded input
// is already bounded and reserved; callbacks are never replayed while parsing.
func (w *work) parse(raw []byte) (any, error) {
	d := codec.NewValueDecoder(raw)
	v, err := w.parseValue(d, 1)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	return v, nil
}
func (w *work) parseValue(d *codec.Decoder, depth int) (any, error) {
	if err := w.depth(depth); err != nil {
		return nil, err
	}
	if err := w.add(NodeUnits); err != nil {
		return nil, err
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case codec.Delim:
		switch x {
		case '{':
			out := make(map[string]any)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				s, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if err = w.add(codec.StringContentSize(s, false)); err != nil {
					return nil, err
				}
				v, err := w.parseValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				if old, exists := out[s]; exists {
					oldCost, _ := logicalCost(old)
					released := oldCost + codec.StringContentSize(s, false)
					w.used -= released
					if w.options.Adjust != nil {
						_ = w.options.Adjust(-released)
					}
				}
				out[s] = v
			}
			_, err := d.Token()
			return out, err
		case '[':
			out := []any{}
			for d.More() {
				v, err := w.parseValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err := d.Token()
			return out, err
		}
		return nil, errors.New("invalid JSON delimiter")
	case string:
		if err = w.add(codec.StringContentSize(x, false)); err != nil {
			return nil, err
		}
		return x, nil
	case json.Number:
		if err = w.add(int64(len(x))); err != nil {
			return nil, err
		}
		return x, nil
	case nil, bool:
		return x, nil
	default:
		return nil, errors.New("unsupported JSON token")
	}
}

// Logical returns an independent mutable logical tree. In particular private
// byte leaves become Base64 strings; they never escape through a public any.
func (s *Snapshot) Logical(ctx context.Context, options Options) (any, error) {
	w, err := newWork(ctx, options, "logical delivery")
	if err != nil {
		return nil, err
	}
	defer w.release()
	return w.logical(s.root, 1)
}
func (w *work) logical(v any, depth int) (any, error) {
	if err := w.depth(depth); err != nil {
		return nil, err
	}
	if err := w.add(NodeUnits); err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case byteString:
		if err := w.add((int64(len(x)) + 2) / 3 * 4); err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString(x), nil
	case string:
		if err := w.add(codec.StringContentSize(x, false)); err != nil {
			return nil, err
		}
		return strings.Clone(x), nil
	case float64:
		b, _ := codec.Marshal(x)
		if err := w.add(int64(len(b))); err != nil {
			return nil, err
		}
		return x, nil
	case json.Number:
		if err := w.add(int64(len(x))); err != nil {
			return nil, err
		}
		return x, nil
	case map[string]any:
		out := make(map[string]any)
		for k, y := range x {
			if err := w.add(codec.StringContentSize(k, false)); err != nil {
				return nil, err
			}
			z, err := w.logical(y, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = z
		}
		return out, nil
	case []any:
		out := []any{}
		for _, y := range x {
			z, err := w.logical(y, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, z)
		}
		return out, nil
	default:
		return x, nil
	}
}

// Export is an explicit bounded conversion, independent of invocation lifetime.
func (s *Snapshot) Export(ctx context.Context, options Options) ([]byte, error) {
	w, err := newWork(ctx, options, "export")
	if err != nil {
		return nil, err
	}
	defer w.release()
	if err = w.add(s.cost); err != nil {
		return nil, err
	}
	account, release := w.scratch()
	defer release()
	return codec.MarshalBounded(w.ctx, exportRoot(s.root), codec.EncodeLimits{MaxBytes: w.options.Limits.MaxUnits, MaxNodes: w.options.Limits.MaxUnits / NodeUnits, MaxDepth: w.options.Limits.MaxDepth, Account: account})
}

// []byte is already a codec-defined Base64 string. No tree conversion is needed.
func exportRoot(v any) any { return v }

// ReadOnlyLogical borrows an already logical immutable tree for synchronous SDK
// readers only. Application hooks and public results must use detached delivery.
func (s *Snapshot) ReadOnlyLogical() (any, bool) { return s.root, !s.native }

// Match jsonvalue's malformed-number policy before a whole-value fallback.
// This never invokes application codecs and bounds its own traversal.
func checkNumberCarriers(ctx context.Context, v reflect.Value, seen map[visit]bool, depth int, limits Limits, nodes *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > limits.MaxDepth*4+32 {
		return &LimitError{"capture", "depth", int64(limits.MaxDepth)}
	}
	*nodes++
	if *nodes > limits.MaxUnits/NodeUnits {
		return &LimitError{"capture", "nodes", limits.MaxUnits / NodeUnits}
	}
	if !v.IsValid() {
		return nil
	}
	if v.Type() == numberType {
		if !codec.ValidNumberToken(v.String()) {
			return errors.New("invalid number token")
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return checkNumberCarriers(ctx, v.Elem(), seen, depth+1, limits, nodes)
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil
		}
		k := visit{typ: v.Type(), ptr: v.Pointer()}
		if v.Kind() == reflect.Slice {
			k.length = v.Len()
		}
		if seen[k] {
			return nil
		}
		seen[k] = true
		defer delete(seen, k)
	}
	visitChild := func(child reflect.Value) error { return checkNumberCarriers(ctx, child, seen, depth+1, limits, nodes) }
	switch v.Kind() {
	case reflect.Pointer:
		return visitChild(v.Elem())
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if err := visitChild(it.Value()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := visitChild(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			if err := visitChild(v.Field(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
