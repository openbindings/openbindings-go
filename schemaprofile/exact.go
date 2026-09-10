package schemaprofile

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// diagnosticValue is presentation, never identity. encoding/json retains
// json.Number tokens; HTML escaping is disabled to match the TS diagnostics.
func diagnosticValue(v any) (string, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(b.Bytes(), []byte{'\n'})), nil
}

func exactFailure(format string, values ...any) (bool, string, error) {
	rendered := make([]any, len(values))
	for i, v := range values {
		s, err := diagnosticValue(v)
		if err != nil {
			return false, "", err
		}
		rendered[i] = s
	}
	return false, fmt.Sprintf(format, rendered...), nil
}

func enumSet(schema map[string]any) (*jsonvalue.ValueSet, bool, error) {
	v, present := schema["enum"]
	if !present {
		return nil, false, nil
	}
	arr, _ := asSlice(v)
	out, err := jsonvalue.NewValueSet(arr)
	return out, true, err
}

func compatConstEnum(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	tc, th := tgt["const"]
	cc, ch := cand["const"]
	te, teh, err := enumSet(tgt)
	if err != nil {
		return false, "", err
	}
	ce, ceh, err := enumSet(cand)
	if err != nil {
		return false, "", err
	}
	constPair := func() (bool, string, error) {
		same, err := jsonvalue.Equal(tc, cc)
		if err != nil {
			return false, "", err
		}
		if same {
			return true, "", nil
		}
		return exactFailure("const: candidate const %s does not match target const %s", cc, tc)
	}
	member := func(set *jsonvalue.ValueSet, v any, format string) (bool, string, error) {
		same, err := set.Contains(v)
		if err != nil {
			return false, "", err
		}
		if same {
			return true, "", nil
		}
		return exactFailure(format, v)
	}
	if isInput {
		if th {
			if ch {
				return constPair()
			}
			if ceh {
				return member(ce, tc, "enum: target const %s not in candidate enum")
			}
			return true, "", nil
		}
		if teh {
			if ch {
				if te.Len() != 1 {
					return exactFailure("const: candidate const %s cannot cover %s target enum values", cc, te.Len())
				}
				return member(te, cc, "const: candidate const %s not in target enum")
			}
			if ceh {
				for _, v := range te.Values() {
					ok, reason, err := member(ce, v, "enum: target value %s not in candidate enum")
					if err != nil || !ok {
						return ok, reason, err
					}
				}
			}
		}
		return true, "", nil
	}
	if teh {
		if ch {
			return member(te, cc, "enum: candidate const %s not in target enum")
		}
		if ceh {
			for _, v := range ce.Values() {
				ok, reason, err := member(te, v, "enum: candidate value %s not in target enum")
				if err != nil || !ok {
					return ok, reason, err
				}
			}
			return true, "", nil
		}
		return false, "enum: candidate is unconstrained but target has enum", nil
	}
	if th {
		if ch {
			return constPair()
		}
		if ceh {
			if ce.Len() != 1 {
				return exactFailure("const: candidate enum has %s values but target allows only const %s", ce.Len(), tc)
			}
			ok, err := ce.Contains(tc)
			if err != nil {
				return false, "", err
			}
			if !ok {
				return exactFailure("const: candidate enum value does not match target const %s", tc)
			}
			return true, "", nil
		}
		return exactFailure("const: candidate is unconstrained but target requires const %s", tc)
	}
	return true, "", nil
}

type numericBound struct {
	value              any
	exclusive, present bool
}

func effectiveBound(schema map[string]any, lower bool) (numericBound, error) {
	key, exclusive := "maximum", "exclusiveMaximum"
	if lower {
		key, exclusive = "minimum", "exclusiveMinimum"
	}
	v, present := schema[key]
	e, ep := schema[exclusive]
	if !present {
		return numericBound{e, true, ep}, nil
	}
	if !ep {
		return numericBound{v, false, true}, nil
	}
	c, err := jsonvalue.CompareNumbers(e, v)
	if err != nil {
		return numericBound{}, err
	}
	if lower && c >= 0 || !lower && c <= 0 {
		return numericBound{e, true, true}, nil
	}
	return numericBound{v, false, true}, nil
}
func (b numericBound) text() (string, error) {
	s, err := diagnosticValue(b.value)
	if b.exclusive {
		s = "exclusive " + s
	}
	return s, err
}

func compatNumericBounds(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	for _, lower := range []bool{true, false} {
		t, err := effectiveBound(tgt, lower)
		if err != nil {
			return false, "", err
		}
		c, err := effectiveBound(cand, lower)
		if err != nil {
			return false, "", err
		}
		key, greater, less := "maximum", "less", "greater"
		if lower {
			key, greater, less = "minimum", "greater", "less"
		}
		if !t.present {
			continue
		}
		if !c.present {
			if isInput {
				continue
			}
			text, err := t.text()
			if err != nil {
				return false, "", err
			}
			return false, fmt.Sprintf("%s: target has %s %s but candidate has none", key, key, text), nil
		}
		order, err := jsonvalue.CompareNumbers(c.value, t.value)
		if err != nil {
			return false, "", err
		}
		// Positive means candidate is stricter, regardless of lower/upper bound.
		if !lower {
			order = -order
		}
		if order == 0 {
			if c.exclusive && !t.exclusive {
				order = 1
			}
			if !c.exclusive && t.exclusive {
				order = -1
			}
		}
		if isInput && order > 0 || !isInput && order < 0 {
			ct, err := c.text()
			if err != nil {
				return false, "", err
			}
			tt, err := t.text()
			if err != nil {
				return false, "", err
			}
			relation := greater
			if !isInput {
				relation = less
			}
			return false, fmt.Sprintf("%s: candidate %s %s is %s than target %s %s", key, key, ct, relation, key, tt), nil
		}
	}
	return true, "", nil
}

func compatSimpleBounds(tgt, cand map[string]any, isInput bool, minKey, maxKey string) (bool, string, error) {
	for _, key := range []string{minKey, maxKey} {
		t, tp := tgt[key]
		c, cp := cand[key]
		if !tp {
			continue
		}
		if !cp {
			if isInput {
				continue
			}
			s, err := diagnosticValue(t)
			return false, fmt.Sprintf("%s: target has %s %s but candidate has none", key, key, s), err
		}
		cmp, err := jsonvalue.CompareNumbers(c, t)
		if err != nil {
			return false, "", err
		}
		lower := key == minKey
		bad := (isInput == lower) && cmp > 0 || (isInput != lower) && cmp < 0
		if bad {
			relation := "less"
			if cmp > 0 {
				relation = "greater"
			}
			ct, err := diagnosticValue(c)
			if err != nil {
				return false, "", err
			}
			tt, err := diagnosticValue(t)
			if err != nil {
				return false, "", err
			}
			return false, fmt.Sprintf("%s: candidate %s %s is %s than target %s %s", key, key, ct, relation, key, tt), nil
		}
	}
	return true, "", nil
}
