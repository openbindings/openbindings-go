package openapi

// Conformant YAML scalar resolution for one artifact resource.
//
// `openbindings.openapi@1` §3 pins the string-content grammar to YAML 1.2.2
// and resolves untagged scalars by "[YAML 1.2.2 §10.3.2]'s core tag
// resolution", constrained by every accepted OAS edition's §4.2 sentence
// "Tags MUST be limited to those allowed by [YAML's] JSON schema ruleset".
// The patterns below are transcribed from the pinned authority bytes
// (corpus-lab/authorities/texts/openapi/yaml/yaml-1.2.2.html, SHA-256
// 2fe1bf40792df22ef8b8a5972a34c48e639969222e0bbae605aa9bb2ca85e9ab, the
// `<h3 id="1032-tag-resolution">` anchor at byte 370,122).
//
// The decode this engine ships is kin-openapi's, over `github.com/oasdiff/yaml`
// and its `yaml3` fork of go-yaml, and that resolver is not §10.3.2's. It
// reads a leading zero as YAML 1.1 octal (`017` is 15, not 17), strips `_`
// separators (`1_000` is 1000, not the string), accepts `0b` binary (`0b101`
// is 5, not the string), merges `<<` keys, and resolves explicit
// `!!timestamp` / `!!binary` / `!!merge` / `!!set` tags the OAS forbids.
// `oasdiff/yaml`'s DecodeOpts exposes only DisableTimestamps, so none of that
// is reachable through configuration.
//
// This file is therefore the family's own YAML-to-JSON layer, scoped to
// scalar RESOLUTION alone: structure, anchors and aliases still come from the
// upstream parser's node tree, and everything downstream of this seam —
// kin-openapi's structure handling, reference resolution, reference metadata
// and diagnostics — is untouched, in the same spirit as the ReadFromURIFunc
// pruning seam. The tree it produces is compared against the incumbent one by
// the caller, and it replaces the incumbent ONLY where the two disagree, so a
// resource whose scalars the incumbent already resolves conformantly is
// handed on byte-identically.
//
// openbindings-go/formats/openapi/yaml_scalars.go and
// openapi-client/go/yaml_scalars.go are this same file, byte-identical apart
// from the package clause, and the shared case table is what proves the two
// engines and the TypeScript one decide alike.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"

	yaml3 "github.com/oasdiff/yaml3"
)

// errScalarResolutionUnavailable reports that this layer cannot speak for the
// resource — the node tree did not parse, or it carries a shape outside the
// JSON domain that the incumbent decoder rejects on its own. It is not a
// refusal: the caller keeps the incumbent tree and the incumbent diagnostic.
var errScalarResolutionUnavailable = errors.New("openapi: conformant scalar resolution unavailable for this resource")

// §10.3.2's regular-expression table, one variable per row. `null | Null |
// NULL | ~` and the empty-scalar row are spelled out in resolvePlainScalar
// rather than as a pattern, because an alternation with an empty branch reads
// worse than the four constants it stands for.
var (
	yamlBooleanPattern       = regexp.MustCompile(`^(true|True|TRUE|false|False|FALSE)$`)
	yamlIntegerBase10Pattern = regexp.MustCompile(`^[-+]?[0-9]+$`)
	yamlIntegerBase8Pattern  = regexp.MustCompile(`^0o[0-7]+$`)
	yamlIntegerBase16Pattern = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	yamlFloatNumberPattern   = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	yamlFloatInfinityPattern = regexp.MustCompile(`^[-+]?\.(inf|Inf|INF)$`)
	yamlFloatNaNPattern      = regexp.MustCompile(`^\.(nan|NaN|NAN)$`)
)

// yamlQuotedStyles marks the scalar styles that carry a string by
// presentation. YAML 1.2.2 §10.3.2 resolves only PLAIN scalars by the
// pattern table; a quoted, literal or folded scalar is a string whatever it
// spells.
const yamlQuotedStyles = yaml3.SingleQuotedStyle | yaml3.DoubleQuotedStyle | yaml3.LiteralStyle | yaml3.FoldedStyle

// resolveScalarsConformantly reads one resource's node tree and returns the
// JSON-domain value its scalars denote under §10.3.2.
//
// Numbers are float64 throughout, which is the domain the incumbent path also
// lands in: `oasdiff/yaml` marshals its decoded tree to JSON and unmarshals it
// back, so an integer arrives as a float64 there too. Matching that exactly is
// what lets the caller compare the two trees for equality instead of guessing
// which spellings might have diverged.
func resolveScalarsConformantly(data []byte) (any, error) {
	var document yaml3.Node
	if err := yaml3.Unmarshal(data, &document); err != nil {
		return nil, errScalarResolutionUnavailable
	}
	if document.Kind == 0 {
		// An empty stream. The incumbent path decodes this to a nil tree.
		return nil, nil
	}
	return resolveNodeConformantly(&document, 0)
}

func resolveNodeConformantly(node *yaml3.Node, depth int) (any, error) {
	// An alias cycle would recur forever here. The incumbent decoder refuses
	// such a document on its own, so falling back is the honest outcome.
	if depth > 512 {
		return nil, errScalarResolutionUnavailable
	}
	switch node.Kind {
	case yaml3.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return resolveNodeConformantly(node.Content[0], depth+1)
	case yaml3.AliasNode:
		if node.Alias == nil {
			return nil, errScalarResolutionUnavailable
		}
		return resolveNodeConformantly(node.Alias, depth+1)
	case yaml3.SequenceNode:
		if err := checkCollectionTag(node, "!!seq"); err != nil {
			return nil, err
		}
		items := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			item, err := resolveNodeConformantly(child, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case yaml3.MappingNode:
		if err := checkCollectionTag(node, "!!map"); err != nil {
			return nil, err
		}
		// No merge-key handling, deliberately. §10.3.2 resolves the plain
		// scalar `<<` to a string like any other unmatched spelling, and §3
		// names merge among the YAML 1.1 types outside the permitted set, so
		// `<<` is an ordinary property name here.
		entries := make(map[string]any, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, err := resolveNodeConformantly(node.Content[index], depth+1)
			if err != nil {
				return nil, err
			}
			name, err := jsonObjectKey(key)
			if err != nil {
				return nil, err
			}
			value, err := resolveNodeConformantly(node.Content[index+1], depth+1)
			if err != nil {
				return nil, err
			}
			entries[name] = value
		}
		return entries, nil
	case yaml3.ScalarNode:
		return resolveScalarConformantly(node)
	}
	return nil, errScalarResolutionUnavailable
}

// checkCollectionTag admits a collection carrying no explicit tag — §10.3.2
// resolves an untagged collection by its kind — and, where one is present,
// admits only the failsafe tag for that kind.
func checkCollectionTag(node *yaml3.Node, kindTag string) error {
	if node.Style&yaml3.TaggedStyle == 0 || node.Tag == kindTag {
		return nil
	}
	return unpermittedTagError(node)
}

func resolveScalarConformantly(node *yaml3.Node) (any, error) {
	if node.Style&yaml3.TaggedStyle != 0 {
		return resolveTaggedScalar(node)
	}
	if node.Style&yamlQuotedStyles != 0 {
		return node.Value, nil
	}
	return resolvePlainScalar(node)
}

// resolveTaggedScalar constructs a scalar whose tag the source states. The
// permitted set is YAML 1.2.2 §10.2.1's null, boolean, integer and float
// alongside the failsafe string; anything else is refused rather than
// resolved, which is the OAS §4.2 restriction §3 restates.
func resolveTaggedScalar(node *yaml3.Node) (any, error) {
	switch node.Tag {
	case "!!str":
		return node.Value, nil
	case "!!null":
		if !isPlainNullSpelling(node.Value) {
			return nil, unresolvableTagError(node)
		}
		return nil, nil
	case "!!bool":
		if !yamlBooleanPattern.MatchString(node.Value) {
			return nil, unresolvableTagError(node)
		}
		return node.Value[0] == 't' || node.Value[0] == 'T', nil
	case "!!int":
		return taggedIntegerValue(node)
	case "!!float":
		return taggedFloatValue(node)
	}
	return nil, unpermittedTagError(node)
}

func taggedIntegerValue(node *yaml3.Node) (any, error) {
	switch {
	case yamlIntegerBase10Pattern.MatchString(node.Value):
		return decimalNumberValue(node, node.Value)
	case yamlIntegerBase8Pattern.MatchString(node.Value):
		return radixNumberValue(node, node.Value[2:], 8)
	case yamlIntegerBase16Pattern.MatchString(node.Value):
		return radixNumberValue(node, node.Value[2:], 16)
	}
	return nil, unresolvableTagError(node)
}

func taggedFloatValue(node *yaml3.Node) (any, error) {
	switch {
	case yamlFloatNaNPattern.MatchString(node.Value), yamlFloatInfinityPattern.MatchString(node.Value):
		return nil, noJSONImageError(node)
	case yamlFloatNumberPattern.MatchString(node.Value):
		return decimalNumberValue(node, node.Value)
	}
	return nil, unresolvableTagError(node)
}

// resolvePlainScalar is §10.3.2's table, in the authority's own row order.
func resolvePlainScalar(node *yaml3.Node) (any, error) {
	text := node.Value
	switch {
	case isPlainNullSpelling(text):
		return nil, nil
	case yamlBooleanPattern.MatchString(text):
		return text[0] == 't' || text[0] == 'T', nil
	case yamlIntegerBase10Pattern.MatchString(text):
		// Base 10 keeps every leading zero as a decimal digit: `017` is 17.
		// Only the `0o` row is octal in this table, and its row carries no
		// sign, so `-0o17` matches nothing and is a string.
		return decimalNumberValue(node, text)
	case yamlIntegerBase8Pattern.MatchString(text):
		return radixNumberValue(node, text[2:], 8)
	case yamlIntegerBase16Pattern.MatchString(text):
		return radixNumberValue(node, text[2:], 16)
	case yamlFloatInfinityPattern.MatchString(text), yamlFloatNaNPattern.MatchString(text):
		return nil, noJSONImageError(node)
	case yamlFloatNumberPattern.MatchString(text):
		return decimalNumberValue(node, text)
	}
	// "if none of the regular expressions matches, the scalar is resolved to
	// tag:yaml.org,2002:str". `1_000`, `0b101`, `0x_1F`, `<<`, `yes`, and
	// every date- and time-shaped spelling land here.
	return text, nil
}

func isPlainNullSpelling(text string) bool {
	return text == "" || text == "~" || text == "null" || text == "Null" || text == "NULL"
}

// An out-of-domain magnitude keeps its source text, DELIBERATELY, in EVERY
// numeric row — base 10, base 8, base 16 and the float row alike — because
// the class is a magnitude, not a spelling. Its members are `1e400` and the
// corpus's real `600e27371700` on the float row, `1` followed by 309 zeros
// on the base-10 row, `0x` followed by 256 `F` on the base-16 row, and `0o`
// followed by 342 sevens on the base-8 row. This is the one place this layer
// does not follow §10.3.2's table, and it is decided identically by
// decimalNumberValue and radixNumberValue so no row can drift away from the
// others.
//
// §10.3.2 resolves each of those spellings to a number. What a processor then
// does with one it cannot hold is not §10.3.2's to say, and the authority says
// so twice, once per tag. §10.2.1.4 (float) states that "The supported range
// and accuracy depends on the implementation, though 32 bit IEEE floats should
// be safe"; §10.2.1.3 (integer) states that "an integer may overflow the
// native type's storage capability. A YAML processor may reject such a value
// as an error, truncate it with a warning or find some other manner to
// round-trip it." Refusing the artifact, carrying ±Inf, and clamping to the
// nearest representable double are therefore all inside what the authority
// permits — a choice, not a deduction. §3's no-JSON-image refusal does not
// reach the class either: unlike `.inf` and `.nan`, which JSON cannot spell at
// all, `6e27371702` is an ordinary JSON number this number type cannot hold.
//
// So the choice is not made here. Every engine spells such a scalar as its own
// source text, and the question is filed as F-O1-15. Every row it spans is
// pinned by name in the shared case table, in both directions of each
// boundary, so no engine can decide it by side effect. Underflow is NOT in
// this class: `1e-400` becomes 0, which is §10.2.1.4's "a float value may
// change by 'a small amount' when round-tripped".

// decimalNumberValue carries §10.3.2's base-10 integer row and its float row,
// which land in the same double domain once the tree is JSON.
func decimalNumberValue(node *yaml3.Node, text string) (any, error) {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, errScalarResolutionUnavailable
	}
	if math.IsNaN(value) {
		return nil, noJSONImageError(node)
	}
	if math.IsInf(value, 0) {
		return text, nil
	}
	return value, nil
}

func radixNumberValue(node *yaml3.Node, digits string, base int) (any, error) {
	magnitude, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return nil, errScalarResolutionUnavailable
	}
	value, _ := new(big.Float).SetInt(magnitude).Float64()
	if math.IsInf(value, 0) {
		// The same F-O1-15 outcome as decimalNumberValue's, for the base-8
		// and base-16 rows: the source text, not ±Inf and not a clamp.
		return node.Value, nil
	}
	return value, nil
}

// jsonObjectKey renders a resolved key in the JSON object-name domain, which
// is where an OpenAPI document's `200:` and `2020-01-01:` keys already live.
// It reproduces the incumbent path's own key rendering so that a document
// whose scalars resolve conformantly compares equal.
func jsonObjectKey(key any) (string, error) {
	switch typed := key.(type) {
	case string:
		return typed, nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	}
	// A null or collection key has no JSON object name; the incumbent decoder
	// rejects the document for the same reason, and owns that diagnostic.
	return "", errScalarResolutionUnavailable
}

func unpermittedTagError(node *yaml3.Node) error {
	return fmt.Errorf(
		"OpenAPI document scalar at line %d, column %d carries the tag %s, which is outside the tag set every accepted OpenAPI edition permits (\"Tags MUST be limited to those allowed by [YAML's] JSON schema ruleset\", OAS §4.2): only !!null, !!bool, !!int, !!float, !!str, !!seq and !!map are resolvable here",
		node.Line, node.Column, node.Tag,
	)
}

func unresolvableTagError(node *yaml3.Node) error {
	return fmt.Errorf(
		"OpenAPI document value %q at line %d, column %d cannot be resolved as %s under YAML 1.2.2 §10.3.2",
		node.Value, node.Line, node.Column, node.Tag,
	)
}

func noJSONImageError(node *yaml3.Node) error {
	return fmt.Errorf(
		"OpenAPI document value %q at line %d, column %d resolves to a float with no JSON representation; the OpenAPI document model is JSON and the YAML spelling exists to round-trip with it",
		node.Value, node.Line, node.Column,
	)
}
