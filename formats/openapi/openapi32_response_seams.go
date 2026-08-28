package openapi

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

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

// openAPI32UnaryResponseBridgeOperation admits only the Response declarations
// whose semantics are identical to the ratified 3.1 trunk: exact/default
// plain-unary responses with the formerly-required description present, no
// response content-coding declaration, and no 3.2 sequential media shape.
// Everything else remains visible in openAPI32M6ResponseSeams and is left for
// M6 rather than accidentally inheriting behavior from kin-openapi or the
// shared runtime.
func openAPI32UnaryResponseBridgeOperation(operation *openapi3.Operation) *openapi3.Operation {
	clone := cloneOpenAPIOperation(operation)
	if clone == nil || clone.Responses == nil {
		return clone
	}
	bridged := openapi3.NewResponsesWithCapacity(clone.Responses.Len())
	for key, ref := range clone.Responses.Map() {
		if !openAPI32UnaryResponseKey(key) || ref == nil || ref.Value == nil || ref.Value.Description == nil {
			continue
		}
		if openAPI32ResponseDeclaresHeader(ref.Value, "Content-Encoding") {
			continue
		}
		copyRef := *ref
		copyResponse := *ref.Value
		copyResponse.Content = make(openapi3.Content, len(ref.Value.Content))
		for mediaType, media := range ref.Value.Content {
			if !openAPI32UnaryResponseMedia(mediaType, media) {
				continue
			}
			copyResponse.Content[mediaType] = media
		}
		if len(ref.Value.Content) > 0 && len(copyResponse.Content) == 0 {
			continue
		}
		copyRef.Value = &copyResponse
		bridged.Set(key, &copyRef)
	}
	clone.Responses = bridged
	return clone
}

func openAPI32ResponseDeclaresHeader(response *openapi3.Response, wanted string) bool {
	if response == nil {
		return false
	}
	for name := range response.Headers {
		if strings.EqualFold(name, wanted) {
			return true
		}
	}
	return false
}

func openAPI32UnaryResponseKey(key string) bool {
	if key == "default" {
		return true
	}
	if len(key) != 3 {
		return false
	}
	for _, char := range key {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func openAPI32UnaryResponseMedia(key string, media *openapi3.MediaType) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(key, ";", 2)[0]))
	if base == "text/event-stream" || base == "application/jsonl" || base == "application/x-ndjson" || base == "application/json-seq" || strings.HasSuffix(base, "+json-seq") {
		return false
	}
	return media == nil || media.ItemSchema == nil
}
