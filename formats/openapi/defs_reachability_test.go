package openapi

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Twin of openbindings-ts/packages/openapi/src/defs-reachability.test.ts.
//
// One self-recursive component, reached from a request body only through a
// property the OAS declares readOnly. Request projection removes that
// property, so the emitted input schema cannot reach the component; the
// emitted output schema, where readOnly properties stand, can. The emitted
// `$defs` map must state exactly that difference.
//
// The document is authored from the OAS text (readOnly is an OAS 3.1 Schema
// Object keyword inherited from JSON Schema 2020-12 §9.4.1; a self-recursive
// component is JSON Schema's own recursion) and from no corpus artifact.
const defsReachabilityDoc = `{
  "openapi": "3.1.0",
  "info": {"title": "defs reachability", "version": "1.0.0"},
  "paths": {
    "/records": {
      "post": {
        "operationId": "createRecord",
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Record"}}}
        },
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Record"}}}
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Record": {
        "type": "object",
        "required": ["title"],
        "properties": {
          "title": {"type": "string"},
          "audit": {
            "readOnly": true,
            "anyOf": [
              {"type": "string"},
              {"$ref": "#/components/schemas/AuditEntry"}
            ]
          }
        }
      },
      "AuditEntry": {
        "type": "object",
        "properties": {
          "actor": {"type": "string"},
          "previous": {
            "anyOf": [
              {"type": "string"},
              {"$ref": "#/components/schemas/AuditEntry"}
            ]
          }
        }
      }
    }
  }
}`

// TestDefsOmitUnreachableCutPoint pins the reachability-closure half: a cycle
// root reached only through a branch the direction projection removes must not
// survive in the emitted `$defs` inventory.
func TestDefsOmitUnreachableCutPoint(t *testing.T) {
	iface := synthesizeDefsReachabilityDoc(t)
	input := decodeSchema(t, iface.Operations["createRecord"].Input)

	if _, present := input["properties"].(map[string]any)["audit"]; present {
		t.Fatalf("request projection must omit the readOnly property; got %v", input["properties"])
	}
	if defs, present := input["$defs"]; present {
		t.Fatalf("emitted input reaches no definition, so $defs must be absent; got %v", defs)
	}
	assertDefsReachabilityClosed(t, "createRecord/input", input, "#/operations/createRecord/input")
}

// TestDefsKeepReachedCutPoint pins the other half: the same self-recursive
// component, reached from the emitted schema root, hoists to exactly one
// `$defs` entry that the root tree references.
func TestDefsKeepReachedCutPoint(t *testing.T) {
	iface := synthesizeDefsReachabilityDoc(t)
	output := decodeSchema(t, iface.Operations["createRecord"].Output)

	if _, present := output["properties"].(map[string]any)["audit"]; !present {
		t.Fatalf("response projection keeps readOnly properties; got %v", output["properties"])
	}
	defs, ok := output["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("emitted output reaches the recursive component; $defs must be present, got %v", output["$defs"])
	}
	if len(defs) != 1 {
		t.Fatalf("expected exactly one hoisted definition, got %v", sortedKeys(defs))
	}
	if _, ok := defs["AuditEntry"]; !ok {
		t.Fatalf("expected the component name as the definition key, got %v", sortedKeys(defs))
	}

	const want = "#/operations/createRecord/output/$defs/AuditEntry"
	root := map[string]any{}
	for key, value := range output {
		if key != "$defs" {
			root[key] = value
		}
	}
	if refs := collectSchemaRefStrings(root); len(refs) != 1 || refs[0] != want {
		t.Fatalf("root tree must reference the hoisted definition exactly once; got %v", refs)
	}
	if refs := collectSchemaRefStrings(defs["AuditEntry"]); len(refs) != 1 || refs[0] != want {
		t.Fatalf("the definition must carry the self-reference; got %v", refs)
	}
	assertDefsReachabilityClosed(t, "createRecord/output", output, "#/operations/createRecord/output")
}

// TestAuthorialDefsSurviveReachabilityClosure keeps the closure narrow. An
// OAS 3.1 Schema Object is a JSON Schema 2020-12 schema and may declare its
// own `$defs`; those members are artifact content, not synthesis output, and
// stand whether or not anything references them.
func TestAuthorialDefsSurviveReachabilityClosure(t *testing.T) {
	const doc = `{
  "openapi": "3.1.0",
  "info": {"title": "authorial defs", "version": "1.0.0"},
  "paths": {
    "/values": {
      "get": {
        "operationId": "readValue",
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {
              "type": "object",
              "$defs": {"Unused": {"type": "string"}},
              "properties": {"name": {"type": "string"}}
            }}}
          }
        }
      }
    }
  }
}`
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	output := decodeSchema(t, result.Interface.Operations["readValue"].Output)
	defs, ok := output["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("the artifact's own $defs must survive; got %v", output["$defs"])
	}
	if _, ok := defs["Unused"]; !ok || len(defs) != 1 {
		t.Fatalf("expected the authored definition intact; got %v", sortedKeys(defs))
	}
}

func synthesizeDefsReachabilityDoc(t *testing.T) *openbindings.Interface {
	t.Helper()
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(defsReachabilityDoc)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	if _, ok := result.Interface.Operations["createRecord"]; !ok {
		t.Fatalf("expected createRecord; got %v", result.Interface.Operations)
	}
	return result.Interface
}

func decodeSchema(t *testing.T, schema any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("schema must serialize: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("schema must decode as an object: %v", err)
	}
	return decoded
}

// assertDefsReachabilityClosed states the invariant both engines hold: an
// emitted operation schema's `$defs` map contains exactly the definitions
// reachable from that schema's root, and every emitted `$ref` into it
// resolves within the emitted document (OBI-D-16).
func assertDefsReachabilityClosed(t *testing.T, label string, schema map[string]any, refBase string) {
	t.Helper()
	defs, _ := schema["$defs"].(map[string]any)
	prefix := refBase + "/$defs/"

	reachable := map[string]bool{}
	var pending []string
	visit := func(node any) {
		for _, ref := range collectSchemaRefStrings(node) {
			if !strings.HasPrefix(ref, prefix) {
				continue
			}
			name := strings.SplitN(strings.TrimPrefix(ref, prefix), "/", 2)[0]
			name = unescapeJSONPointerSegment(name)
			if _, defined := defs[name]; !defined {
				t.Fatalf("%s: $ref %q does not resolve in the emitted document", label, ref)
			}
			if !reachable[name] {
				reachable[name] = true
				pending = append(pending, name)
			}
		}
	}
	root := map[string]any{}
	for key, value := range schema {
		if key != "$defs" {
			root[key] = value
		}
	}
	visit(root)
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		visit(defs[name])
	}
	for _, name := range sortedKeys(defs) {
		if !reachable[name] {
			t.Fatalf("%s: $defs carries %q, which the emitted schema root cannot reach", label, name)
		}
	}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
