package asyncapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// A record that declares a named enum inline and reuses it by name. The
// derivation materializes Suit once under $defs; every reference — including
// the one inside the union branch — spells the shared "#/$defs/<fullname>"
// form for decycle to rebase.
var reusedEnumAvro = map[string]any{
	"type": "record",
	"name": "ParentRecord",
	"fields": []any{
		map[string]any{"name": "first", "type": map[string]any{
			"type": "enum", "name": "Suit", "symbols": []any{"SPADES", "HEARTS"},
		}},
		map[string]any{"name": "second", "type": "Suit"},
		map[string]any{"name": "maybe", "type": []any{"null", "Suit"}},
	},
}

func TestDeriveAvroSchemaNamedTypeReuse(t *testing.T) {
	derived, ok := deriveAvroSchema(reusedEnumAvro)
	if !ok {
		t.Fatal("derivation refused a well-formed record")
	}
	if derived["$ref"] != "#/$defs/ParentRecord" {
		t.Fatalf("root ref = %v", derived["$ref"])
	}
	defs, _ := derived["$defs"].(map[string]any)
	if _, present := defs["Suit"]; !present {
		t.Fatalf("$defs missing reused named type: %v", defs)
	}
	parent, _ := defs["ParentRecord"].(map[string]any)
	props, _ := parent["properties"].(map[string]any)
	for _, field := range []string{"first", "second"} {
		member, _ := props[field].(map[string]any)
		if member["$ref"] != "#/$defs/Suit" {
			t.Fatalf("field %s ref = %v", field, member["$ref"])
		}
	}
}

// The emitted operation schema must contain no derivation-form ref: every
// "#/$defs/<name>" — including those inside $defs members, which ride
// beside the derived root's own $ref — rebases onto the operation pointer.
// The regression this pins: decycleNode returning at a $ref node without
// walking its siblings left the $defs interior unrebased, the pointers
// dangled, and the schema-defect gate wrongly excluded the operation
// (corpus specimen asyncapi/avro-schema-parser
// test/documents/asyncapi-with-reused-enums.yaml).
func TestSynthesizeAvroNamedTypeReuseRebasesAllRefs(t *testing.T) {
	artifact := map[string]any{
		"asyncapi": "3.0.0",
		"info":     map[string]any{"title": "Reused named types", "version": "1"},
		"servers":  map[string]any{"broker": map[string]any{"host": "broker.example:9092", "protocol": "kafka"}},
		"channels": map[string]any{
			"records": map[string]any{
				"address": "records.v1",
				"messages": map[string]any{
					"record": map[string]any{
						"payload": map[string]any{
							"schemaFormat": "application/vnd.apache.avro;version=1.9.0",
							"schema":       reusedEnumAvro,
						},
					},
				},
			},
		},
		"operations": map[string]any{
			"publishRecord": map[string]any{
				"action":   "receive",
				"channel":  map[string]any{"$ref": "#/channels/records"},
				"messages": []any{map[string]any{"$ref": "#/channels/records/messages/record"}},
			},
		},
	}
	content, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	iface, err := (&Synthesizer{}).SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	op, present := iface.Operations["publishRecord"]
	if !present {
		t.Fatalf("operation excluded; operations = %v", iface.Operations)
	}
	encoded, err := json.Marshal(op.Input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"#/$defs/`) {
		t.Fatalf("unrebased derivation ref survives in emitted schema: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"#/operations/publishRecord/input/$defs/Suit"`) {
		t.Fatalf("expected rebased reused-type ref: %s", encoded)
	}
}

// The invocation-side guard (§9.2 codec capability): an Avro-declared
// payload refuses any non-JSON wire pre-dispatch — the binary encoding is
// an unqualified codec here — while JSON-family media rides the ordinary
// JSON lane (that lane IS the Avro-JSON wire).
func TestAvroMediaGuard(t *testing.T) {
	avroMsg := message{Payload: map[string]any{
		"schemaFormat": "application/vnd.apache.avro;version=1.9.0",
		"schema":       map[string]any{"type": "record", "name": "R", "fields": []any{}},
	}}
	if err := avroMediaGuard(avroMsg, "avro/binary"); err == nil || !strings.Contains(err.Error(), "Avro binary codec") {
		t.Fatalf("binary media: err = %v", err)
	}
	if err := avroMediaGuard(avroMsg, "text/plain"); err == nil {
		t.Fatal("text media must refuse for an Avro-declared payload")
	}
	if err := avroMediaGuard(avroMsg, "application/json"); err != nil {
		t.Fatalf("JSON media: %v", err)
	}
	plain := message{Payload: map[string]any{"type": "object"}}
	if err := avroMediaGuard(plain, "avro/binary"); err != nil {
		t.Fatalf("non-Avro payload must not trip the guard: %v", err)
	}
}

// A top-level union: the Avro specification admits a JSON ARRAY as a
// complete schema, so the derivation must accept it at the root exactly as
// at an interior position (MC5 seal-1 finding F-V3-2's derivation cell;
// ASYNC-SS-24 pins the end-to-end projection).
func TestDeriveAvroSchemaTopLevelUnion(t *testing.T) {
	derived, ok := deriveAvroSchema([]any{
		"null",
		map[string]any{
			"type":   "record",
			"name":   "File",
			"fields": []any{map[string]any{"name": "path", "type": "string"}},
		},
	})
	if !ok {
		t.Fatal("a top-level union is a legal Avro schema and must derive")
	}
	branches, ok := derived["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("derived = %#v, want a two-branch oneOf", derived)
	}
	if branches[0].(map[string]any)["type"] != "null" {
		t.Fatalf("null branch = %#v", branches[0])
	}
	wrapper := branches[1].(map[string]any)
	required, _ := wrapper["required"].([]any)
	if len(required) != 1 || required[0] != "File" {
		t.Fatalf("named branch must carry the single-key JSON Encoding wrapper, got %#v", wrapper)
	}
}
