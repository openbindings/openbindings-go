package openbindings

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// The typed model decodes an OBI-defined object only if re-encoding it
// reproduces every member. Each object type decodes through decodeObject and
// encodes through encodeObject, which read its typed members from the Go
// struct: a member's JSON name is its field's json tag, and what the member
// can carry follows from the field's Go type.
//
//   - A string field is a required member; its zero value would re-encode as
//     a present empty string, so a missing member fails decoding.
//   - A json.RawMessage field carries any JSON value exactly, null included.
//   - Every other field refuses null, which a nil pointer, an absent schema,
//     or a Go zero value would re-encode as absence or as a different value.
//     String collections also refuse null elements.
//   - A *int64 field holds an integer in the §5.3 preference range, in any
//     number spelling.
//
// Decoding also refuses input the model's Go values cannot carry: invalid
// UTF-8, and a duplicate member name in any object. Members are matched by
// exact name, never case-folded. (An escaped lone UTF-16 surrogate is carried:
// the SDK's codec keeps it in a Go string as WTF-8 and escapes it again.)

// LosslessFields is embedded in every OBI-defined object type to carry the
// members its typed fields do not: Extensions holds `x-` members (§12) and
// Unknown every other one (OBI-T-02). Decoding fills both; encoding writes
// them back beside the typed members.
//
// An entry whose name is a typed member's name is never encoded: the typed
// field alone states that member, so a nil field is absent whatever these maps
// hold.
type LosslessFields struct {
	// Extensions holds `x-` members.
	Extensions map[string]json.RawMessage `json:"-"`

	// Unknown holds every other member the type does not model.
	Unknown map[string]json.RawMessage `json:"-"`
}

func (l *LosslessFields) setLossless(extensions, unknown map[string]json.RawMessage) {
	l.Extensions, l.Unknown = extensions, unknown
}

// losslessObject is a pointer to an OBI-defined object type's method-less
// counterpart, which decodeObject fills.
type losslessObject interface {
	setLossless(extensions, unknown map[string]json.RawMessage)
}

// memberClass is what a typed member can carry, from its field's Go type.
type memberClass int

const (
	memberValue     memberClass = iota // refuses null
	memberRequired                     // a string: required, refuses null
	memberRaw                          // json.RawMessage: any value, null included
	memberStrings                      // []string or map[string]string: no null elements
	memberInteger                      // *int64: an exact preference-range integer
	memberTransform                    // TransformOrRef
)

type memberField struct {
	name  string
	index int
	class memberClass
}

type memberTable struct {
	fields []memberField
	typed  map[string]bool
}

var (
	memberTables      sync.Map // reflect.Type -> *memberTable
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
	int64PointerType  = reflect.TypeFor[*int64]()
	transformType     = reflect.TypeFor[TransformOrRef]()
	errNotJSONObject  = errors.New("not a JSON object")
	errNullJSONObject = errors.New("null is not an object")
)

// membersOf returns the typed members of an OBI-defined object type's
// method-less counterpart, in field order.
func membersOf(t reflect.Type) *memberTable {
	if cached, ok := memberTables.Load(t); ok {
		return cached.(*memberTable)
	}
	table := &memberTable{typed: map[string]bool{}}
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Anonymous || name == "" || name == "-" {
			continue
		}
		table.fields = append(table.fields, memberField{name: name, index: i, class: classifyMember(field.Type)})
		table.typed[name] = true
	}
	memberTables.Store(t, table)
	return table
}

func classifyMember(t reflect.Type) memberClass {
	switch {
	case t == rawMessageType:
		return memberRaw
	case t.Kind() == reflect.String:
		return memberRequired
	case t == int64PointerType:
		return memberInteger
	case t == transformType:
		return memberTransform
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Map) && t.Elem().Kind() == reflect.String:
		return memberStrings
	default:
		return memberValue
	}
}

// decodeObject decodes the OBI-defined object b into target, replacing its
// contents. what names the object in errors.
func decodeObject(b []byte, what string, target losslessObject) error {
	members, err := exactMembers(b)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	value := reflect.ValueOf(target).Elem()
	value.SetZero()
	table := membersOf(value.Type())
	for _, field := range table.fields {
		raw, present := members[field.name]
		if !present {
			if field.class == memberRequired {
				return fmt.Errorf("%s: missing required member %q", what, field.name)
			}
			continue
		}
		if err := decodeMember(raw, field.class, value.Field(field.index)); err != nil {
			return fmt.Errorf("%s: member %q: %w", what, field.name, err)
		}
	}
	var extensions, unknown map[string]json.RawMessage
	for name, raw := range members {
		switch {
		case table.typed[name]:
		case strings.HasPrefix(name, "x-"):
			if extensions == nil {
				extensions = map[string]json.RawMessage{}
			}
			extensions[name] = raw
		default:
			if unknown == nil {
				unknown = map[string]json.RawMessage{}
			}
			unknown[name] = raw
		}
	}
	target.setLossless(extensions, unknown)
	return nil
}

func decodeMember(raw json.RawMessage, class memberClass, field reflect.Value) error {
	if class == memberRaw {
		field.Set(reflect.ValueOf(raw))
		return nil
	}
	if isJSONNull(raw) {
		return errors.New("null, which this model cannot carry")
	}
	switch class {
	case memberInteger:
		value, err := exactPreference(raw)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(&value))
		return nil
	case memberTransform:
		transform, err := decodeTransform(raw)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(&transform).Elem())
		return nil
	case memberStrings:
		if err := rejectNullElements(raw); err != nil {
			return err
		}
	}
	return jsonvalue.Unmarshal(raw, field.Addr().Interface())
}

// exactPreference decodes a preference exactly: a JSON number denoting an
// integer in the §5.3 range, in any spelling (1, 1.0, 1e3). A string, or a
// number the model cannot carry exactly, fails decoding.
func exactPreference(raw json.RawMessage) (int64, error) {
	token := string(bytes.TrimSpace(raw))
	if !json.ValidNumberToken(token) {
		return 0, fmt.Errorf("%s is not a JSON number", token)
	}
	value, ok := new(big.Rat).SetString(token)
	if !ok || !value.IsInt() || value.Num().CmpAbs(big.NewInt(maxPreference)) > 0 {
		return 0, fmt.Errorf("%s is not an integer from -%d through %d", token, maxPreference, maxPreference)
	}
	return value.Num().Int64(), nil
}

// encodeObject encodes an OBI-defined object: typed, the method-less
// counterpart of its type, and the members its lossless fields carry.
func encodeObject(typed any, lossless LosslessFields) ([]byte, error) {
	data, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	if len(lossless.Extensions) == 0 && len(lossless.Unknown) == 0 {
		return data, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	typedNames := membersOf(reflect.TypeOf(typed)).typed
	for _, carried := range []map[string]json.RawMessage{lossless.Unknown, lossless.Extensions} {
		for name, raw := range carried {
			if !typedNames[name] {
				members[name] = raw
			}
		}
	}
	return json.Marshal(members)
}

// exactMembers splits a JSON object into its members by exact name, refusing
// input the model's Go values cannot carry exactly.
func exactMembers(b []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("not valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(b); err != nil {
		return nil, err
	}
	entries, err := splitObject(b)
	if err != nil {
		return nil, err
	}
	members := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		members[entry.name] = entry.value
	}
	return members, nil
}

type objectEntry struct {
	name  string
	value json.RawMessage
}

// splitObject splits a JSON object into its entries in document order, with a
// copy of each value's bytes.
func splitObject(b []byte) ([]objectEntry, error) {
	if !json.Valid(b) {
		return nil, errors.New("not valid JSON")
	}
	i := skipJSONSpace(b, 0)
	if b[i] != '{' {
		if isJSONNull(b) {
			return nil, errNullJSONObject
		}
		return nil, errNotJSONObject
	}
	var entries []objectEntry
	i = skipJSONSpace(b, i+1)
	for b[i] != '}' {
		nameEnd := jsonValueEnd(b, i)
		var name string
		if err := json.Unmarshal(b[i:nameEnd], &name); err != nil {
			return nil, err
		}
		start := skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1) // past the colon
		end := jsonValueEnd(b, start)
		entries = append(entries, objectEntry{
			name:  name,
			value: append(json.RawMessage(nil), b[start:end]...),
		})
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
// for b known to be valid JSON.
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

// rejectNullElements refuses a null element in an array or object of
// strings, which a Go string would re-encode as "". A value of another JSON
// type is left to the typed decode to reject.
func rejectNullElements(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(trimmed, &elements); err != nil {
			return err
		}
		for i, element := range elements {
			if isJSONNull(element) {
				return fmt.Errorf("element %d is null, which this model cannot carry", i)
			}
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		entries, err := splitObject(trimmed)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if isJSONNull(entry.value) {
				return fmt.Errorf("entry %q is null, which this model cannot carry", entry.name)
			}
		}
	}
	return nil
}
