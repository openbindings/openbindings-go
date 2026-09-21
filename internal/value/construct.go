package value

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strconv"

	codec "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

// Construct performs fresh checked construction. The source remains immutable;
// a failed conversion exposes neither a partially decoded target nor an alias.
func Construct[T any](ctx context.Context, s *Snapshot, options Options) (T, error) {
	var zero T
	w, err := newWork(ctx, options, "construction")
	if err != nil {
		return zero, err
	}
	t := reflect.TypeFor[T]()
	n, plain, err := estimate(w, s.root, t, 1)
	if err != nil {
		return zero, err
	}
	if err = w.add(max(s.cost, n)); err != nil {
		return zero, err
	}
	dst := reflect.New(t).Elem()
	if plain {
		err = assign(s.root, dst)
	} else {
		raw, e := codec.MarshalBounded(w.ctx, s.root, codec.EncodeLimits{MaxBytes: w.options.Limits.MaxUnits, MaxNodes: w.options.Limits.MaxUnits / NodeUnits, MaxDepth: w.options.Limits.MaxDepth, Account: w.scratch()})
		if e != nil {
			return zero, e
		}
		d := codec.NewValueDecoder(raw)
		err = d.Decode(dst.Addr().Interface())
	}
	if err != nil {
		return zero, err
	}
	if err = w.ctx.Err(); err != nil {
		return zero, err
	}
	return *dst.Addr().Interface().(*T), nil
}
func customDecode(t reflect.Type) bool {
	return t.Implements(unmarshalType) || t.Implements(textUnmarshalType) || reflect.PointerTo(t).Implements(unmarshalType) || reflect.PointerTo(t).Implements(textUnmarshalType)
}
func addEstimate(w *work, n *int64, extra int64) error {
	if extra < 0 || extra > w.options.Limits.MaxUnits-*n {
		return &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
	}
	*n += extra
	return nil
}
func estimate(w *work, src any, t reflect.Type, depth int) (int64, bool, error) {
	if err := w.depth(depth); err != nil {
		return 0, false, err
	}
	if t.Size() > uintptr(w.options.Limits.MaxUnits) {
		return 0, false, &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
	}
	n := int64(t.Size())
	if customDecode(t) {
		if src != nil && t.Kind() == reflect.Pointer {
			size := t.Elem().Size()
			if size > uintptr(w.options.Limits.MaxUnits) || addEstimate(w, &n, int64(size)) != nil {
				return 0, false, &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
			}
		}
		return max(NodeUnits, n), false, nil
	}
	if src == nil {
		return max(NodeUnits, n), true, nil
	}
	plain := true
	add := func(x int64) error { return addEstimate(w, &n, x) }
	child := func(x any, ct reflect.Type) error {
		m, p, e := estimate(w, x, ct, depth+1)
		plain = plain && p
		if e != nil {
			return e
		}
		return add(m)
	}
	switch t.Kind() {
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return n, false, errors.New("cannot construct a nonempty interface")
		}
		c, err := logicalCost(src)
		if err != nil {
			return 0, false, err
		}
		n = max(n, c)
	case reflect.Pointer:
		if err := child(src, t.Elem()); err != nil {
			return 0, false, err
		}
	case reflect.Struct:
		m, ok := src.(map[string]any)
		if !ok {
			return n, true, nil
		}
		for k, v := range m {
			f, ok := codec.LookupValueField(t, k)
			if !ok {
				continue
			}
			plain = plain && !f.Quoted
			ft := t
			for _, i := range f.Index {
				if ft.Kind() == reflect.Pointer {
					if err := add(int64(ft.Elem().Size())); err != nil {
						return 0, false, err
					}
					ft = ft.Elem()
				}
				ft = ft.Field(i).Type
			}
			if err := child(v, ft); err != nil {
				return 0, false, err
			}
		}
	case reflect.Map:
		m, ok := src.(map[string]any)
		if !ok {
			return n, true, nil
		}
		plain = plain && t.Key().Kind() == reflect.String && !customDecode(t.Key())
		for k, v := range m {
			// Conservative map backing/key/value slot estimate, including growth.
			if t.Key().Size() > uintptr(w.options.Limits.MaxUnits) || t.Elem().Size() > uintptr(w.options.Limits.MaxUnits) {
				return 0, false, &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
			}
			cost := int64(t.Key().Size()) + int64(t.Elem().Size()) + 64
			if cost > math.MaxInt64/2 {
				return 0, false, &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
			}
			if err := add(2*cost + int64(len(k))); err != nil {
				return 0, false, err
			}
			if err := child(v, t.Elem()); err != nil {
				return 0, false, err
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			switch v := src.(type) {
			case byteString:
				if err := add(int64(len(v))); err != nil {
					return 0, false, err
				}
				return max(NodeUnits, n), !customDecode(t.Elem()), nil
			case string:
				if err := add(int64(base64.StdEncoding.DecodedLen(len(v)))); err != nil {
					return 0, false, err
				}
				return max(NodeUnits, n), !customDecode(t.Elem()), nil
			}
		}
		a, ok := src.([]any)
		if !ok {
			return n, true, nil
		}
		count := len(a)
		if t.Kind() == reflect.Array {
			count = min(count, t.Len())
		}
		if t.Kind() == reflect.Slice {
			size := int64(t.Elem().Size())
			if size > w.options.Limits.MaxUnits || int64(count) > w.options.Limits.MaxUnits/max(1, size)/2 {
				return 0, false, &LimitError{w.stage, "construction", w.options.Limits.MaxUnits}
			}
			if err := add(2 * size * int64(count)); err != nil {
				return 0, false, err
			}
		}
		for _, v := range a[:count] {
			if err := child(v, t.Elem()); err != nil {
				return 0, false, err
			}
		}
	case reflect.String:
		switch v := src.(type) {
		case string:
			if err := add(int64(len(v))); err != nil {
				return 0, false, err
			}
		case byteString:
			if err := add((int64(len(v)) + 2) / 3 * 4); err != nil {
				return 0, false, err
			}
		case json.Number:
			if err := add(int64(len(v))); err != nil {
				return 0, false, err
			}
		}
	}
	return max(NodeUnits, n), plain, nil
}
func logicalCost(src any) (int64, error) {
	n := NodeUnits
	switch v := src.(type) {
	case string:
		n += codec.StringContentSize(v, false)
	case float64:
		b, _ := codec.Marshal(v)
		n += int64(len(b))
	case json.Number:
		n += int64(len(v))
	case byteString:
		n += (int64(len(v)) + 2) / 3 * 4
	case []any:
		for _, x := range v {
			c, e := logicalCost(x)
			if e != nil {
				return 0, e
			}
			if c > math.MaxInt64-n {
				return 0, errors.New("cost overflow")
			}
			n += c
		}
	case map[string]any:
		for k, x := range v {
			c, e := logicalCost(x)
			if e != nil {
				return 0, e
			}
			c += codec.StringContentSize(k, false)
			if c > math.MaxInt64-n {
				return 0, errors.New("cost overflow")
			}
			n += c
		}
	}
	return n, nil
}
func mismatch() error { return errors.New("logical value does not match destination type") }
func assign(src any, dst reflect.Value) error {
	if src == nil {
		return nil
	}
	if dst.Kind() == reflect.Pointer {
		dst.Set(reflect.New(dst.Type().Elem()))
		return assign(src, dst.Elem())
	}
	if dst.Kind() == reflect.Interface {
		x, err := cloneLogical(src)
		if err != nil {
			return err
		}
		if x != nil {
			dst.Set(reflect.ValueOf(x))
		}
		return nil
	}
	if dst.Type() == numberType {
		switch x := src.(type) {
		case json.Number:
			dst.SetString(string(x))
			return nil
		case string:
			if codec.ValidNumberToken(x) {
				dst.SetString(x)
				return nil
			}
		}
		return mismatch()
	}
	switch x := src.(type) {
	case float64:
		b, err := codec.Marshal(x)
		if err != nil {
			return err
		}
		return assign(json.Number(b), dst)
	case bool:
		if dst.Kind() != reflect.Bool {
			return mismatch()
		}
		dst.SetBool(x)
		return nil
	case byteString:
		if dst.Kind() == reflect.String {
			dst.SetString(base64.StdEncoding.EncodeToString(x))
			return nil
		}
		if dst.Kind() != reflect.Slice || dst.Type().Elem().Kind() != reflect.Uint8 {
			return mismatch()
		}
		dst.Set(reflect.MakeSlice(dst.Type(), len(x), len(x)))
		copy(dst.Bytes(), x)
		return nil
	case string:
		if dst.Kind() == reflect.String {
			dst.SetString(x)
			return nil
		}
		if dst.Kind() == reflect.Slice && dst.Type().Elem().Kind() == reflect.Uint8 {
			b, err := base64.StdEncoding.DecodeString(x)
			if err != nil {
				return mismatch()
			}
			out := reflect.MakeSlice(dst.Type(), len(b), len(b))
			copy(out.Bytes(), b)
			dst.Set(out)
			return nil
		}
		return mismatch()
	case json.Number:
		switch dst.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, e := strconv.ParseInt(string(x), 10, dst.Type().Bits())
			if e != nil {
				return mismatch()
			}
			dst.SetInt(n)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			n, e := strconv.ParseUint(string(x), 10, dst.Type().Bits())
			if e != nil {
				return mismatch()
			}
			dst.SetUint(n)
		case reflect.Float32, reflect.Float64:
			n, e := strconv.ParseFloat(string(x), dst.Type().Bits())
			if e != nil {
				return mismatch()
			}
			dst.SetFloat(n)
		default:
			return mismatch()
		}
		return nil
	case []any:
		if dst.Kind() != reflect.Slice && dst.Kind() != reflect.Array {
			return mismatch()
		}
		n := len(x)
		if dst.Kind() == reflect.Slice {
			dst.Set(reflect.MakeSlice(dst.Type(), n, n))
		} else {
			n = min(n, dst.Len())
		}
		for i, v := range x[:n] {
			if err := assign(v, dst.Index(i)); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		switch dst.Kind() {
		case reflect.Map:
			if dst.Type().Key().Kind() != reflect.String {
				return mismatch()
			}
			dst.Set(reflect.MakeMapWithSize(dst.Type(), len(x)))
			for k, v := range x {
				key := reflect.New(dst.Type().Key()).Elem()
				key.SetString(k)
				elem := reflect.New(dst.Type().Elem()).Elem()
				if err := assign(v, elem); err != nil {
					return err
				}
				dst.SetMapIndex(key, elem)
			}
			return nil
		case reflect.Struct:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				f, ok := codec.LookupValueField(dst.Type(), k)
				if !ok {
					continue
				}
				fv, ok := fieldValue(dst, f.Index, true)
				if !ok || !fv.CanSet() {
					return mismatch()
				}
				if err := assign(x[k], fv); err != nil {
					return err
				}
			}
			return nil
		}
		return mismatch()
	}
	return mismatch()
}
func cloneLogical(src any) (any, error) {
	switch x := src.(type) {
	case byteString:
		return base64.StdEncoding.EncodeToString(x), nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			z, e := cloneLogical(v)
			if e != nil {
				return nil, e
			}
			out[i] = z
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			z, e := cloneLogical(v)
			if e != nil {
				return nil, e
			}
			out[k] = z
		}
		return out, nil
	default:
		return src, nil
	}
}
