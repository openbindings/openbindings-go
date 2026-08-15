package openapi

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Cut-point naming, twinned in openapi-client/typescript/src/util.ts and
// pinned there by cutpoint-names.test.ts. Every document here is authored from
// the OpenAPI text; none is derived from a corpus artifact.
//
// The invariant: a hoisted cycle participant is named by the component's own
// name in the document that DECLARES it, whether that is the artifact or a
// document the artifact composed through an external `$ref`, and that name is a
// function of the cut-point set rather than of the order the walk met them.

const cutPointNodeDocument = `openapi: 3.0.3
info: {title: Shared, version: "1"}
paths: {}
components:
  schemas:
    Node:
      type: object
      properties:
        label: {type: string}
        child: {$ref: '#/components/schemas/Node'}
`

func writeCutPointArtifact(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return "file://" + filepath.Join(dir, "root.yaml")
}

func synthesizeCutPointArtifact(t *testing.T, location string) openbindings.Interface {
	t.Helper()
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: location}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	return *iface
}

func operationDefs(t *testing.T, iface openbindings.Interface, operation, boundary string) map[string]any {
	t.Helper()
	op, found := iface.Operations[operation]
	if !found {
		t.Fatalf("operation %q absent; have %v", operation, operationKeys(iface))
	}
	var schema any
	if boundary == "input" {
		schema = op.Input
	} else {
		schema = op.Output
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal %s.%s: %v", operation, boundary, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode %s.%s: %v", operation, boundary, err)
	}
	defs, _ := decoded["$defs"].(map[string]any)
	return defs
}

func sortedDefNames(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func operationKeys(iface openbindings.Interface) []string {
	keys := make([]string, 0, len(iface.Operations))
	for key := range iface.Operations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// (i) A cycle reached only through an external reference is cut at the target
// document's own component name — not at a name this processor generated while
// internalizing the reference.
func TestCutPointNamedByExternalDocumentComponentName(t *testing.T) {
	location := writeCutPointArtifact(t, map[string]string{
		"root.yaml": `openapi: 3.0.3
info: {title: Root, version: "1"}
servers: [{url: "https://api.example.com"}]
paths:
  /nodes:
    get:
      operationId: listNodes
      responses:
        "200":
          description: nodes
          content:
            application/json:
              schema: {$ref: 'shared/node.yaml#/components/schemas/Node'}
`,
		"shared/node.yaml": cutPointNodeDocument,
	})
	defs := operationDefs(t, synthesizeCutPointArtifact(t, location), "listNodes", "output")
	if got := sortedDefNames(defs); len(got) != 1 || got[0] != "Node" {
		t.Fatalf("emitted $defs = %v, want exactly [Node]: an externally declared\n"+
			"component must be cut at the name its own document gives it", got)
	}
	body, _ := json.Marshal(defs["Node"])
	if want := "#/operations/listNodes/output/$defs/Node"; !strings.Contains(string(body), want) {
		t.Fatalf("hoisted definition does not reference %s: %s", want, body)
	}
}

// (ii) Two documents declaring one component name disambiguate by the document
// that declares each, deterministically and without reference to walk order.
func TestCutPointCollisionQualifiesByDeclaringDocument(t *testing.T) {
	location := writeCutPointArtifact(t, map[string]string{
		"root.yaml": `openapi: 3.0.3
info: {title: Root, version: "1"}
servers: [{url: "https://api.example.com"}]
paths:
  /both:
    get:
      operationId: getBoth
      responses:
        "200":
          description: both
          content:
            application/json:
              schema:
                type: object
                properties:
                  one: {$ref: 'one.yaml#/components/schemas/Node'}
                  two: {$ref: 'two.yaml#/components/schemas/Node'}
`,
		"one.yaml": cutPointNodeDocument,
		"two.yaml": strings.Replace(cutPointNodeDocument, "label: {type: string}", "label: {type: integer}", 1),
	})
	defs := operationDefs(t, synthesizeCutPointArtifact(t, location), "getBoth", "output")
	want := []string{"one_Node", "two_Node"}
	got := sortedDefNames(defs)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("emitted $defs = %v, want %v: two documents claiming one component\n"+
			"name qualify by the document that declares each", got, want)
	}
}

// (iii's neighbour) The artifact's own component keeps its name when a composed
// document declares an alias of it; only the external claimant is qualified.
func TestCutPointArtifactComponentKeepsItsNameOnCollision(t *testing.T) {
	location := writeCutPointArtifact(t, map[string]string{
		"root.yaml": `openapi: 3.0.3
info: {title: Root, version: "1"}
servers: [{url: "https://api.example.com"}]
paths:
  /both:
    get:
      operationId: getBoth
      responses:
        "200":
          description: both
          content:
            application/json:
              schema:
                type: object
                properties:
                  local: {$ref: '#/components/schemas/Node'}
                  remote: {$ref: 'other.yaml#/components/schemas/Node'}
components:
  schemas:
    Node:
      type: object
      properties:
        localOnly: {type: boolean}
        child: {$ref: '#/components/schemas/Node'}
`,
		"other.yaml": cutPointNodeDocument,
	})
	defs := operationDefs(t, synthesizeCutPointArtifact(t, location), "getBoth", "output")
	want := []string{"Node", "other_Node"}
	got := sortedDefNames(defs)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("emitted $defs = %v, want %v", got, want)
	}
}

// (iv) Reordering the artifact's paths changes no emitted name. The names are a
// function of the cut-point set; nothing about the traversal may reach them.
func TestCutPointNamesSurvivePathReordering(t *testing.T) {
	const header = `openapi: 3.0.3
info: {title: Root, version: "1"}
servers: [{url: "https://api.example.com"}]
paths:
`
	const solo = `  /alpha:
    get:
      operationId: getAlpha
      responses:
        "200": {description: a, content: {application/json: {schema: {$ref: 'two.yaml#/components/schemas/Node'}}}}
`
	// One operation hoists BOTH colliding declarations, so the order the walk
	// meets them is observable if anything about naming depends on it.
	both := func(first, second string) string {
		return `  /both:
    get:
      operationId: getBoth
      responses:
        "200":
          description: both
          content:
            application/json:
              schema:
                type: object
                properties:
                  ` + first + `: {$ref: '` + first + `.yaml#/components/schemas/Node'}
                  ` + second + `: {$ref: '` + second + `.yaml#/components/schemas/Node'}
`
	}
	// ONE directory, rewritten in place: the artifact's address is held fixed so
	// the only thing that changes between the two syntheses is declaration order.
	location := writeCutPointArtifact(t, map[string]string{
		"root.yaml": header + solo + both("one", "two"),
		"one.yaml":  cutPointNodeDocument,
		"two.yaml":  strings.Replace(cutPointNodeDocument, "label: {type: string}", "label: {type: integer}", 1),
	})
	forward := synthesizeCutPointArtifact(t, location)

	rootPath := strings.TrimPrefix(location, "file://")
	if err := os.WriteFile(rootPath, []byte(header+both("two", "one")+solo), 0o600); err != nil {
		t.Fatalf("rewrite root: %v", err)
	}
	reversed := synthesizeCutPointArtifact(t, location)

	want := []string{"one_Node", "two_Node"}
	for _, iface := range []openbindings.Interface{forward, reversed} {
		got := sortedDefNames(operationDefs(t, iface, "getBoth", "output"))
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("getBoth emitted %v, want %v: a $defs key must not depend on the\n"+
				"order the artifact declares its paths or properties", got, want)
		}
	}
	// The uncontested cut point in the other operation keeps the artifact's own
	// name in both orderings: contention is scoped to one operation schema.
	for _, iface := range []openbindings.Interface{forward, reversed} {
		if got := sortedDefNames(operationDefs(t, iface, "getAlpha", "output")); len(got) != 1 || got[0] != "Node" {
			t.Fatalf("getAlpha emitted %v, want [Node]", got)
		}
	}
}

// One artifact component reached through two reference spellings is ONE cut
// point. kin-openapi records a resolved reference's fragment with and without
// its leading '#' depending on which resolution path reached it; keying the
// internalization on the unnormalized spelling published the same component
// twice, under two generated names.
func TestExternalComponentReachedTwiceIsOneCutPoint(t *testing.T) {
	location := writeCutPointArtifact(t, map[string]string{
		"root.yaml": `openapi: 3.0.3
info: {title: Root, version: "1"}
servers: [{url: "https://api.example.com"}]
paths:
  /reports:
    get:
      operationId: listReports
      responses:
        "200":
          description: reports
          content:
            application/json:
              schema: {$ref: 'shared/model.yaml#/components/schemas/Report'}
`,
		"shared/model.yaml": `openapi: 3.0.3
info: {title: Shared, version: "1"}
paths: {}
components:
  schemas:
    Report:
      type: object
      properties:
        owner: {$ref: '#/components/schemas/Team'}
    Team:
      type: object
      properties:
        lead: {$ref: '#/components/schemas/Member'}
        latest: {$ref: '#/components/schemas/Report'}
    Member:
      type: object
      properties:
        team: {$ref: '#/components/schemas/Team'}
`,
	})
	defs := operationDefs(t, synthesizeCutPointArtifact(t, location), "listReports", "output")
	want := []string{"Member", "Report", "Team"}
	got := sortedDefNames(defs)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("emitted $defs = %v, want %v: one artifact component is one cut point,\n"+
			"however many reference spellings reach it", got, want)
	}
}

// The naming rule's own unit surface, pinned identically in
// openapi-client/typescript/src/cutpoint-names.test.ts. Both engines must
// derive the same qualified spelling from the same two addresses, including the
// decoded path spelling and the member-name sanitization.
func TestRelativeDocumentNameMatchesTheTypeScriptTwin(t *testing.T) {
	const artifact = "https://api.example/root.yaml"
	cases := []struct{ document, want string }{
		{"https://api.example/shared/node.yaml", "shared/node"},
		{"https://api.example/one.json", "one"},
		{"https://other.example/one.yaml", "other.example/one"},
	}
	base := mustParseArtifactURL(t, artifact)
	for _, testCase := range cases {
		if got := relativeDocumentName(base, testCase.document); got != testCase.want {
			t.Errorf("relativeDocumentName(%q) = %q, want %q", testCase.document, got, testCase.want)
		}
	}
	// Independent of how the artifact was reached: the same layout qualifies
	// identically from a checkout and from a server.
	fromDisk := relativeDocumentName(
		mustParseArtifactURL(t, "file:///checkout/api/root.yaml"),
		"file:///checkout/api/shared/node.yaml",
	)
	if fromDisk != "shared/node" {
		t.Errorf("file:// layout produced %q, want %q", fromDisk, "shared/node")
	}
}

func TestAssignCutPointNamesMatchesTheTypeScriptTwin(t *testing.T) {
	namer := newCutPointNamer("https://api.example/root.yaml", map[string]refIdentity{
		"ob_one":   {document: "https://api.example/one.yaml", pointer: "/components/schemas/Node"},
		"ob_two":   {document: "https://api.example/two.yaml", pointer: "/components/schemas/Node"},
		"ob_team":  {document: "https://api.example/shared/model.yaml", pointer: "/components/schemas/Team"},
		"ob_space": {document: "https://api.example/a b/c.yaml", pointer: "/components/schemas/Node"},
	})
	assert := func(refs []string, want map[string]string) {
		t.Helper()
		got := namer.assign(refs)
		for ref, expected := range want {
			if got[ref] != expected {
				t.Errorf("assign(%v)[%s] = %q, want %q", refs, ref, got[ref], expected)
			}
		}
	}
	// Uncontested names are the declaring document's own.
	assert([]string{"#/components/schemas/Node", "#/components/schemas/ob_team"}, map[string]string{
		"#/components/schemas/Node":    "Node",
		"#/components/schemas/ob_team": "Team",
	})
	// The artifact's own component keeps its name; every contesting composed
	// document qualifies.
	assert([]string{
		"#/components/schemas/Node",
		"#/components/schemas/ob_one",
		"#/components/schemas/ob_two",
	}, map[string]string{
		"#/components/schemas/Node":   "Node",
		"#/components/schemas/ob_one": "one_Node",
		"#/components/schemas/ob_two": "two_Node",
	})
	// Characters a member name cannot carry are replaced, on the decoded path.
	assert([]string{"#/components/schemas/ob_space", "#/components/schemas/ob_one"}, map[string]string{
		"#/components/schemas/ob_space": "a_b_c_Node",
		"#/components/schemas/ob_one":   "one_Node",
	})
	// Assignment is over the set: reversing the input changes nothing.
	forward := namer.assign([]string{"#/components/schemas/ob_one", "#/components/schemas/ob_two"})
	reversed := namer.assign([]string{"#/components/schemas/ob_two", "#/components/schemas/ob_one"})
	for ref, name := range forward {
		if reversed[ref] != name {
			t.Errorf("reordering changed %s: %q then %q", ref, name, reversed[ref])
		}
	}
}

func mustParseArtifactURL(t *testing.T, address string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %q: %v", address, err)
	}
	return parsed
}
