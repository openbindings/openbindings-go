package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// serverTargetBaseCasesDigest pins the frozen twin case table. The identical
// file is executed by the other two engines that resolve an OpenAPI Server
// Object into a target address; changing it in one engine without the others
// fails here.
const serverTargetBaseCasesDigest = "4dc1e910ccba0fea37a8bce4a12465d3275b57cc5d275b9f8ddfdc46c44428ae"

type serverTargetBaseTable struct {
	Comment        string                   `json:"$comment"`
	PredicateCases []serverTargetPredicate  `json:"predicateCases"`
	ResolutionCase []serverTargetResolution `json:"resolutionCases"`
}

type serverTargetPredicate struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Usable bool   `json:"usable"`
	Why    string `json:"why"`
	Basis  string `json:"basis"`
}

type serverTargetResolution struct {
	Name             string           `json:"name"`
	OpenAPI          string           `json:"openapi"`
	RootServers      []map[string]any `json:"rootServers"`
	OperationServers []map[string]any `json:"operationServers"`
	Expect           struct {
		Resolvable bool   `json:"resolvable"`
		TargetBase string `json:"targetBase"`
	} `json:"expect"`
	Requirements []string `json:"requirements"`
	Basis        string   `json:"basis"`
}

func loadServerTargetBaseTable(t *testing.T) serverTargetBaseTable {
	t.Helper()
	raw, err := os.ReadFile("testdata/server-target-base-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != serverTargetBaseCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the two twin engines)", got, serverTargetBaseCasesDigest)
	}
	var table serverTargetBaseTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.PredicateCases) == 0 || len(table.ResolutionCase) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

// TestServerTargetBasePredicate executes the table's predicate half: does this
// string denote a target address under RFC 3986 plus RFC 9110's host rule?
func TestServerTargetBasePredicate(t *testing.T) {
	table := loadServerTargetBaseTable(t)
	for _, c := range table.PredicateCases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if got := denotesTargetBase(c.URL); got != c.Usable {
				t.Fatalf("denotesTargetBase(%q) = %v, want %v\nbasis: %s", c.URL, got, c.Usable, c.Basis)
			}
		})
	}
}

// serverTargetBaseDocument renders one resolution case as a WHOLE OpenAPI
// document. The document, and not a hand-built openapi3.T, is what the engine
// has to be given: the shipped loader normalizes the raw tree before
// kin-openapi's typed model exists, and a harness that builds the model
// directly measures an engine the project does not ship.
func serverTargetBaseDocument(t *testing.T, c serverTargetResolution) []byte {
	t.Helper()
	operation := map[string]any{
		"operationId": "listThings",
		"responses": map[string]any{
			"200": map[string]any{"description": "ok"},
		},
	}
	if c.OperationServers != nil {
		operation["servers"] = c.OperationServers
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "server target base case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/things": map[string]any{"get": operation},
		},
	}
	if c.RootServers != nil {
		document["servers"] = c.RootServers
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal case document: %v", err)
	}
	return raw
}

// serverTargetBaseResolution loads one case through the engine's own shipped
// loader and resolves the effective server with NO consumer configuration and
// no source location, which is the state synthesis coverage asks about.
func serverTargetBaseResolution(t *testing.T, c serverTargetResolution) (string, bool) {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(serverTargetBaseDocument(t, c)))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/things")
	if item == nil || item.Get == nil {
		t.Fatalf("%s: loaded document has no things operation", c.Name)
	}
	base, err := resolveServer(doc, item, item.Get, nil, "")
	if err != nil {
		return "", false
	}
	return base, true
}

// TestServerTargetBaseResolutionCaseTable executes the table's resolution half
// through the shipped loader.
func TestServerTargetBaseResolutionCaseTable(t *testing.T) {
	for _, c := range loadServerTargetBaseTable(t).ResolutionCase {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			base, ok := serverTargetBaseResolution(t, c)
			if ok != c.Expect.Resolvable {
				t.Fatalf("%s: resolvable = %v, want %v (base %q)\nbasis: %s", c.Name, ok, c.Expect.Resolvable, base, c.Basis)
			}
			if ok && base != c.Expect.TargetBase {
				t.Fatalf("%s: target base = %q, want %q\nbasis: %s", c.Name, base, c.Expect.TargetBase, c.Basis)
			}
		})
	}
}

// TestServerTargetBaseEmittedRequirements is the half only a synthesizing
// engine can execute: the same table's `requirements`, read off the coverage
// entry the shipped synthesis path emits for the target. This is what the
// nhost/nhost and nixopus/nixopus corpus mismatch was measured in.
func TestServerTargetBaseEmittedRequirements(t *testing.T) {
	for _, c := range loadServerTargetBaseTable(t).ResolutionCase {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{
					BindingSpec: bindingSpecForTestDocument(serverTargetBaseDocument(t, c)),
					Content:     openbindings.TextContent(string(serverTargetBaseDocument(t, c))),
				}},
			})
			if err != nil {
				t.Fatalf("%s: synthesize: %v", c.Name, err)
			}
			var got []string
			found := false
			for _, entry := range result.Coverage.Entries {
				if entry.SourceRef == "#/paths/~1things/get" && entry.Scope == synthesize.SynthesisCoverageTarget {
					got, found = entry.Requirements, true
					break
				}
			}
			if !found {
				t.Fatalf("%s: no target coverage entry for the operation", c.Name)
			}
			want := c.Requirements
			if len(got) != len(want) {
				t.Fatalf("%s: requirements = %v, want %v\nbasis: %s", c.Name, got, want, c.Basis)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s: requirements = %v, want %v\nbasis: %s", c.Name, got, want, c.Basis)
				}
			}
		})
	}
}
