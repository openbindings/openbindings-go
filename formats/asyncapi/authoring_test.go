package asyncapi

import "testing"

func TestClassifySchemaFormat(t *testing.T) {
	cases := []struct {
		format string
		want   schemaFormatDisposition
	}{
		// Absent or blank: the artifact's default (Draft-07-superset) governs.
		{"", schemaFormatTranslate},
		{"   ", schemaFormatTranslate},
		// AsyncAPI default formats, any suffix, case-insensitive.
		{"application/vnd.aai.asyncapi;version=3.0.0", schemaFormatTranslate},
		{"application/vnd.aai.asyncapi+json;version=2.6.0", schemaFormatTranslate},
		{"application/vnd.aai.asyncapi+yaml", schemaFormatTranslate},
		{"Application/VND.AAI.AsyncAPI+JSON;Version=3.0.0", schemaFormatTranslate},
		// Official JSON Schema media type: version parameter decides.
		{"application/schema+json;version=draft-07", schemaFormatTranslate},
		{"application/schema+yaml;version=draft-07", schemaFormatTranslate},
		{"application/schema+json;version=draft/2020-12", schemaFormatPassthrough},
		{"application/schema+json;version=\"draft/2020-12\"", schemaFormatPassthrough},
		// Unknown or absent JSON Schema version: no translation rules to apply.
		{"application/schema+json", schemaFormatForeign},
		{"application/schema+json;version=draft-04", schemaFormatForeign},
		// The named Avro correspondence (ruled 2026-08-14): 1.x editions.
		{"application/vnd.apache.avro;version=1.9.0", schemaFormatAvro},
		{"application/vnd.apache.avro+json;version=1.11.1", schemaFormatAvro},
		{"application/vnd.apache.avro", schemaFormatAvro},
		{"application/vnd.apache.avro;version=2.0.0", schemaFormatForeign},
		// Foreign languages that the substring heuristic previously mishandled.
		{"application/vnd.google.protobuf;version=2", schemaFormatForeign},
		{"application/vnd.oai.openapi;version=3.0.0", schemaFormatForeign},
		{"application/raml+yaml;version=1.0", schemaFormatForeign},
		// Malformed media types are foreign, never guessed at.
		{"not a media type", schemaFormatForeign},
		{"avro", schemaFormatForeign},
	}
	for _, tc := range cases {
		if got := classifySchemaFormat(tc.format); got != tc.want {
			t.Errorf("classifySchemaFormat(%q) = %v, want %v", tc.format, got, tc.want)
		}
	}
}

func TestUnionPayloadSchemasPassthrough(t *testing.T) {
	schema := map[string]any{
		"type":         "object",
		"properties":   map[string]any{"card": map[string]any{"type": "string"}},
		"dependencies": map[string]any{"card": []any{"cvv"}},
	}
	wrap := func(format string) message {
		return message{Payload: map[string]any{"schemaFormat": format, "schema": schema}}
	}
	doc := &document{}

	// Declared 2020-12 passes through verbatim — the Draft-07 keyword walk
	// must not touch it (dependencies stays spelled as the artifact wrote it).
	got := unionPayloadSchemas(doc, []message{wrap("application/schema+json;version=draft/2020-12")})
	if _, present := got["dependencies"]; !present {
		t.Fatalf("passthrough rewrote the declared 2020-12 schema: %v", got)
	}

	// Declared Draft-07 translates: dependencies with a string-array member
	// becomes dependentRequired.
	got = unionPayloadSchemas(doc, []message{wrap("application/schema+json;version=draft-07")})
	if _, present := got["dependencies"]; present {
		t.Fatalf("declared Draft-07 schema was not translated: %v", got)
	}
	if _, present := got["dependentRequired"]; !present {
		t.Fatalf("expected dependentRequired after translation: %v", got)
	}
}
