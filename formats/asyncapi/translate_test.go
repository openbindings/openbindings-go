package asyncapi

import (
	"encoding/json"
	"testing"
)

// Each case is one authority-derived dialect cell: an AsyncAPI Schema Object
// spelling whose verbatim copy into a 2020-12 position is either invalid or —
// worse — valid with a different meaning than the author's Draft-07 text.
func TestTranslateSchemaDialect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"tuple items become prefixItems",
			`{"type":"array","items":[{"type":"string"},{"type":"integer"}]}`,
			`{"type":"array","prefixItems":[{"type":"string"},{"type":"integer"}]}`,
		},
		{
			"additionalItems with a tuple becomes items",
			`{"type":"array","items":[{"type":"string"}],"additionalItems":{"type":"integer"}}`,
			`{"type":"array","prefixItems":[{"type":"string"}],"items":{"type":"integer"}}`,
		},
		{
			"additionalItems without a tuple was inert and drops",
			`{"type":"array","items":{"type":"string"},"additionalItems":{"type":"integer"}}`,
			`{"type":"array","items":{"type":"string"}}`,
		},
		{
			"dependencies split into dependentRequired and dependentSchemas",
			`{"dependencies":{"card":["cvv"],"billing":{"required":["address"]}}}`,
			`{"dependentRequired":{"card":["cvv"]},"dependentSchemas":{"billing":{"required":["address"]}}}`,
		},
		{
			"$schema drops",
			`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`,
			`{"type":"object"}`,
		},
		{
			"plain-name fragment $id becomes $anchor",
			`{"$id":"#MassCancelResponse","type":"object"}`,
			`{"$anchor":"MassCancelResponse","type":"object"}`,
		},
		{
			"relative $id drops",
			`{"$id":"GeneralReply","type":"object"}`,
			`{"type":"object"}`,
		},
		{
			"pointer-form $id drops",
			`{"$id":"#/properties/driver","type":"object"}`,
			`{"type":"object"}`,
		},
		{
			"absolute $id keeps",
			`{"$id":"https://example.com/schemas/task","type":"object"}`,
			`{"$id":"https://example.com/schemas/task","type":"object"}`,
		},
		{
			// Draft 07 ignores assertion keywords beside $ref; keeping them
			// would activate constraints the author's dialect made inert.
			"assertion siblings of $ref drop, annotations keep",
			`{"$ref":"https://example.com/x.json","maxLength":5,"description":"kept"}`,
			`{"$ref":"https://example.com/x.json","description":"kept"}`,
		},
		{
			"literal keywords are never entered",
			`{"enum":[{"items":[1,2]}],"const":{"$schema":"x"},"default":{"dependencies":{}}}`,
			`{"enum":[{"items":[1,2]}],"const":{"$schema":"x"},"default":{"dependencies":{}}}`,
		},
		{
			"map-of-schema members translate regardless of member name",
			`{"properties":{"enum":{"items":[{"type":"string"}]},"const":{"$schema":"y","type":"integer"}}}`,
			`{"properties":{"enum":{"prefixItems":[{"type":"string"}]},"const":{"type":"integer"}}}`,
		},
		{
			"unknown and AsyncAPI-extension keywords pass through",
			`{"discriminator":"kind","x-custom":{"items":[1]},"externalDocs":{"url":"https://example.com"}}`,
			`{"discriminator":"kind","x-custom":{"items":[1]},"externalDocs":{"url":"https://example.com"}}`,
		},
		{
			"nested translation reaches combinators and items",
			`{"anyOf":[{"items":[{"type":"string"}]}],"not":{"$schema":"z","dependencies":{"a":["b"]}}}`,
			`{"anyOf":[{"prefixItems":[{"type":"string"}]}],"not":{"dependentRequired":{"a":["b"]}}}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var in map[string]any
			if err := json.Unmarshal([]byte(testCase.in), &in); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(translateSchemaDialect(in))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal([]byte(testCase.want), &want); err != nil {
				t.Fatal(err)
			}
			var gotValue any
			_ = json.Unmarshal(got, &gotValue)
			wantEncoded, _ := json.Marshal(want)
			gotCanonical, _ := json.Marshal(gotValue)
			if string(gotCanonical) != string(wantEncoded) {
				t.Fatalf("got %s, want %s", gotCanonical, wantEncoded)
			}
		})
	}
}
