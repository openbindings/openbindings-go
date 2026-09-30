package schemaeval

import (
	"errors"
	"math/big"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// kindPrinter renders the library's error kinds.
var kindPrinter = message.NewPrinter(language.English)

// problems projects the library's error tree onto the evaluator contract's
// problems: each failing keyword once per instance location it fails at, at
// the location of the deepest failing keyword on its path. In-place and child
// applicators pass their subschemas' problems through; a failing false under
// additionalProperties is one problem per member; an anyOf, or a oneOf no
// alternative satisfies, is one problem at its own location, stating what
// each alternative lacked; propertyNames, contains, and not are located at
// the value they apply to.
func problems(ve *jsonschema.ValidationError, standsFor map[string]string) []openbindings.SchemaProblem {
	at := pointerOf(ve.InstanceLocation...)
	switch k := ve.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		if len(ve.Causes) > 0 {
			var alternatives []string
			for _, cause := range ve.Causes {
				for _, p := range problems(cause, standsFor) {
					alternatives = append(alternatives, relative(at, p))
				}
			}
			slices.Sort(alternatives)
			return []openbindings.SchemaProblem{{InstanceLocation: at, Message: "satisfies none of the alternatives: " + joinMessages(alternatives)}}
		}
	case *kind.PropertyNames:
		var messages []string
		for _, cause := range ve.Causes {
			for _, p := range problems(cause, standsFor) {
				messages = append(messages, p.Message)
			}
		}
		if len(messages) == 0 {
			messages = []string{ve.ErrorKind.LocalizedString(kindPrinter)}
		}
		slices.Sort(messages)
		return []openbindings.SchemaProblem{{InstanceLocation: at, Message: "the member name " + quote(k.Property) + " is invalid: " + joinMessages(messages)}}
	case *kind.Format:
		if strings.HasPrefix(k.Want, "schemaeval-bound-") {
			return []openbindings.SchemaProblem{{InstanceLocation: at, Message: k.Err.Error()}}
		}
	case *kind.Contains, *kind.MinContains, *kind.MaxContains:
		// Located at the array, whatever its items did.
		return []openbindings.SchemaProblem{{InstanceLocation: at, Message: ve.ErrorKind.LocalizedString(kindPrinter)}}
	case *kind.AdditionalProperties:
		var out []openbindings.SchemaProblem
		for _, name := range slices.Sorted(slices.Values(k.Properties)) {
			out = append(out, openbindings.SchemaProblem{InstanceLocation: pointerOf(append(slices.Clip(ve.InstanceLocation), name)...), Message: "additional property not allowed"})
		}
		return out
	}
	if got, want, compared := comparison(ve.ErrorKind); compared {
		if number, standIn := standsFor[got.RatString()]; standIn {
			// The library compared the stand-in; the problem states the
			// number.
			limit, _ := want.Float64()
			return []openbindings.SchemaProblem{{InstanceLocation: at, Message: kindPrinter.Sprintf("%s: got %s, want %v", ve.ErrorKind.KeywordPath()[0], number, limit)}}
		}
	}
	if len(ve.Causes) == 0 {
		return []openbindings.SchemaProblem{{InstanceLocation: at, Message: ve.ErrorKind.LocalizedString(kindPrinter)}}
	}
	var out []openbindings.SchemaProblem
	names := -1
	for _, cause := range ve.Causes {
		found := problems(cause, standsFor)
		if _, isNames := cause.ErrorKind.(*kind.PropertyNames); isNames && len(found) == 1 {
			// The library reports propertyNames once per invalid name; the
			// keyword fails once, at the object.
			if names >= 0 && out[names].InstanceLocation == found[0].InstanceLocation {
				out[names].Message += "; " + found[0].Message
				continue
			}
			names = len(out)
		}
		out = append(out, found...)
	}
	return out
}

// refCycle reports whether the library met a cycle of references it could
// not evaluate, which is no mismatch.
func refCycle(err error) bool {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	var found func(ve *jsonschema.ValidationError) bool
	found = func(ve *jsonschema.ValidationError) bool {
		if _, ok := ve.ErrorKind.(*kind.RefCycle); ok {
			return true
		}
		return slices.ContainsFunc(ve.Causes, found)
	}
	return found(ve)
}

// comparison returns the numbers a comparison failed on: the value's and the
// schema's.
func comparison(k jsonschema.ErrorKind) (got, want *big.Rat, compared bool) {
	switch k := k.(type) {
	case *kind.Minimum:
		return k.Got, k.Want, true
	case *kind.Maximum:
		return k.Got, k.Want, true
	case *kind.ExclusiveMinimum:
		return k.Got, k.Want, true
	case *kind.ExclusiveMaximum:
		return k.Got, k.Want, true
	case *kind.MultipleOf:
		return k.Got, k.Want, true
	}
	return nil, nil, false
}

// relative renders a problem found under base, naming its location relative
// to base when it lies deeper.
func relative(base string, p openbindings.SchemaProblem) string {
	if len(p.InstanceLocation) <= len(base) {
		return p.Message
	}
	return p.InstanceLocation[len(base):] + ": " + p.Message
}

func joinMessages(messages []string) string {
	out := ""
	for i, m := range messages {
		if i > 0 {
			out += "; "
		}
		out += m
	}
	return out
}

func quote(s string) string { return kindPrinter.Sprintf("%q", s) }
