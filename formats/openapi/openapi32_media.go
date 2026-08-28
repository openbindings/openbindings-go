package openapi

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// The adapter needs only the 3.2 request representation's abstract shape in
// order to lower the public {parameters, body} envelope. Positional/nested
// Encoding semantics and wire bytes remain owned by openapi-client's
// artifact-local overlay.
func openAPI32SequentialRequestKind(base string, media *openapi3.MediaType) (string, bool, error) {
	itemSchema := media != nil && media.ItemSchema != nil
	positional := openAPI32PositionalMultipart(media)
	switch {
	case base == "application/jsonl", base == "application/x-ndjson":
		return "json-lines", true, nil
	case base == "application/json-seq", strings.HasSuffix(base, "+json-seq"):
		return "json-seq", true, nil
	case base == "text/event-stream":
		return "", true, fmt.Errorf("text/event-stream has no incorporated request write algorithm")
	case strings.HasPrefix(base, "multipart/") && positional:
		return "multipart", true, nil
	case itemSchema:
		return "", true, fmt.Errorf("media type %q declares itemSchema but has no incorporated sequential request framing", base)
	default:
		return "", false, nil
	}
}

func openAPI32PositionalMultipart(media *openapi3.MediaType) bool {
	if media == nil {
		return false
	}
	if media.ItemSchema != nil || schemaTypeIs(mediaSchema(media), "array", map[*openapi3.Schema]bool{}) || openAPI32MediaReference(media) {
		return true
	}
	return media.Extensions != nil && (media.Extensions["prefixEncoding"] != nil || media.Extensions["itemEncoding"] != nil)
}

func openAPI32MediaReference(media *openapi3.MediaType) bool {
	if media == nil || media.Extensions == nil {
		return false
	}
	_, present := media.Extensions["$ref"]
	return present
}

func validateOpenAPI32MultipartMedia(doc *openapi3.T, media *openapi3.MediaType) error {
	if openAPI32MediaReference(media) {
		// The standalone artifact overlay resolves components.mediaTypes; this
		// adapter view needs only a whole-body route for the referenced value.
		return nil
	}
	if media == nil || len(media.Encoding) == 0 {
		return validateRevision3MultipartMedia(doc, media)
	}
	copyMedia := *media
	copyMedia.Encoding = make(openapi3.Encodings, len(media.Encoding))
	for name, encoding := range media.Encoding {
		if encoding == nil {
			copyMedia.Encoding[name] = nil
			continue
		}
		if encoding.Extensions != nil && (encoding.Extensions["encoding"] != nil || encoding.Extensions["prefixEncoding"] != nil || encoding.Extensions["itemEncoding"] != nil) {
			copyMedia.Encoding[name] = nil
			continue
		}
		copyMedia.Encoding[name] = encoding
	}
	return validateRevision3MultipartMedia(doc, &copyMedia)
}

func validateOpenAPI32URLEncodedMedia(doc *openapi3.T, media *openapi3.MediaType) error {
	schema := mediaSchema(media)
	if schema == nil {
		return fmt.Errorf("schema-omitted urlencoded media has no application-value caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	for name := range props {
		propertySchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		propertySchema, _ = effectiveRevision3PartSchema(propertySchema, false)
		encoding := media.Encoding[name]
		if encodingUsesSerialization(encoding) {
			if err := validateMultipartSerializationMethod(name, propertySchema, encoding, false); err != nil {
				return err
			}
			continue
		}
		if encodingRequiresPropertyMedia(encoding) {
			continue
		}
		contentType, err := revision3PartContentType(propertySchema, encoding, false)
		if err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
		if _, err := revision3PropertyCarriage(propertySchema, contentType, false, true); err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
	}
	return nil
}

func openAPI32NonJSONTextSchema(schema *openapi3.Schema) bool {
	types, constrained, ambiguous := openAPI32ResolvedTypes(schema, map[*openapi3.Schema]bool{})
	if ambiguous || !constrained {
		return false
	}
	nonNull := false
	for member := range types {
		switch member {
		case "null":
		case "string", "boolean", "number", "integer":
			nonNull = true
		default:
			return false
		}
	}
	return nonNull
}

func openAPI32ResolvedTypes(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) (map[string]bool, bool, bool) {
	if schema == nil || seen[schema] {
		return nil, false, false
	}
	seen[schema] = true
	defer delete(seen, schema)
	var result map[string]bool
	constrained := false
	intersect := func(candidate map[string]bool, present bool) {
		if !present {
			return
		}
		if !constrained {
			result, constrained = candidate, true
			return
		}
		for member := range result {
			if !candidate[member] {
				delete(result, member)
			}
		}
	}
	if schema.Type != nil {
		candidate := map[string]bool{}
		for _, member := range schema.Type.Slice() {
			candidate[member] = true
		}
		intersect(candidate, true)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		candidate, present, ambiguous := openAPI32ResolvedTypes(member.Value, seen)
		if ambiguous {
			return nil, false, true
		}
		intersect(candidate, present)
	}
	for _, choice := range []openapi3.SchemaRefs{schema.AnyOf, schema.OneOf} {
		if len(choice) == 0 {
			continue
		}
		union := map[string]bool{}
		for _, branch := range choice {
			if branch == nil || branch.Value == nil {
				return nil, false, true
			}
			candidate, present, ambiguous := openAPI32ResolvedTypes(branch.Value, seen)
			if ambiguous || !present {
				return nil, false, true
			}
			for member := range candidate {
				union[member] = true
			}
		}
		intersect(union, true)
	}
	return result, constrained, false
}
