package openapi

// openAPI32M6ResponseSeams is the explicit boundary around M5's request
// implementation. The only response path admitted here is the already-shared
// plain-unary behavior where the 3.2 binding document states the same rule as
// the 3.1 trunk. M6 removes these seams individually as it implements them.
var openAPI32M6ResponseSeams = []struct {
	section string
	rule    string
	name    string
}{
	{"5.1", "OAPI32-P-01", "response-side reference identity and confinement"},
	{"6.2", "OAPI32-S-01", "callback and webhook dependency contracts and coverage"},
	{"9.4", "OAPI32-P-03", "governing response Content-Encoding declarations and decoder stacks"},
	{"9.5", "OAPI32-P-03", "sequential response media, itemSchema, JSONL/NDJSON/JSON-seq, positional multipart, and SSE"},
	{"9.6", "OAPI32-P-03", "3.2 response-key ranges, lookup, classification, required headers, media election, and value boundaries"},
}
