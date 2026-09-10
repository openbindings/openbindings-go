package jsonvalue

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// ValueSet owns exact JSON values in first-insertion order. Its private index
// only narrows comparisons; exact equality verifies every membership hit.
// Index keys are not exported, persisted, canonical JSON, or trust identities.
// Numeric indexing has the same operation budget as exact numeric predicates.
type ValueSet struct {
	buckets map[string][]any
	values  []any
	key     func(any) (string, error)
}

func NewValueSet(values []any) (*ValueSet, error) {
	s := &ValueSet{buckets: make(map[string][]any), key: valueIndexKey}
	for _, v := range values {
		if _, err := s.Add(v); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *ValueSet) Add(value any) (bool, error) {
	if s.key == nil {
		s.key = valueIndexKey
	}
	if s.buckets == nil {
		s.buckets = make(map[string][]any)
	}
	v, err := detached(value)
	if err != nil {
		return false, err
	}
	key, err := s.key(v)
	if err != nil {
		return false, err
	}
	for _, prior := range s.buckets[key] {
		same, err := equal(v, prior)
		if err != nil || same {
			return false, err
		}
	}
	s.buckets[key] = append(s.buckets[key], v)
	s.values = append(s.values, v)
	return true, nil
}

func (s *ValueSet) Contains(value any) (bool, error) {
	v, err := detached(value)
	if err != nil {
		return false, err
	}
	keyFn := s.key
	if keyFn == nil {
		keyFn = valueIndexKey
	}
	key, err := keyFn(v)
	if err != nil {
		return false, err
	}
	for _, prior := range s.buckets[key] {
		same, err := equal(v, prior)
		if err != nil || same {
			return same, err
		}
	}
	return false, nil
}

func (s *ValueSet) Len() int { return len(s.values) }
func (s *ValueSet) Values() []any {
	out := make([]any, len(s.values))
	for i, v := range s.values {
		copy, err := detached(v)
		if err != nil {
			panic("invalid owned JSON set value")
		}
		out[i] = copy
	}
	return out
}

func valueIndexKey(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case string:
		b, _ := json.Marshal(x)
		return "s" + string(b), nil
	case json.Number:
		// The standard integer parser is a fast path to the same big.Rat key.
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return "n" + strconv.FormatInt(n, 10), nil
		}
		n, err := rational(x)
		if err != nil {
			return "", err
		}
		return "n" + n.RatString(), nil
	case []any:
		var b strings.Builder
		b.WriteString("a")
		for _, v := range x {
			k, err := valueIndexKey(v)
			if err != nil {
				return "", err
			}
			b.WriteString(strconv.Itoa(len(k)))
			b.WriteByte(':')
			b.WriteString(k)
		}
		return b.String(), nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("o")
		for _, k := range keys {
			v, err := valueIndexKey(x[k])
			if err != nil {
				return "", err
			}
			b.WriteString(strconv.Itoa(len(k)))
			b.WriteByte(':')
			b.WriteString(k)
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
		}
		return b.String(), nil
	default:
		panic("non-JSON value in owned JSON set")
	}
}
