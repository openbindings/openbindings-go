package openapi

import (
	"fmt"
	"strings"
)

// Reference traversal: what a `$ref` fragment means when its own path runs
// BELOW another reference.
//
// The shape. A library declares an alias:
//
//	components: {schemas: {Alias: {$ref: "#/components/schemas/Real"},
//	                       Real: {type: object, properties: {name: {type: string}}}}}
//
// and something writes `$ref: "./library.yaml#/components/schemas/Alias/properties/name"`.
// Read against the document's literal contents that pointer dies at `properties`:
// `Alias` is an object with exactly one member and it is not `properties`. Read
// against a document in which references have already been substituted for
// their targets, the same pointer walks through `Alias` into `Real` and lands on
// `{type: string}`.
//
// THE ANSWER IS THE INCORPORATED AUTHORITY'S, AND IT DIFFERS BY EDITION LINE.
// The registered OpenAPI family siblings' §2 accepts eight editions "each interpreted under its
// own immutable official text", so the two lines are read separately rather than
// averaged. Every quotation below is verified at the pinned bytes by
// `corpus-lab/scripts/verify-pointer-below-reference-authorities.mjs`, which
// publishes the digest of each edition's stripped rendering and the per-edition
// count of every sentence it relies on.
//
//	OAS 3.0.0-3.0.4  -> FOLLOW. §4.6 "Relative References in URLs": "Relative
//	                    references used in $ref are processed as per JSON
//	                    Reference, using the URL of the current document as the
//	                    base URI." (1 occurrence in each of the five; 0 in the
//	                    3.1 line.) The Reference Object says it a second time:
//	                    "The Reference Object is defined by JSON Reference and
//	                    follows the same structure, behavior and rules", and
//	                    "For this specification, reference resolution is
//	                    accomplished as defined by the JSON Reference
//	                    specification and not by the JSON Schema specification."
//	                    JSON Reference (draft-pbryan-zyp-json-ref-03) §1 states
//	                    its own basis as transclusion, "the use of a target
//	                    resource as an effective substitute for the reference";
//	                    §3 "Any members other than "$ref" in a JSON Reference
//	                    object SHALL be ignored", so such a node has no other
//	                    denotable members at all; §4 "Resolution of a JSON
//	                    Reference object SHOULD yield the referenced JSON value.
//	                    Implementations MAY choose to replace the reference with
//	                    the referenced value." Following is deference rung 1.
//
//	OAS 3.1.0-3.1.2  -> REFUSE. §4.6 "Relative References in URIs": "If a URI
//	                    contains a fragment identifier, then the fragment should
//	                    be resolved per the fragment resolution mechanism of the
//	                    referenced document. If the representation of the
//	                    referenced document is JSON or YAML, then the fragment
//	                    identifier SHOULD be interpreted as a JSON-Pointer as
//	                    per [RFC6901]." (1 occurrence in each of the three; 0 in
//	                    all five 3.0 editions. The spelling is the HYPHENATED
//	                    `JSON-Pointer`, which is why an earlier audit searching
//	                    the spaced form read silence here.) The fragment is
//	                    therefore an RFC 6901 pointer over the REFERENCED
//	                    DOCUMENT, and the 3.1 line's Schema Object "is a superset
//	                    of the JSON Schema Specification Draft 2020-12", whose
//	                    §8.2.3.1 makes `$ref` "an applicator that is used to
//	                    reference a statically identified schema. Its results are
//	                    the results of the referenced schema", with the authors'
//	                    own note that "other keywords can appear alongside of
//	                    "$ref" in the same schema object". 2020-12 substitutes
//	                    nothing: the referencing node keeps its members, the next
//	                    token names none of them, and RFC 6901 §4's error
//	                    condition arises — "Implementations will evaluate each
//	                    reference token against the document's contents and will
//	                    raise an error condition if it fails to resolve a
//	                    concrete value for any of the JSON pointer's reference
//	                    tokens." No 3.1 edition mentions JSON Reference at all
//	                    (0 occurrences in 3.1.1 and 3.1.2; the single 3.1.0 hit
//	                    is an unrelated `operationRef` escaping note), and no
//	                    accepted edition anywhere uses the word "transclusion" or
//	                    carries a replacement permission of its own.
//
// The GOVERNING EDITION is the artifact's own — the `openapi` field of the
// document the source addresses or carries. §3 makes that field the edition
// discriminator for the whole source, and §6 already establishes that a
// referenced document need not itself be a conforming OpenAPI Document, so a
// per-document edition is not generally available to consult.
//
// SCOPE. This governs the fragment inside an artifact `$ref` only. A binding's
// own `ref` (§7) is a different pointer with a different owner: core OBI-B-02
// item 5 assigns its syntax and resolution to this binding specification, and
// RFC 6901 §7 delegates a pointer application's error handling to that
// application. §7's path-item rule is unchanged in both branches.
//
// This file is a byte-for-byte twin of `openapi-client/go/reference_traversal.go`
// apart from its package clause, and states the same rule as
// `openapi-client/typescript/src/internal/deref.ts`.

// openAPIFollowsPointerBelowReference reports whether the artifact's declared
// OAS edition resolves a `$ref` standing in a fragment's path and continues
// evaluation into the target.
//
// The refusing branch is enumerated rather than the following one on purpose:
// an edition outside the accepted set, or a document whose `openapi` field this
// pass could not read, is refused by OAPI-P-01 at load with its own diagnostic,
// and this pass must not pre-empt that with a second, less informative one.
func openAPIFollowsPointerBelowReference(edition string) bool {
	switch edition {
	case "3.1.0", "3.1.1", "3.1.2", "3.2.0":
		return false
	default:
		return true
	}
}

// pointerBelowReference evaluates a JSON Pointer against a resource's LITERAL
// contents and reports the first position at which the pointer's path runs
// below a reference: a JSON object carrying a `$ref` string member that does
// not itself carry the next reference token.
//
// It returns that node's `$ref` value and the token that identified nothing.
// A pointer that resolves plainly, that names nothing with no reference in the
// way, or that is not a well-formed RFC 6901 pointer reports nothing — the
// first is not a traversal at all and the other two are ordinary unresolvable
// references that the load lane already reports in its own terms.
//
// A `$ref` object that ALSO declares the next token as a sibling member is not
// a traversal either: the token names a member of the node in hand, which is
// exactly the case JSON Schema 2020-12 §8.2.3.1's note contemplates.
func pointerBelowReference(root any, pointer string) (ref string, token string, found bool) {
	if !strings.HasPrefix(pointer, "/") {
		// An anchor or the empty fragment carries no path to run through.
		return "", "", false
	}
	current := root
	for _, encoded := range strings.Split(pointer[1:], "/") {
		if !wellFormedPointerToken(encoded) {
			return "", "", false
		}
		decoded := decodePointerToken(encoded)
		if object, isObject := current.(map[string]any); isObject {
			if reference, isReference := object["$ref"].(string); isReference {
				if _, sibling := object[decoded]; !sibling {
					return reference, decoded, true
				}
			}
		}
		switch typed := current.(type) {
		case map[string]any:
			child, present := typed[decoded]
			if !present {
				return "", "", false
			}
			current = child
		case []any:
			index, ok := rawSequenceIndex(decoded, len(typed))
			if !ok {
				return "", "", false
			}
			current = typed[index]
		default:
			return "", "", false
		}
	}
	return "", "", false
}

// wellFormedPointerToken applies RFC 6901 §3's escaping grammar: a literal "~"
// is not a legal reference token character, it must be "~0" or "~1".
func wellFormedPointerToken(token string) bool {
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			continue
		}
		if index+1 >= len(token) {
			return false
		}
		if token[index+1] != '0' && token[index+1] != '1' {
			return false
		}
		index++
	}
	return true
}

func decodePointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}

// pointerBelowReferenceRefusal is the one diagnostic, spelled identically by all
// three engines so a consumer reads the same sentence whichever one ran it. It
// names the reference whose fragment was being evaluated, the edition whose text
// decided the question, the token that identified nothing, and the reference
// object standing in the pointer's path.
func pointerBelowReferenceRefusal(reference, edition, token, standingRef string) error {
	return fmt.Errorf(
		"openapi: reference %q is unresolvable under OAS %s: its fragment's token %q identifies no member of the referenced document's literal contents, where the reference object %q stands in the pointer's path; on the 3.1 line a fragment is a JSON-Pointer over the referenced document (OAS §4.6) and $ref substitutes nothing (JSON Schema 2020-12 §8.2.3.1, RFC 6901 §4)",
		reference, edition, token, standingRef,
	)
}
