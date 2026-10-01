// Package openbindingstest checks a schema evaluator against the evaluator
// contract of package openbindings, as testing/fstest checks an fs.FS:
//
//	func TestEvaluator(t *testing.T) {
//		openbindingstest.TestSchemaEvaluator(t, myevaluator.New(), openbindingstest.Options{
//			Undecided: map[string]string{
//				"adversarial/unreached-lookahead/0": "RE2 has no lookahead; compiles eagerly",
//			},
//		})
//	}
//
// It runs the JSON Schema Test Suite's draft2020-12 tests (pinned, with their
// remote fixtures supplied as Resources) and adversarial cases of its own,
// each through core (a ValueContractCompiler with the evaluator) and on the
// evaluator directly with core's bundle, under two spellings of what core
// generates, so an evaluator that depends on a spelling fails. A wrong
// verdict fails. No verdict fails unless it is a refusal of core's the kit
// pins or Options.Undecided names the case. Problem paths are compared where
// the contract decides them. Every error the evaluator returns is checked
// against its contract, and invariants check concurrency, isolation between
// bundles, retained errors, that nothing is changed or fetched, and
// cancellation. Each group runs as a subtest named by its ID, so -run selects
// groups. Run it under -race, and not as a parallel test: it swaps
// http.DefaultTransport to catch a fetch.
package openbindingstest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/kithook"
)

// Options exempt named cases, each with a reason the report shows. An entry
// names a case by its ID, or the cases of a group by the group's ID: a suite
// group's file path, "#", index, a space, and description
// ("suite/draft2020-12/pattern.json#2 pattern with Unicode property escape
// requires unicode mode"), or "adversarial/" and a group's name. An entry
// naming a case fails when the case needs no exemption, and one naming a
// group when none of its cases does.
type Options struct {
	// Undecided names cases the evaluator may leave without a verdict: a
	// pattern feature its engine lacks, a number beyond its arithmetic.
	Undecided map[string]string
	// Unlocated names cases whose problem paths the evaluator cannot locate
	// as the contract says.
	Unlocated map[string]string
}

// exemption returns the entry of entries that names a case or its group.
func exemption(entries map[string]string, c testCase, g group) (string, string, bool) {
	if reason, ok := entries[c.id]; ok {
		return c.id, reason, true
	}
	reason, ok := entries[g.id]
	return g.id, reason, ok
}

// TestSchemaEvaluator checks an evaluator against the evaluator contract.
// An Undecided or Unlocated entry that names no case, or whose case needs no
// exemption, fails. Each group runs as a subtest. Do not call it from a
// parallel test: it swaps http.DefaultTransport to catch a fetch, and fails
// a test that has called t.Parallel.
func TestSchemaEvaluator(t *testing.T, e openbindings.SchemaEvaluator, o Options) {
	t.Helper()
	check(t, e, o)
}

// check runs the kit, reporting through t, which the kit's own tests replace
// to see what the kit finds.
func check(t testing.TB, e openbindings.SchemaEvaluator, o Options) {
	t.Helper()
	// The network trap swaps http.DefaultTransport; t.Setenv refuses a
	// parallel test, so no test of the package runs beside the swap.
	t.Setenv("OPENBINDINGSTEST_NETWORK_TRAP", "1")
	restore := trapNetwork(t)
	defer restore()
	k := &kit{t: t, e: e, o: o, seen: map[string]bool{}, report: map[string]*tally{}}
	groups, err := suiteGroups()
	if err != nil {
		t.Errorf("openbindingstest: the embedded suite: %v", err)
		return
	}
	groups = append(groups, adversarialGroups()...)
	for _, g := range groups {
		k.inSubtest(g.id, func() { k.runGroup(g) })
	}
	k.inSubtest("invariants", k.invariants)
	k.checkOptions()
	k.logReport()
}

// inSubtest runs f reporting through a subtest of the given name when the
// kit reports through a *testing.T, so an author can run one group with
// -run; otherwise it runs f as it is.
func (k *kit) inSubtest(name string, f func()) {
	parent, ok := k.t.(*testing.T)
	if !ok {
		f()
		return
	}
	ran := false
	parent.Run(name, func(t *testing.T) {
		ran = true
		k.t = t
		defer func() { k.t = parent }()
		f()
	})
	// A subtest -run filters out never starts.
	k.filtered = k.filtered || !ran
}

// outcome is what a validation answers.
type outcome int

const (
	valid outcome = iota
	mismatch
	noVerdict
	coreRefusal
)

func (o outcome) String() string {
	return [...]string{"valid", "mismatch", "no verdict", "core's refusal"}[o]
}

// group is a document and the values one value contract validates.
type group struct {
	// id prefixes the IDs of its cases; tally names the report line it
	// counts toward.
	id, tally string
	document  string
	resources []openbindings.Resource
	// pinned is why core refuses the contract, when it does.
	pinned string
	cases  []testCase
}

type testCase struct {
	id    string
	value string
	want  outcome
	// paths are the problem paths a mismatch may report, one acceptable
	// list each, sorted; nil only checks that every path resolves.
	paths [][]string
}

type kit struct {
	t    testing.TB
	e    openbindings.SchemaEvaluator
	o    Options
	seen map[string]bool
	// usedUndecided and usedUnlocated record the entries a case needed, and
	// pinnedCases the cases core refuses, which need none.
	usedUndecided, usedUnlocated, pinnedCases []string
	report                                    map[string]*tally
	// filtered is whether -run left some group unrun, so the entries of
	// Options cannot be judged against every case.
	filtered bool
}

type tally struct {
	evaluated, pinned, undecided, unlocated int
	reasons                                 []string
}

func (k *kit) tally(name string) *tally {
	if k.report[name] == nil {
		k.report[name] = &tally{}
	}
	return k.report[name]
}

// runGroup validates a group's values through core and directly, under both
// spellings.
func (k *kit) runGroup(g group) {
	t := k.t
	t.Helper()
	compiler, err := openbindings.NewValueContractCompiler(k.e, g.resources...)
	if err != nil {
		t.Errorf("%s: the kit's resources: %v", g.id, err)
		return
	}
	var doc openbindings.Document
	if err := json.Unmarshal([]byte(g.document), &doc); err != nil {
		t.Errorf("%s: the kit's document: %v", g.id, err)
		return
	}
	contracts, err := compiler.Resolve(context.Background(), &doc)
	if err != nil {
		t.Errorf("%s: resolving the kit's document: %v", g.id, err)
		return
	}
	contract, err := contracts.CompileInput(context.Background(), "op")
	if err != nil {
		t.Errorf("%s: %v", g.id, err)
		return
	}
	if err := contract.Err(); errors.Is(err, openbindings.ErrMismatch) {
		t.Errorf("%s: core's standing refusal matches ErrMismatch: %v", g.id, err)
	}
	var direct [2]openbindings.CompiledSchema
	var bundles [2]json.RawMessage
	refused := false
	for spelling := range 2 {
		bundle, refusal := kithook.Bundle(contracts, "op", "input", spelling)
		if refusal != nil {
			refused = true
			continue
		}
		bundles[spelling] = bundle
		compiled, panicked, err := compileDirect(k.e, bundle)
		if panicked != "" {
			t.Errorf("%s: the evaluator panicked compiling core's bundle: %s", g.id, panicked)
			continue
		}
		if !bytes.Equal(bundle, mustBundle(contracts, spelling)) {
			t.Errorf("%s: Compile changed the bundle's Document", g.id)
		}
		k.checkAnswer(g.id+" (Compile)", err, true, context.Background())
		direct[spelling] = compiled
	}
	counts := k.tally(g.tally)
	switch {
	case refused && g.pinned == "":
		t.Errorf("%s: core refuses the contract, which the kit does not pin: %v", g.id, contract.Err())
		return
	case !refused && g.pinned != "":
		t.Errorf("%s: the kit pins core's refusal (%s), but core compiles the contract", g.id, g.pinned)
		return
	case refused:
		counts.pinned += len(g.cases)
		k.seen[g.id] = true
		k.pinnedCases = append(k.pinnedCases, g.id)
		for _, c := range g.cases {
			k.seen[c.id] = true
			k.pinnedCases = append(k.pinnedCases, c.id)
		}
		return
	}
	for _, c := range g.cases {
		k.runCase(g, c, contract, direct, counts)
	}
}

// runCase validates one value and judges the answers.
func (k *kit) runCase(g group, c testCase, contract *openbindings.ValueContract, direct [2]openbindings.CompiledSchema, counts *tally) {
	t := k.t
	t.Helper()
	k.seen[c.id], k.seen[g.id] = true, true
	through := contract.ValidateJSON(context.Background(), []byte(c.value))
	got := classify(through)
	var answers [2]outcome
	var paths [2][]string
	for spelling, compiled := range direct {
		if compiled == nil {
			answers[spelling] = noVerdict
			continue
		}
		value := decode(c.value)
		kept := decode(c.value)
		panicked, answer := validateDirect(compiled, value)
		if panicked != "" {
			t.Errorf("%s: the evaluator panicked validating %s: %s", c.id, c.value, panicked)
			answers[spelling] = noVerdict
			continue
		}
		if !reflect.DeepEqual(value, kept) {
			t.Errorf("%s: Validate changed the value", c.id)
		}
		k.checkAnswer(c.id, answer, false, context.Background())
		answers[spelling] = classifyAnswer(answer)
		paths[spelling] = problemPaths(answer)
	}
	switch {
	case answers[0] != answers[1]:
		t.Errorf("%s on %s: directly %v and %v under the two spellings: an evaluator must not depend on core's spellings", c.id, c.value, answers[0], answers[1])
	case answers[0] != got:
		t.Errorf("%s on %s: through core %v, directly %v: core reads an answer the evaluator contract forbids otherwise (%v)", c.id, c.value, got, answers[0], through)
	}
	entry, reason, excused := exemption(k.o.Undecided, c, g)
	switch {
	case got == c.want && excused && entry == c.id:
		t.Errorf("%s: Options.Undecided names it, but it got its verdict", c.id)
	case got == c.want:
		counts.evaluated++
	case got == noVerdict && excused:
		k.usedUndecided = append(k.usedUndecided, entry)
		counts.undecided++
		counts.reasons = append(counts.reasons, "undecided "+c.id+": "+reason)
		return
	default:
		t.Errorf("%s on %s: %v, want %v: %v", c.id, c.value, got, c.want, through)
		return
	}
	if got != mismatch {
		return
	}
	k.checkPaths(g, c, paths, counts)
}

// checkPaths judges the problem paths the evaluator reported directly, under
// each spelling; a case is located when its paths are under both.
func (k *kit) checkPaths(g group, c testCase, spellings [2][]string, counts *tally) {
	value := decode(c.value)
	isLocated := func(paths []string) bool {
		if c.paths == nil {
			return !slices.ContainsFunc(paths, func(path string) bool {
				_, ok := jsonpointer.Resolve(value, path)
				return !ok
			})
		}
		sorted := slices.Clone(paths)
		sort.Strings(sorted)
		return slices.ContainsFunc(c.paths, func(want []string) bool { return slices.Equal(want, sorted) })
	}
	located, paths, where := true, spellings[0], ""
	for spelling, reported := range spellings {
		if !isLocated(reported) {
			located, paths = false, reported
			if spelling == 1 {
				where = " under core's second spelling"
			}
			break
		}
	}
	entry, reason, excused := exemption(k.o.Unlocated, c, g)
	switch {
	case located && excused && entry == c.id:
		k.t.Errorf("%s: Options.Unlocated names it, but its paths are located", c.id)
	case located:
	case excused:
		k.usedUnlocated = append(k.usedUnlocated, entry)
		counts.unlocated++
		counts.reasons = append(counts.reasons, "unlocated "+c.id+": "+reason)
	case c.paths == nil:
		k.t.Errorf("%s on %s: problem paths %q%s do not all resolve in the value", c.id, c.value, paths, where)
	default:
		k.t.Errorf("%s on %s: problem paths %q%s, want %q", c.id, c.value, paths, where, c.paths)
	}
}

// checkAnswer checks an error the evaluator returned against its contract:
// it never holds a *NoVerdictError or matches ErrNoVerdict, ErrUndefined,
// ErrNoValueContract, ErrOperationNotFound, or ErrInconclusive; it matches
// ErrMismatch only by
// holding a *MismatchError, which Compile's errors never hold; and it matches
// a context error only when ctx is done.
func (k *kit) checkAnswer(id string, err error, fromCompile bool, ctx context.Context) {
	if err == nil {
		return
	}
	var refusal *openbindings.NoVerdictError
	var held *openbindings.MismatchError
	holdsMismatch := errors.As(err, &held) && held != nil
	switch {
	case errors.As(err, &refusal):
		k.t.Errorf("%s: the evaluator returned a *NoVerdictError, core's own type: %v", id, err)
	case errors.Is(err, openbindings.ErrNoVerdict), errors.Is(err, openbindings.ErrUndefined), errors.Is(err, openbindings.ErrNoValueContract), errors.Is(err, openbindings.ErrOperationNotFound), errors.Is(err, openbindings.ErrInconclusive):
		k.t.Errorf("%s: the evaluator's error matches one of core's refusal sentinels: %v", id, err)
	case errors.Is(err, openbindings.ErrMismatch) && !holdsMismatch:
		k.t.Errorf("%s: the evaluator's error matches ErrMismatch without holding a *MismatchError: %v", id, err)
	case fromCompile && holdsMismatch:
		k.t.Errorf("%s: a Compile error holds a *MismatchError: %v", id, err)
	case ctx.Err() == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		k.t.Errorf("%s: the evaluator's error matches a context error while its ctx is live: %v", id, err)
	}
}

// classify reads a result core returned.
func classify(err error) outcome {
	switch {
	case err == nil:
		return valid
	case errors.Is(err, openbindings.ErrMismatch):
		return mismatch
	}
	return noVerdict
}

// classifyAnswer reads an evaluator's answer as the contract defines it.
func classifyAnswer(err error) outcome {
	var held *openbindings.MismatchError
	switch {
	case err == nil:
		return valid
	case errors.As(err, &held) && held != nil:
		return mismatch
	}
	return noVerdict
}

func problemPaths(err error) []string {
	var held *openbindings.MismatchError
	if !errors.As(err, &held) || held == nil {
		return nil
	}
	var out []string
	for _, p := range held.Problems {
		out = append(out, p.InstanceLocation)
	}
	return out
}

// compileDirect and validateDirect call the evaluator as core would,
// recovering a panic, which the kit reports as a failure.
func compileDirect(e openbindings.SchemaEvaluator, bundle json.RawMessage) (openbindings.CompiledSchema, string, error) {
	return compileWithin(context.Background(), e, bundle)
}

func compileWithin(ctx context.Context, e openbindings.SchemaEvaluator, bundle json.RawMessage) (compiled openbindings.CompiledSchema, panicked string, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = fmt.Sprint(r)
		}
	}()
	compiled, err = e.Compile(ctx, openbindings.SchemaBundle{Document: bundle})
	if err != nil {
		compiled = nil
	}
	return compiled, "", err
}

func validateDirect(compiled openbindings.CompiledSchema, value any) (string, error) {
	return validateWithin(context.Background(), compiled, value)
}

func validateWithin(ctx context.Context, compiled openbindings.CompiledSchema, value any) (panicked string, answer error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = fmt.Sprint(r)
		}
	}()
	return "", compiled.Validate(ctx, value)
}

func mustBundle(contracts *openbindings.ValueContracts, spelling int) json.RawMessage {
	bundle, _ := kithook.Bundle(contracts, "op", "input", spelling)
	return bundle
}

// decode reads JSON text as core hands values to an evaluator.
func decode(text string) any {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		panic(fmt.Sprintf("openbindingstest: a case's value is not JSON: %v", err))
	}
	return value
}

// checkOptions fails an exemption that names no case, or whose case needed
// none. A run -run filtered judges only the entries naming cases it ran.
func (k *kit) checkOptions() {
	for _, entries := range []struct {
		name  string
		given map[string]string
		used  []string
	}{{"Undecided", k.o.Undecided, k.usedUndecided}, {"Unlocated", k.o.Unlocated, k.usedUnlocated}} {
		for id := range entries.given {
			switch {
			case !k.seen[id] && k.filtered:
			case !k.seen[id]:
				k.t.Errorf("Options.%s names %q, which is no case", entries.name, id)
			case slices.Contains(k.pinnedCases, id):
				k.t.Errorf("Options.%s names %q, which core refuses, so it needs no exemption", entries.name, id)
			case !slices.Contains(entries.used, id):
				k.t.Errorf("Options.%s names %q, which no case needed", entries.name, id)
			}
		}
	}
}

func (k *kit) logReport() {
	names := make([]string, 0, len(k.report))
	for name := range k.report {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := k.report[name]
		k.t.Logf("%s: %d evaluated (%d unlocated), %d pinned core refusals, %d undecided", name, c.evaluated, c.unlocated, c.pinned, c.undecided)
		for _, reason := range c.reasons {
			k.t.Logf("  %s", reason)
		}
	}
}

// trapNetwork makes any request through http.DefaultTransport fail the kit.
func trapNetwork(t testing.TB) func() {
	previous := http.DefaultTransport
	http.DefaultTransport = trap{t}
	return func() { http.DefaultTransport = previous }
}

type trap struct{ t testing.TB }

var trapOnce sync.Mutex

func (tr trap) RoundTrip(r *http.Request) (*http.Response, error) {
	trapOnce.Lock()
	defer trapOnce.Unlock()
	tr.t.Errorf("the evaluator made a network request: %s", r.URL)
	return nil, errors.New("openbindingstest: the evaluator obtains nothing")
}
