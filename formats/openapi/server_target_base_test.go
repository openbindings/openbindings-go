package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// serverTargetBaseCasesDigest pins the frozen twin case table. The identical
// file is executed by the other two engines that resolve an OpenAPI Server
// Object into a target address; changing it in one engine without the others
// fails here.
const serverTargetBaseCasesDigest = "ddb3478b583c694a707a6f1316329809c3ebf560a0d5275a1adf11ff3fb6b1da"

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
	OutcomeClass string   `json:"outcomeClass"`
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

// serverTargetBaseResolve loads one case through the engine's own shipped
// loader and resolves the effective server with NO consumer configuration and
// no source location, which is the state synthesis coverage asks about.
func serverTargetBaseResolve(t *testing.T, c serverTargetResolution) (string, error) {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(serverTargetBaseDocument(t, c)))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/things")
	if item == nil || item.Get == nil {
		t.Fatalf("%s: loaded document has no things operation", c.Name)
	}
	return resolveServer(doc, item, item.Get, nil, "")
}

// serverTargetBaseOutcomeClass names the table's `outcomeClass` for one
// resolution result. openbindings.openapi@1 §9.3 partitions the two
// unsuccessful classes — a missing selection from a multi-entry list is "the
// retryable context challenge above, never a terminal refusal", while an
// unresolvable server URL "is a pre-dispatch refusal" — and OAPI-P-05 restates
// the refusal half.
//
// This package cannot read the class off an invoke-path mapping the way its
// openapi-client/go twin does, because invocation here delegates to that
// package and the only caller of resolveServer in this one is synthesis
// coverage, which emits `configuration.server` on any error. The class is
// therefore UNOBSERVABLE in this engine and is asserted on the signal type
// alone — the same signal the twin maps — so that an untwinned line in twinned
// files cannot drift unnoticed.
func serverTargetBaseOutcomeClass(err error) string {
	if err == nil {
		return "resolved"
	}
	var cr *configRequired
	if errors.As(err, &cr) {
		return "retryable-context"
	}
	return "refusal"
}

// TestServerTargetBaseResolutionCaseTable executes the table's resolution half
// through the shipped loader.
func TestServerTargetBaseResolutionCaseTable(t *testing.T) {
	for _, c := range loadServerTargetBaseTable(t).ResolutionCase {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			base, err := serverTargetBaseResolve(t, c)
			ok := err == nil
			if ok != c.Expect.Resolvable {
				t.Fatalf("%s: resolvable = %v, want %v (base %q, err %v)\nbasis: %s", c.Name, ok, c.Expect.Resolvable, base, err, c.Basis)
			}
			if ok && base != c.Expect.TargetBase {
				t.Fatalf("%s: target base = %q, want %q\nbasis: %s", c.Name, base, c.Expect.TargetBase, c.Basis)
			}
			if got := serverTargetBaseOutcomeClass(err); got != c.OutcomeClass {
				t.Fatalf("%s: outcome class = %q, want %q (err %v)\nbasis: %s", c.Name, got, c.OutcomeClass, err, c.Basis)
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
			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{
					BindingSpec: BindingSpec,
					Content:     openbindings.TextContent(string(serverTargetBaseDocument(t, c))),
				}},
			})
			if err != nil {
				t.Fatalf("%s: synthesize: %v", c.Name, err)
			}
			var got []string
			found := false
			for _, entry := range result.Coverage.Entries {
				if entry.SourceRef == "#/paths/~1things/get" && entry.Scope == openbindings.SynthesisCoverageTarget {
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
