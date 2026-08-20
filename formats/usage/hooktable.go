package usage

import (
	"encoding/json"
	"slices"

	"github.com/openbindings/openbindings-go/invoke"
)

// HookTable is the data-shaped consumer configuration for exec bindings —
// the hooksFromTable affordance: per-CLI knowledge organized as a table
// (the shape a community hooks package or an embedder's internal table
// ships) compiled into the three generic hooks. Key rows by CANONICAL
// operation keys — prefer codegen'd signature constants over string
// literals (a stale literal after a rename silently reverts that op to
// the floor; constants follow the rename).
//
// The compiled hooks are format-guarded (they decline for any
// non-usage-family site) and decline for any operation without a row, so
// a table composes safely on a multi-format invoker.
type HookTable struct {
	// DecodeJSON lists operations whose machine lane emits JSON: stdout
	// is parsed strictly (a parse failure is a deliberate terminal —
	// the command failed to honor its machine lane, never a silent
	// string).
	DecodeJSON []string
	// OKExits maps operations to their acceptable exit codes (the
	// diff(1) class: {"compare": {0, 1}}). Codes outside the list are
	// the format's native failure.
	OKExits map[string][]int
	// Routes maps operations to field→channel elections (the filter
	// lane: {"validate": {"locator": RouteStdinDash}}). Channel tokens
	// are validated loudly at argv assembly (unknown tokens, slot
	// compatibility, one-stdin-max) — a misspelled TOKEN can never
	// silently ride argv; a misspelled FIELD name is validated against
	// the artifact's slots when consulted (the op-key asymmetry is
	// documented: a stale op key reverts that op to the floor).
	Routes map[string]map[string]string
}

// Hooks compiles the table into the three generic hooks, suitable for
// invoker-level attachment (inv.OutputDecoder, inv.ResultClassifier,
// inv.FieldRouter) or per-invocation options.
func (t HookTable) Hooks() (invoke.OutputDecoder, invoke.ResultClassifier, invoke.FieldRouter) {
	decodeJSON := make(map[string]bool, len(t.DecodeJSON))
	for _, op := range t.DecodeJSON {
		decodeJSON[op] = true
	}

	decoder := func(site invoke.InvokeSite, raw invoke.RawResult) (any, error) {
		if site.FamilyName() != "usage" || !decodeJSON[site.Operation] {
			return nil, invoke.ErrUseDefault
		}
		if len(raw.Body) == 0 {
			return nil, nil
		}
		var v any
		if err := json.Unmarshal(raw.Body, &v); err != nil {
			return nil, &invoke.InvocationError{
				Code: invoke.ErrCodeResponseError,
			}
		}
		return v, nil
	}

	classifier := func(site invoke.InvokeSite, raw invoke.RawResult) (bool, error) {
		if site.FamilyName() != "usage" || raw.Status == nil {
			return false, invoke.ErrUseDefault
		}
		oks, has := t.OKExits[site.Operation]
		if !has {
			return false, invoke.ErrUseDefault
		}
		return slices.Contains(oks, *raw.Status), nil
	}

	router := func(site invoke.InvokeSite, field string, _ any) string {
		if site.FamilyName() != "usage" {
			return ""
		}
		routes, has := t.Routes[site.Operation]
		if !has {
			return ""
		}
		return routes[field] // "" (absent) declines to the format default
	}

	return decoder, classifier, router
}

// Install attaches the table's compiled hooks at invoker level.
func (t HookTable) Install(inv *invoke.OperationInvoker) {
	inv.OutputDecoder, inv.ResultClassifier, inv.FieldRouter = t.Hooks()
}
