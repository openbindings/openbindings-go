package openbindings

// Conformance corpus adapter: runs this SDK's binding selection against the
// interfaces repository's operation-invoker selection corpus unmodified
// (conformance/selection — the contract's default policy, the
// context.configuration.selection consumer override, and the explicit
// `binding` bypass).
//
// The corpus is located via OB_INTERFACES_CORPUS or the local-dev sibling
// path (openbindings/interfaces next to openbindings/openbindings-go); the
// tests skip when it is absent.
//
// Each fixture drives the real selection path through the public API:
// the fixture's `supported` set is presented by a stub BindingInvoker (the
// candidate set is formed from the invoker's registered binding
// specifications), and PlanOperation — which shares Invoke's resolution
// (OBI-T-12 name resolution, override, pinning, default policy) — reports
// the selected binding key without invoking anything.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func selectionCorpusDir(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("OB_INTERFACES_CORPUS"); env != "" {
		return env
	}
	dir := filepath.Join("..", "interfaces", "conformance")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("interfaces conformance corpus not found at %s (set OB_INTERFACES_CORPUS)", dir)
	}
	return dir
}

type selectionFixtureFile struct {
	Cluster     string          `json:"cluster"`
	Description string          `json:"description"`
	Tests       []selectionCase `json:"tests"`
}

type selectionCase struct {
	Description string          `json:"description"`
	Document    json.RawMessage `json:"document"`
	Operation   string          `json:"operation"`
	Supported   []string        `json:"supported"`
	Selection   []string        `json:"selection"`
	Binding     string          `json:"binding"`
	Expected    struct {
		Binding string `json:"binding"`
		Error   bool   `json:"error"`
		Kind    string `json:"kind"`
	} `json:"expected"`
}

// selectionSpecStub presents the fixture's `supported` set as its registered
// binding specifications. Selection never invokes, so InvokeBinding is a
// guard against accidental invocation, not a mock.
type selectionSpecStub struct {
	specs []string
}

func (s *selectionSpecStub) BindingSpecs() []BindingSpecInfo {
	infos := make([]BindingSpecInfo, 0, len(s.specs))
	for _, spec := range s.specs {
		infos = append(infos, BindingSpecInfo{BindingSpec: spec})
	}
	return infos
}

func (s *selectionSpecStub) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	return NewErroredInvocation[any, any](&InvocationError{
		Code:    "ERR_NO_MOCK",
		Message: "selection corpus fixtures must not invoke bindings",
	})
}

func TestSelectionCorpus(t *testing.T) {
	dir := filepath.Join(selectionCorpusDir(t), "selection")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus selection dir: %v", err)
	}
	files := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" || entry.Name() == "fixture.schema.json" {
			continue
		}
		files++
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var file selectionFixtureFile
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, tc := range file.Tests {
			tc := tc
			t.Run(file.Cluster+"/"+tc.Description, func(t *testing.T) {
				runSelectionFixture(t, &tc)
			})
		}
	}
	if files == 0 {
		t.Fatalf("no fixture files found in %s", dir)
	}
}

func runSelectionFixture(t *testing.T, tc *selectionCase) {
	t.Helper()

	// Fixture documents are complete, valid OBIs; run them through the
	// SDK's real document validation. A failure here is a corpus defect.
	iface, err := ValidateDocument(tc.Document)
	if err != nil {
		t.Fatalf("fixture document does not validate: %v", err)
	}

	invoker := NewOperationInvoker(&selectionSpecStub{specs: tc.Supported})

	var opts []InvokeOption
	if tc.Binding != "" {
		opts = append(opts, WithBindingKey(tc.Binding))
	}
	if tc.Selection != nil {
		opts = append(opts, WithContext(map[string]any{
			"configuration": map[string]any{"selection": tc.Selection},
		}))
	}

	plan, err := invoker.PlanOperation(context.Background(), iface, tc.Operation, opts...)

	if tc.Expected.Error {
		if err == nil {
			t.Fatalf("expected selection error (%s), selected %q", tc.Expected.Kind, plan.BindingKey)
		}
		// Both error kinds — unknown explicit binding key and no invocable
		// candidate — are contract-named ERR_BINDING_NOT_FOUND.
		var ie *InvocationError
		if !errors.As(err, &ie) {
			t.Fatalf("expected *InvocationError, got %T: %v", err, err)
		}
		if ie.Code != ErrCodeBindingNotFound {
			t.Fatalf("error code = %q, want %q (err: %v)", ie.Code, ErrCodeBindingNotFound, err)
		}
		return
	}

	if err != nil {
		t.Fatalf("expected %q, got error: %v", tc.Expected.Binding, err)
	}
	if plan.BindingKey != tc.Expected.Binding {
		t.Fatalf("selected %q, want %q", plan.BindingKey, tc.Expected.Binding)
	}
}
