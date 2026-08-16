package openapi

import (
	"context"
	"encoding/json"
	"fmt"
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

// The naming rule's own unit surface, pinned cell for cell in
// openapi-client/typescript/src/cutpoint-names.test.ts. Both engines must
// derive the same qualified spelling from the same two addresses. The whole
// base x document matrix is here because the twin claim was once made by three
// absolute http cases, and the two engines disagreed on every RELATIVE and
// every OPAQUE address without a test noticing.
func TestRelativeDocumentNameMatchesTheTypeScriptTwin(t *testing.T) {
	// Read down a column to see one document qualified from every artifact
	// address; read across a row to see one artifact address qualify every
	// document form. rule 3 in cutpoint_names.go states the three cases.
	documents := []string{
		"https://api.example/shared/node.yaml",
		"https://api.example/one.json",
		"https://other.example/one.yaml",
		"https://api.example:8443/v1/defs.yaml",
		"file:///checkout/api/shared/node.yaml",
		"/checkout/api/shared/node.yaml",
		"defs.yaml",
		"schemas/defs.yaml",
		"./defs.yaml",
		"urn:example:one",
		"https://api.example/a b/c.yaml",
		"https://api.example/a%20b/c.yaml",
		"https://api.example/v1/defs",
	}
	matrix := []struct {
		base string
		want []string
	}{
		{"https://api.example/root.yaml", []string{
			"shared/node",
			"one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"a b/c",
			"a b/c",
			"v1/defs",
		}},
		{"https://api.example/v1/root.yaml", []string{
			"shared/node",
			"one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"a b/c",
			"a b/c",
			"defs",
		}},
		{"https://api.example/root", []string{
			"shared/node",
			"one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"a b/c",
			"a b/c",
			"v1/defs",
		}},
		{"https://api.example:8443/v1/root.yaml", []string{
			"api.example/shared/node",
			"api.example/one",
			"other.example/one",
			"defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"api.example/a b/c",
			"api.example/a b/c",
			"api.example/v1/defs",
		}},
		{"file:///checkout/api/root.yaml", []string{
			"api.example/shared/node",
			"api.example/one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"shared/node",
			"shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"api.example/a b/c",
			"api.example/a b/c",
			"api.example/v1/defs",
		}},
		{"/checkout/api/root.yaml", []string{
			"api.example/shared/node",
			"api.example/one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"shared/node",
			"shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"api.example/a b/c",
			"api.example/a b/c",
			"api.example/v1/defs",
		}},
		{"root.yaml", []string{
			"api.example/shared/node",
			"api.example/one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"api.example/a b/c",
			"api.example/a b/c",
			"api.example/v1/defs",
		}},
		{"https://api.example/", []string{
			"shared/node",
			"one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"a b/c",
			"a b/c",
			"v1/defs",
		}},
		{"urn:example:root", []string{
			"api.example/shared/node",
			"api.example/one",
			"other.example/one",
			"api.example:8443/v1/defs",
			"checkout/api/shared/node",
			"checkout/api/shared/node",
			"defs",
			"schemas/defs",
			"defs",
			"urn:example:one",
			"api.example/a b/c",
			"api.example/a b/c",
			"api.example/v1/defs",
		}},
	}
	for _, row := range matrix {
		base := mustParseArtifactURL(t, row.base)
		for i, document := range documents {
			if got := relativeDocumentName(base, document); got != row.want[i] {
				t.Errorf("relativeDocumentName(%q, %q) = %q, want %q",
					row.base, document, got, row.want[i])
			}
		}
	}
	// Independent of how the artifact was reached: the same layout qualifies
	// identically from a checkout and from a server.
	fromDisk := relativeDocumentName(
		mustParseArtifactURL(t, "file:///checkout/api/root.yaml"),
		"file:///checkout/api/shared/node.yaml",
	)
	fromServer := relativeDocumentName(
		mustParseArtifactURL(t, "https://api.example/root.yaml"),
		"https://api.example/shared/node.yaml",
	)
	if fromDisk != fromServer || fromDisk != "shared/node" {
		t.Errorf("checkout produced %q and server %q, want %q from both",
			fromDisk, fromServer, "shared/node")
	}
}

// Every claimant shape the assignment rule admits, pinned identically in
// openapi-client/typescript/src/cutpoint-names.test.ts. The rule must be TOTAL
// (every claimant gets a key) and INJECTIVE (no two get the same one): `$defs`
// is a map, so a repeated key drops one definition and silently resolves the
// other cut point's `$ref` to the survivor.
//
// A claimant here is either the artifact's own component — a plain registry ref
// with no recorded external identity — or a component reached through an
// external reference, whose declaring document may be a real address or, where
// the resolver recorded none, empty. Note that the artifact cannot itself
// declare one name twice: its registry is keyed by the pointer. The reachable
// same-name-no-document contest is between external references whose documents
// were not recorded.
func TestAssignCutPointNamesMatchesTheTypeScriptTwin(t *testing.T) {
	const artifact = "https://api.example/root.yaml"
	type claimant struct {
		name     string
		document *string
		pointer  string
	}
	external := func(document string) *string { return &document }
	cases := []struct {
		label     string
		claimants []claimant
		want      []string
	}{
		{
			label: "uncontested names are the declaring document's own",
			claimants: []claimant{
				{name: "Node", document: nil, pointer: "/components/schemas/Node"},
				{name: "Team", document: external("https://api.example/shared/model.yaml"), pointer: "/components/schemas/Team"},
			},
			want: []string{"Node", "Team"},
		},
		{
			label: "the artifact's own component keeps the name, composed documents qualify",
			claimants: []claimant{
				{name: "Node", document: nil, pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/one.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/two.yaml"), pointer: "/components/schemas/Node"},
			},
			want: []string{"Node", "one_Node", "two_Node"},
		},
		{
			label: "two claimants with no declaring document to qualify by",
			claimants: []claimant{
				{name: "Node", document: external(""), pointer: "/components/schemas/Node"},
				{name: "Node", document: external(""), pointer: "/definitions/Node"},
			},
			want: []string{"Node", "Node_2"},
		},
		{
			label: "three claimants with no declaring document to qualify by",
			claimants: []claimant{
				{name: "Node", document: external(""), pointer: "/a/Node"},
				{name: "Node", document: external(""), pointer: "/b/Node"},
				{name: "Node", document: external(""), pointer: "/c/Node"},
			},
			want: []string{"Node", "Node_2", "Node_3"},
		},
		{
			label: "the artifact's own component outranks an unqualifiable claimant",
			claimants: []claimant{
				{name: "Node", document: nil, pointer: "/components/schemas/Node"},
				{name: "Node", document: external(""), pointer: "/definitions/Node"},
			},
			want: []string{"Node", "Node_2"},
		},
		{
			label: "three-way collision between composed documents",
			claimants: []claimant{
				{name: "Node", document: external("https://api.example/one.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/two.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/three.yaml"), pointer: "/components/schemas/Node"},
			},
			want: []string{"one_Node", "two_Node", "three_Node"},
		},
		{
			label: "two documents that qualify to one name",
			claimants: []claimant{
				{name: "Node", document: external("https://api.example/one.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/one.json"), pointer: "/components/schemas/Node"},
			},
			want: []string{"one_Node_2", "one_Node"},
		},
		{
			label: "a qualified name the artifact already spells itself",
			claimants: []claimant{
				{name: "one_Node", document: nil, pointer: "/components/schemas/one_Node"},
				{name: "Node", document: external("https://api.example/one.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("https://api.example/two.yaml"), pointer: "/components/schemas/Node"},
			},
			want: []string{"one_Node", "one_Node_2", "two_Node"},
		},
		{
			label: "unknown and absent declaring documents together",
			claimants: []claimant{
				{name: "Node", document: external(""), pointer: "/a/Node"},
				{name: "Node", document: external(""), pointer: "/b/Node"},
				{name: "Node", document: nil, pointer: "/components/schemas/Node"},
			},
			want: []string{"Node_2", "Node_3", "Node"},
		},
		{
			label: "opaque and relative declaring documents",
			claimants: []claimant{
				{name: "Node", document: external("urn:example:one"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("defs.yaml"), pointer: "/components/schemas/Node"},
				{name: "Node", document: external("./defs.yaml"), pointer: "/components/schemas/Node"},
			},
			want: []string{"urn_example_one_Node", "defs_Node_2", "defs_Node"},
		},
	}
	for _, testCase := range cases {
		externals := map[string]refIdentity{}
		refs := make([]string, 0, len(testCase.claimants))
		for i, claim := range testCase.claimants {
			if claim.document == nil {
				// The artifact's own component: the registry ref IS its pointer.
				refs = append(refs, "#"+claim.pointer)
				continue
			}
			key := fmt.Sprintf("ob_k%d", i)
			externals[key] = refIdentity{document: *claim.document, pointer: claim.pointer}
			refs = append(refs, "#/components/schemas/"+key)
		}
		names := newCutPointNamer(artifact, externals).assign(refs)
		got := make([]string, len(refs))
		assigned := map[string]bool{}
		for i, ref := range refs {
			got[i] = names[ref]
			if got[i] == "" {
				t.Errorf("%s: claimant %d received no name: the rule is not total", testCase.label, i)
			}
			if assigned[got[i]] {
				t.Errorf("%s: two claimants received %q: the rule is not injective, and one\n"+
					"definition is dropped from the emitted $defs map", testCase.label, got[i])
			}
			assigned[got[i]] = true
		}
		if len(got) != len(testCase.want) {
			t.Fatalf("%s: got %v, want %v", testCase.label, got, testCase.want)
		}
		for i := range got {
			if got[i] != testCase.want[i] {
				t.Errorf("%s: claimant %d = %q, want %q (got %v)", testCase.label, i, got[i], testCase.want[i], got)
			}
		}
		// Assignment is over the SET: reversing the input reverses only the output.
		reversed := make([]string, len(refs))
		copy(reversed, refs)
		for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
			reversed[i], reversed[j] = reversed[j], reversed[i]
		}
		again := newCutPointNamer(artifact, externals).assign(reversed)
		for _, ref := range refs {
			if again[ref] != names[ref] {
				t.Errorf("%s: reordering changed %s: %q then %q", testCase.label, ref, names[ref], again[ref])
			}
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
