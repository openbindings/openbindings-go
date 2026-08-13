package openbindings

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// The consumer hook surface: specification + configuration = complete
// invocation. Where a binding-source format's specification is lacking
// (exec CLIs described by jdx usage cannot declare stdout decoding,
// exit-code meaning, or field routing), the gap is made up in CONSUMER
// CONFIGURATION — these hooks — never by OB authoring the missing coverage
// into a document and never by OBI absorbing format conventions.
// The model is documented publicly in the invocation-configuration guide
// (https://openbindings.com/spec/invocation-configuration) and the spec
// repo's formats README (the wire questions each format answers).
//
// Generic SHAPE, protocol-specific HANDLING: one set of signatures serves
// every format; the callback body switches on the site (FormatName,
// Operation, Target). Hooks are process-local — they never ride documents,
// context, or the wire; each hop of a multi-hop composition configures
// itself.

// InvokeSite identifies where a hook is being consulted: which operation,
// through which binding, against which source format and target. Hook
// tables key on its string fields.
//
// Operation is the RESOLVED CANONICAL operation key (post-OBI-T-12), so
// tables keyed on it always fire regardless of the alias the caller used;
// InvokedAs carries the caller's name. Target is the base URL / binary
// name where known — GUARANTEED on the usage lane (the kdl's bin/name),
// best-effort elsewhere.
//
// The unexported fields carry the owning format's builtin hooks, stamped
// by the operation invoker before consultation (see BuiltinDecode).
// Func-valued fields make this struct NON-COMPARABLE in Go — by design;
// key tables on its string fields, never on the site itself.
type InvokeSite struct {
	Operation   string
	InvokedAs   string
	BindingKey  string
	BindingSpec string
	Ref         string
	Target      string

	builtinDecode   OutputDecoder
	builtinClassify ResultClassifier
	seamStamped     bool
}

// FamilyName returns the bare family name of the site's binding
// specification ("openbindings.usage@1" → "usage"; a pre-promotion draft
// token like "graphql" passes through), so hook bodies never string-match
// identifiers.
func (s InvokeSite) FamilyName() string { return familyName(s.BindingSpec) }

// RawResult is ONE DELIVERY UNIT of a completed transport exchange, as the
// decode and classify hooks see it.
//
// Status is the transport's scalar completion status in the format's own
// units (exit code | HTTP status | rpc code) — NIL where no such status
// exists (e.g. a WS frame); it is never fabricated, on either hook path.
// Body is the unit's bytes (response body | stdout | SSE event data | WS
// frame payload). Meta carries invocation-scoped values (headers,
// {x-exit-code, x-stderr}: the FULL stderr capture — tail-capping applies
// only to the trailer) merged with the unit's framing fields where the
// lane has them.
type RawResult struct {
	Status *int
	Body   []byte
	Meta   Metadata
}

// ErrUseDefault is the uniform decline sentinel: a decode or classify hook
// returning it falls to the NEXT precedence tier (per-invocation →
// invoker-level → format built-in). FieldRouter declines with "".
var ErrUseDefault = errors.New("openbindings: use default")

// OutputDecoder turns one delivery unit of a completed, classified-ok
// transport exchange into an output value. Return ErrUseDefault to
// decline. Any other returned error is the invocation's DELIBERATE
// terminal (data failures are not bugs); return an *InvocationError to
// choose the code, else the seam wraps it as ErrCodeResponseError with
// tier provenance.
type OutputDecoder func(site InvokeSite, raw RawResult) (any, error)

// ResultClassifier decides whether a completed transport exchange is a
// success. Return ErrUseDefault to decline. Classify-false produces the
// format's own unsuccessful-completion path — hooks change the verdict,
// never define portable protocol-to-error mappings. A returned non-sentinel
// error is a deliberate terminal
// (ErrCodeExecutionFailed unless it is already an *InvocationError).
type ResultClassifier func(site InvokeSite, raw RawResult) (bool, error)

// FieldRouter places one POST-TRANSFORM input field on a transport
// channel, returning a format-defined channel token (exported as
// constants by each format package — e.g. usage.RouteStdinDash). Return
// "" to decline to the next tier / the format default. An unrecognized
// token is refused loudly at argv assembly (ErrCodeValidationFailed),
// never a silent fallback.
type FieldRouter func(site InvokeSite, field string, value any) string

// ---------------------------------------------------------------------------
// The optional format interface (Builtin* dispatch)
// ---------------------------------------------------------------------------

// BuiltinHooksProvider is the optional interface a format invoker
// implements to expose its builtin decode/classify (the Builtin* dispatch
// half) to the consultation seam. Asserted the way
// TransformEvaluatorWithBindings is. A format that does not consult an axis
// (per its consultation matrix row) returns nil for that builtin; core
// never fabricates one.
type BuiltinHooksProvider interface {
	// BuiltinHooks returns the format's builtin decode and classify for
	// the axes it consults; nil for axes it does not.
	BuiltinHooks() (OutputDecoder, ResultClassifier)
}

// ---------------------------------------------------------------------------
// The seam
// ---------------------------------------------------------------------------

// InvokeHooks is the consultation seam: the ONLY way format packages run
// consumer hooks. It snapshots BOTH tiers (per-invocation and
// invoker-level) at Invoke entry — "resolved once" means immunity to later
// field mutation only; precedence applies at CONSULTATION time by
// decline-chaining. It is nil-safe (a nil receiver or nil slots run the
// builtin), panic-containing (a panicking hook becomes ErrCodeRuntime,
// never a process kill — format invokers run in their own goroutines,
// outside core's Invoke recover), and provenance-carrying (every failure
// names the deciding tier).
//
// Hooks may run multiple times per Invoke (per attempt, per delivery
// unit) and must be effectively pure.
//
// Concurrency contract for the decode/classify success provenance
// (DecodeDecidedBy, ClassifyDecidedBy): the hook chain writes it on the
// BINDING goroutine and core reads it from the RUN goroutine. The pair is
// published atomically, so a reader never observes a torn pair and the read
// needs no external synchronization. A provenance value is readable and
// meaningful only AFTER the corresponding axis has been consulted for the
// delivery unit in hand — both the write (consultation) and the load-bearing
// read (the emit-time output-validation failure teaching) happen on the emit
// path; before the first decode/classify the axis reads "".
type InvokeHooks struct {
	perInvocation hookSlots
	invokerLevel  hookSlots
	// decidedBy holds the decode/classify success-provenance pair — the tiers
	// ("hook" or "builtin") that produced the most recent decode and
	// classification. Tier-blind on purpose (the failure paths are
	// tier-precise; success provenance is not). The decode axis is read by
	// core's contract-decided teaching (an output-validation failure against a
	// floor-stamped schema after a hook decode names the schema election, not
	// the hook); the classify axis by the x-ob-classify success provenance
	// stamp (the conventions record, spec/binding-specs/README.md). Published
	// as one immutable value through an atomic pointer (see the type doc's
	// concurrency contract), so the cross-goroutine read is race-free by
	// construction rather than by an incidental emit-path mutex edge.
	decidedBy atomic.Pointer[decidedTiers]
}

// decidedTiers is the immutable decode/classify provenance pair published
// atomically by InvokeHooks. It is never mutated after Store — each write
// builds a fresh value carrying the other axis forward.
type decidedTiers struct {
	decode   string
	classify string
}

// setDecodeTier records the decode provenance, carrying the classify axis
// forward. Called only on the binding goroutine (a single writer per
// invocation), so the load-then-store is race-free against itself; the atomic
// store is what publishes the pair to the run-goroutine reader.
func (h *InvokeHooks) setDecodeTier(tier string) {
	next := decidedTiers{decode: tier}
	if cur := h.decidedBy.Load(); cur != nil {
		next.classify = cur.classify
	}
	h.decidedBy.Store(&next)
}

// setClassifyTier records the classify provenance, carrying the decode axis
// forward. Same single-writer contract as setDecodeTier.
func (h *InvokeHooks) setClassifyTier(tier string) {
	next := decidedTiers{classify: tier}
	if cur := h.decidedBy.Load(); cur != nil {
		next.decode = cur.decode
	}
	h.decidedBy.Store(&next)
}

// DecodeDecidedBy reports which tier produced the most recent decode:
// "hook", "builtin", or "" (no decode yet).
func (h *InvokeHooks) DecodeDecidedBy() string {
	if h == nil {
		return "builtin"
	}
	if d := h.decidedBy.Load(); d != nil {
		return d.decode
	}
	return ""
}

// ClassifyDecidedBy reports which tier produced the most recent
// classification: "hook", "builtin", or "" (no classification yet).
func (h *InvokeHooks) ClassifyDecidedBy() string {
	if h == nil {
		return "builtin"
	}
	if d := h.decidedBy.Load(); d != nil {
		return d.classify
	}
	return ""
}

type hookSlots struct {
	decode   OutputDecoder
	classify ResultClassifier
	route    FieldRouter
}

// newInvokeHooks snapshots the two tiers. Either tier may be all-nil.
func newInvokeHooks(perInv, invoker hookSlots) *InvokeHooks {
	if perInv.empty() && invoker.empty() {
		return nil // nil carrier = builtins only; the seam is nil-safe
	}
	return &InvokeHooks{perInvocation: perInv, invokerLevel: invoker}
}

func (h hookSlots) empty() bool {
	return h.decode == nil && h.classify == nil && h.route == nil
}

const (
	tierPerInvocation = "per-invocation hook"
	tierInvokerLevel  = "invoker-level hook"
	tierBuiltin       = "format built-in"
)

// hookPanicError converts a recovered hook panic into the contained
// terminal (failure channel iii).
func hookPanicError(_ string, _ any) *InvocationError {
	return &InvocationError{
		Code: ErrCodeRuntime,
	}
}

// hookTerminal wraps a hook's returned error per the failure channels:
// a returned *InvocationError passes through as the DELIBERATE terminal
// (channel i); any other returned error is ALSO a deliberate terminal
// (channel ii — data failures are not bugs), wrapped with the axis's
// native code and tier provenance.
func hookTerminal(_ string, nativeCode string, err error) error {
	var ie *InvocationError
	if errors.As(err, &ie) {
		return ie
	}
	return &InvocationError{
		Code: nativeCode,
	}
}

// DecodeOutput runs the decode chain: per-invocation → invoker-level →
// builtin, each tier declining with ErrUseDefault. The builtin is the
// owning format's (the same function stamped on the site); passing nil
// means the format consults no decode builtin, and an all-decline chain
// is a loud ErrCodeRuntime, never a silent value.
func (h *InvokeHooks) DecodeOutput(site InvokeSite, raw RawResult, builtin OutputDecoder) (out any, err error) {
	tiers := []struct {
		name string
		fn   OutputDecoder
	}{}
	if h != nil {
		tiers = append(tiers,
			struct {
				name string
				fn   OutputDecoder
			}{tierPerInvocation, h.perInvocation.decode},
			struct {
				name string
				fn   OutputDecoder
			}{tierInvokerLevel, h.invokerLevel.decode},
		)
	}
	for _, t := range tiers {
		if t.fn == nil {
			continue
		}
		v, derr := runDecodeHook(t.name, t.fn, site, raw)
		if derr == nil {
			h.setDecodeTier("hook")
			return v, nil
		}
		if errors.Is(derr, ErrUseDefault) {
			continue
		}
		return nil, derr
	}
	if h != nil {
		h.setDecodeTier("builtin")
	}
	if builtin == nil {
		return nil, &InvocationError{
			Code: ErrCodeRuntime,
		}
	}
	v, derr := runDecodeHook(tierBuiltin, builtin, site, raw)
	if derr != nil && errors.Is(derr, ErrUseDefault) {
		return nil, &InvocationError{
			Code: ErrCodeRuntime,
		}
	}
	return v, derr
}

func runDecodeHook(tier string, fn OutputDecoder, site InvokeSite, raw RawResult) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, hookPanicError(tier, r)
		}
	}()
	v, herr := fn(site, raw)
	if herr == nil {
		return v, nil
	}
	if errors.Is(herr, ErrUseDefault) {
		return nil, herr
	}
	return nil, hookTerminal(tier, ErrCodeResponseError, herr)
}

// Classify runs the classify chain with the same tiering and channels;
// the native code for a returned plain error is ErrCodeExecutionFailed.
func (h *InvokeHooks) Classify(site InvokeSite, raw RawResult, builtin ResultClassifier) (ok bool, err error) {
	tiers := []struct {
		name string
		fn   ResultClassifier
	}{}
	if h != nil {
		tiers = append(tiers,
			struct {
				name string
				fn   ResultClassifier
			}{tierPerInvocation, h.perInvocation.classify},
			struct {
				name string
				fn   ResultClassifier
			}{tierInvokerLevel, h.invokerLevel.classify},
		)
	}
	for _, t := range tiers {
		if t.fn == nil {
			continue
		}
		v, cerr := runClassifyHook(t.name, t.fn, site, raw)
		if cerr == nil {
			h.setClassifyTier("hook")
			return v, nil
		}
		if errors.Is(cerr, ErrUseDefault) {
			continue
		}
		return false, cerr
	}
	if h != nil {
		h.setClassifyTier("builtin")
	}
	if builtin == nil {
		return false, &InvocationError{
			Code: ErrCodeRuntime,
		}
	}
	v, cerr := runClassifyHook(tierBuiltin, builtin, site, raw)
	if cerr != nil && errors.Is(cerr, ErrUseDefault) {
		return false, &InvocationError{
			Code: ErrCodeRuntime,
		}
	}
	return v, cerr
}

func runClassifyHook(tier string, fn ResultClassifier, site InvokeSite, raw RawResult) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, hookPanicError(tier, r)
		}
	}()
	v, herr := fn(site, raw)
	if herr == nil {
		return v, nil
	}
	if errors.Is(herr, ErrUseDefault) {
		return false, herr
	}
	return false, hookTerminal(tier, ErrCodeExecutionFailed, herr)
}

// RouteField runs the route chain; "" declines to the next tier and an
// all-decline chain returns "" (the format default — argv, for exec).
// A panicking router is contained as a terminal error (the seam's one
// deviation from the design doc's illustrative string-only signature:
// containment needs an error channel, and argv assembly already has one).
func (h *InvokeHooks) RouteField(site InvokeSite, field string, value any) (route string, err error) {
	if h == nil {
		return "", nil
	}
	for _, t := range []struct {
		name string
		fn   FieldRouter
	}{
		{tierPerInvocation, h.perInvocation.route},
		{tierInvokerLevel, h.invokerLevel.route},
	} {
		if t.fn == nil {
			continue
		}
		r, rerr := runRouteHook(t.name, t.fn, site, field, value)
		if rerr != nil {
			return "", rerr
		}
		if r != "" {
			return r, nil
		}
	}
	return "", nil
}

func runRouteHook(tier string, fn FieldRouter, site InvokeSite, field string, value any) (route string, err error) {
	defer func() {
		if r := recover(); r != nil {
			route, err = "", hookPanicError(tier, r)
		}
	}()
	return fn(site, field, value), nil
}

// ---------------------------------------------------------------------------
// Builtin dispatch (cross-format default arms)
// ---------------------------------------------------------------------------

// BuiltinDecode delegates to the owning format's builtin decoder, stamped
// onto the site by the operation invoker before consultation. Calling it
// for an axis the owning format does not consult, or on a site that never
// passed through the seam, is a LOUD ErrCodeRuntime — never a silent
// no-op. Builtins never return ErrUseDefault.
func BuiltinDecode(site InvokeSite, raw RawResult) (any, error) {
	if !site.seamStamped || site.builtinDecode == nil {
		return nil, builtinAbsentError(site, "decoder")
	}
	return site.builtinDecode(site, raw)
}

// BuiltinClassify delegates to the owning format's builtin classifier;
// same loudness contract as BuiltinDecode.
func BuiltinClassify(site InvokeSite, raw RawResult) (bool, error) {
	if !site.seamStamped || site.builtinClassify == nil {
		return false, builtinAbsentError(site, "classifier")
	}
	return site.builtinClassify(site, raw)
}

func builtinAbsentError(site InvokeSite, axis string) *InvocationError {
	return &InvocationError{
		Code: ErrCodeRuntime,
	}
}

// stampSite completes a core-constructed site with the owning format's
// builtins (via the optional BuiltinHooksProvider) and marks it
// seam-stamped. The FORMAT completes Target on its copy before
// consultation.
func stampSite(site *InvokeSite, formatInvoker BindingInvoker) {
	site.seamStamped = true
	if p, ok := formatInvoker.(BuiltinHooksProvider); ok {
		site.builtinDecode, site.builtinClassify = p.BuiltinHooks()
	}
}

// FloorStamped reports whether a derived output schema carries the
// synthesis floor-stamp ({"type":"string","x-ob":{"floor":...}}) — the
// declaration the diagnostics key on. Content-independent: it reads the
// contract, never payload bytes.
func FloorStamped(schema JSONSchema) bool {
	obj, ok := schema.(map[string]any)
	if !ok {
		// nil (unspecified) and boolean-form schemas carry no stamp.
		return false
	}
	xob, _ := obj["x-ob"].(map[string]any)
	if xob == nil {
		return false
	}
	_, has := xob["floor"]
	return has
}

// AssumptionWarning composes the unvalidated/undiscriminating-assumption
// warning recommended by the conventions record (spec/binding-specs/README.md):
// it fires when an ASSUMPTION decoded the output
// (decodeStamp is the format's x-ob-decode provenance trailer stamp —
// "assumption/<detail>" means the documented default ran; "hook",
// "header/...", "spec/...", or absent mean it did not) AND the output
// contract cannot catch a wrong lane (no schema, a non-discriminating
// schema, or a floor-stamped schema). Format-generic text; "" means no
// warning. Content-independent — it reads provenance and the contract,
// never payload bytes. Keying on the stamp rather than the hook carrier
// keeps it honest: a format that consults no decode axis emits no stamp
// and can never be accused of a decode it did not run.
func AssumptionWarning(decodeStamp string, outputSchema JSONSchema) string {
	if !strings.HasPrefix(decodeStamp, "assumption/") {
		return ""
	}
	obj, isObj := outputSchema.(map[string]any)
	var reason string
	switch {
	case FloorStamped(outputSchema):
		reason = "floor-stamped output schema"
	case outputSchema == nil || (isObj && len(obj) == 0):
		reason = "no output schema"
	case NonDiscriminatingOutput(outputSchema):
		reason = "non-discriminating output schema"
	default:
		return ""
	}
	return fmt.Sprintf("the built-in default decoded the output and the output contract cannot catch a wrong lane (%s); a wrong decode assumption would pass silently — set an OutputDecoder or elect the real output schema", reason)
}

// NonDiscriminatingOutput reports whether an output contract cannot catch
// a wrong decode lane: absent, empty, boolean-true, or admitting bare
// strings. Content-independent contract inspection (the assumption
// warning's trigger arms).
func NonDiscriminatingOutput(schema JSONSchema) bool {
	if schema == nil {
		return true
	}
	if b, isBool := schema.(bool); isBool {
		// Boolean-true accepts everything (cannot catch a wrong lane);
		// boolean-false rejects everything (any wrong decode fails loudly).
		return b
	}
	obj, isObj := schema.(map[string]any)
	if !isObj || len(obj) == 0 {
		return true
	}
	if FloorStamped(schema) {
		return true
	}
	switch t := obj["type"].(type) {
	case string:
		return t == "string"
	case []any:
		for _, v := range t {
			if v == "string" {
				return true
			}
		}
	case nil:
		// No type constraint at the top level and no combinator that
		// obviously excludes strings: treat as non-discriminating unless
		// a combinator is present (a oneOf/anyOf/allOf is at least TRYING
		// to discriminate; honest-but-cheap).
		if obj["oneOf"] == nil && obj["anyOf"] == nil && obj["allOf"] == nil && obj["$ref"] == nil {
			return true
		}
	}
	return false
}
