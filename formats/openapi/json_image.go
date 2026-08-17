package openapi

import (
	"bytes"
	"encoding/json"
)

// jsonImage renders one application value as the JSON text that rides the
// wire. Every lane in this engine that turns a caller value into JSON bytes
// goes through it: the JSON request body, a multipart part whose selected
// media type is JSON-family, an `application/x-www-form-urlencoded` property on
// the CONTENT lane, a content-form parameter declaring a JSON media type, and
// the revision-2 routing transform expression.
//
// WHY THIS EXISTS AT ALL, AND WHY IT IS EXPLICIT.
//
// RFC 8259 Section 7 requires exactly three things to be escaped inside a JSON
// string — "quotation mark, reverse solidus, and the control characters
// (U+0000 through U+001F)" — and then permits, but never requires, any other
// character to be escaped as well: "Any character may be escaped." Its `char`
// production spells both alternatives, `unescaped = %x20-21 / %x23-5B /
// %x5D-10FFFF` for the literal form and `escape %x75 4HEXDIG` for the
// six-character form. AMPERSAND (U+0026), LESS-THAN SIGN (U+003C) and
// GREATER-THAN SIGN (U+003E) all fall inside `unescaped`, so `{"a":"x&y"}` and
// `{"a":"x\u0026y"}` are both conformant JSON texts carrying the same value.
// (Read at the pinned bytes: corpus-lab authority pin
// `authorities/texts/openapi/json/rfc8259.txt`, SHA-256
// 61a5378f4255c720beb2a4b4a63b29540147c140f36988bf086291989b4cd2d7.)
//
// Because the authority permits several spellings and an implementation must
// still put ONE byte string on the wire, the choice is not the incorporated
// authority's and is not `openbindings.openapi@1`'s either — §10 places
// document translation outside the specification. It is THIS IMPLEMENTATION'S
// CONVENTION, held byte-identical with the TypeScript twin
// (`openapi-client/typescript/src/json-image.ts`) and pinned by the shared case
// table `testdata/json-image-cases.json`.
//
// The convention is: EMIT THE LITERAL CHARACTERS. Two grounds.
//
//  1. The literal characters are what the application value contains. Escaping
//     is a representation choice with no meaning at the operation boundary, and
//     the spelling that adds nothing is the one that states nothing extra.
//  2. Go's `encoding/json` escapes `<`, `>` and `&` by default so that the
//     result is safe to embed inside HTML — that is what `SetEscapeHTML`
//     documents itself as controlling. No lane here embeds JSON into HTML: the
//     bytes go into a request body, a multipart part, a form field or a query
//     string. The default is a host-language safety measure aimed at a context
//     this engine is not in, and it was never a decision anyone made about this
//     wire.
//
// The encoder is therefore configured EXPLICITLY rather than left to either
// language's default, so the two engines' agreement is written down in both
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
