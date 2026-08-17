package openapi

import (
	"bytes"
	"encoding/json"
)

// jsonImage renders one application value as the JSON text that rides the
// wire. Every lane in this engine that turns a caller value into JSON bytes
// goes through it: the JSON request body, a multipart part whose selected part
// media type is JSON-family, an `application/x-www-form-urlencoded` property on
// the CONTENT path, a content-form parameter declaring a JSON media type, and
// the revision-2 routing transform expression.
//
// WHY THE CHOICE IS THIS ENGINE'S TO MAKE, AND WHOSE IT IS NOT.
//
// [RFC 8259] Section 7 requires exactly three things to be escaped inside a
// JSON string — "quotation mark, reverse solidus, and the control characters
// (U+0000 through U+001F)" — and then permits, without ever requiring, any
// other character to be escaped as well: "Any character may be escaped." Its
// `char` production spells both alternatives, `unescaped = %x20-21 / %x23-5B /
// %x5D-10FFFF` for the literal form and `escape %x75 4HEXDIG` for the
// six-character one. AMPERSAND (U+0026), LESS-THAN SIGN (U+003C) and
// GREATER-THAN SIGN (U+003E) all fall inside `unescaped`, so `{"a":"x&y"}` and
// `{"a":"x\u0026y"}` are two conformant JSON texts carrying one value. (Read at
// the pinned bytes: corpus-lab authority pin
// `authorities/texts/openapi/json/rfc8259.txt`, SHA-256
// 61a5378f4255c720beb2a4b4a63b29540147c140f36988bf086291989b4cd2d7.)
//
// `openbindings.openapi@1` Section 9.2, "The JSON image of an application
// value", governs these lanes and DELIBERATELY DOES NOT DECIDE THIS. It
// preserves RFC 8259's permitted set, states no preference inside it, and says
// in as many words that "[w]hich member an implementation emits is that
// implementation's own documented behavior, not portable meaning under this
// identifier." (OAPI-P-04 carries the same sentence in the conformance
// summary.) So the specification has answered the question it owns — the set —
// and left this one open on purpose. It is not a gap and it is not silence.
//
// Exactly one byte string still goes on the wire, so this engine must choose,
// and THIS COMMENT IS WHERE THAT CHOICE IS DOCUMENTED. The choice is held
// identical with the two twins — `openapi-client/go/json_image.go` and
// `openapi-client/typescript/src/json-image.ts` — and pinned by the shared case
// table `testdata/json-image-cases.json`. The Go twin is this file below the
// package clause except for the paragraph above: that package is a standalone
// OpenAPI engine whose own `scripts/verify-boundary.mjs` forbids its non-test
// sources from naming a binding-specification identifier, so it states the same
// posture without the citation and points here for it.
//
// THE CHOICE IS: EMIT THE LITERAL CHARACTERS. Two grounds.
//
//  1. The literal characters are what the application value contains. Escaping
//     is a representation choice with no meaning at the operation boundary, and
//     the spelling that adds nothing is the one that states nothing extra.
//  2. Go's `encoding/json` escapes `<`, `>` and `&` by default so that the
//     result is safe to embed inside HTML — that is what `SetEscapeHTML`
//     documents itself as controlling. No lane here embeds JSON into HTML: the
//     bytes go into a request body, a multipart part, a form field or a header.
//     The default is a host-language safety measure aimed at a context this
//     engine is not in, and it was never a decision anyone made about this wire.
//
// The encoder is therefore configured EXPLICITLY rather than left to either
// language's default, so the two languages' agreement is written down in both
// rather than being a coincidence of which standard library each was built on.
func jsonImage(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	// json.Encoder.Encode terminates each value with a newline; json.Marshal
	// does not, and a wire body is one value.
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
