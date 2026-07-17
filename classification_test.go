package openbindings

import (
	"context"
	"encoding/json"
	"testing"
)

// readmeCategories is the code→category table transcribed VERBATIM from the
// binding-invoker README "Standard error codes" registry. It is the
// independent source of truth: if codeCategory ever drifts from the contract,
// TestCategoryForCode_MatchesREADME fails. Update this table only when the
// README table changes.
var readmeCategories = map[string]Category{
	// Normative codes.
	"CONTEXT_REQUIRED":      CategoryContext,
	"ERR_PROTOCOL":          CategoryProtocol,
	"ERR_TRANSPORT_CLOSED":  CategoryTransient,
	"ERR_CANCELLED":         CategoryCancelled,
	"ERR_VALIDATION_FAILED": CategoryValidation,
	"ERR_BINDING_NOT_FOUND": CategoryPermanent,
	// Convention codes.
	"ERR_AUTH_REQUIRED":              CategoryAuth,
	"ERR_PERMISSION_DENIED":          CategoryAuth,
	"ERR_INVALID_REF":                CategoryPermanent,
	"ERR_REF_NOT_FOUND":              CategoryPermanent,
	"ERR_SCHEMA_UNRESOLVED":          CategoryValidation,
	"ERR_SOURCE_LOAD_FAILED":         CategoryPermanent,
	"ERR_SOURCE_CONFIG_ERROR":        CategoryPermanent,
	"ERR_CONNECT_FAILED":             CategoryTransient,
	"ERR_EXECUTION_FAILED":           CategoryService,
	"ERR_RESPONSE_ERROR":             CategoryService,
	"ERR_STREAM_ERROR":               CategoryTransient,
	"ERR_TIMEOUT":                    CategoryTransient,
	"ERR_OPERATION_NOT_FOUND":        CategoryPermanent,
	"ERR_UNKNOWN_SOURCE":             CategoryPermanent,
	"ERR_TRANSFORM_ERROR":            CategoryValidation,
	"ERR_INPUT_CLOSED":               CategoryProtocol,
	"ERR_INVOCATION_CLOSED":          CategoryProtocol,
	"ERR_TOO_MANY_INPUTS":            CategoryProtocol,
	"ERR_MISSING_INPUT":              CategoryProtocol,
	"ERR_ALREADY_CONSUMED":           CategoryProtocol,
	"ERR_EXPECTED_SINGLE":            CategoryProtocol,
	"ERR_TYPE_MISMATCH":              CategoryValidation,
	"ERR_EVENT_LIMIT_EXCEEDED":       CategoryPermanent,
	"ERR_OPERATION_GRAPH_EXIT":       CategoryService,
	"ERR_UNSUPPORTED_FORMAT_VERSION": CategoryPermanent,
	"ERR_RUNTIME":                    CategoryPermanent,
}

// TestCategoryForCode_MatchesREADME is the anti-drift guard: every code in the
// contract's registry must map to its documented category.
func TestCategoryForCode_MatchesREADME(t *testing.T) {
	for code, want := range readmeCategories {
		if got := categoryForCode(code); got != want {
			t.Errorf("categoryForCode(%q) = %q, want %q (README table)", code, got, want)
		}
	}
	// The map's registry entries must not exceed the README (only the one
	// documented format-specific promotion, TIMEOUT_EXCEEDED, may be extra).
	for code := range codeCategory {
		if _, ok := readmeCategories[code]; ok {
			continue
		}
		if code != "TIMEOUT_EXCEEDED" {
			t.Errorf("codeCategory has %q, which is not in the README registry (drift?)", code)
		}
	}
}

// TestEveryEmittedCodeIsClassified asserts every ErrCode* constant this SDK can
// emit resolves to an explicit, non-fallback category — no code the SDK emits
// silently falls through to the permanent default by accident.
func TestEveryEmittedCodeIsClassified(t *testing.T) {
	emitted := []string{
		ErrCodeCancelled, ErrCodeAlreadyConsumed, ErrCodeExpectedSingle,
		ErrCodeInputClosed, ErrCodeInvocationClosed, ErrCodeTooManyInputs,
		ErrCodeMissingInput, ErrCodeProtocol, ErrCodeTransportClosed,
		ErrCodeContextRequired, ErrCodeTypeMismatch, ErrCodeOperationNotFound,
		ErrCodeBindingNotFound, ErrCodeUnknownSource, ErrCodeAuthRequired,
		ErrCodePermissionDenied, ErrCodeInvalidRef, ErrCodeRefNotFound,
		ErrCodeSourceLoadFailed, ErrCodeSourceConfigError, ErrCodeConnectFailed,
		ErrCodeExecutionFailed, ErrCodeResponseError, ErrCodeStreamError,
		ErrCodeTimeout, ErrCodeTransformError, ErrCodeValidationFailed,
		ErrCodeSchemaUnresolved, ErrCodeRuntime, ErrCodeEventLimitExceeded,
		ErrCodeOperationGraphExit, ErrCodeUnsupportedFormatVersion,
		"TIMEOUT_EXCEEDED",
	}
	for _, code := range emitted {
		if _, ok := codeCategory[code]; !ok {
			t.Errorf("emitted code %q has no explicit category entry", code)
		}
	}
}

func TestCategoryForCode_UnknownDefaultsPermanent(t *testing.T) {
	if got := categoryForCode("ERR_SOME_THIRD_PARTY_CODE"); got != CategoryPermanent {
		t.Errorf("unknown code = %q, want permanent", got)
	}
	if got := categoryForCode(""); got != CategoryPermanent {
		t.Errorf("empty code = %q, want permanent", got)
	}
}

// TestClassify_EffectsAtDispatchAwareCodes pins the effects defaults for the
// transport-transient codes: connect-failed provably did not dispatch (none);
// a timeout, stream error, or transport drop happened after dispatch
// (possible).
func TestClassify_EffectsAtDispatchAwareCodes(t *testing.T) {
	cases := []struct {
		code string
		want Effects
	}{
		{ErrCodeConnectFailed, EffectsNone},
		{ErrCodeTimeout, EffectsPossible},
		{ErrCodeStreamError, EffectsPossible},
		{ErrCodeTransportClosed, EffectsPossible},
		{"TIMEOUT_EXCEEDED", EffectsPossible},
		// Non-transport codes carry no effects default (omitted => possible).
		{ErrCodeValidationFailed, ""},
		{ErrCodeAuthRequired, ""},
		{ErrCodeProtocol, ""},
		{ErrCodeRuntime, ""},
	}
	for _, c := range cases {
		e := classifiedError(c.code, "msg")
		if e.Effects != c.want {
			t.Errorf("classify(%q).Effects = %q, want %q", c.code, e.Effects, c.want)
		}
	}
}

// TestFireError_StampsClassification proves the terminal chokepoint stamps the
// category and effects a consumer reads back through the output stream: a
// connect-failed surfaces as transient/none (auto-retry safe).
func TestFireError_StampsClassification(t *testing.T) {
	inv := NewInvocationImpl[any, any](context.Background())
	inv.FireError(&InvocationError{Code: ErrCodeConnectFailed, Message: "dial tcp: refused"})

	_, err := inv.Outputs().Read(context.Background())
	ie, ok := err.(*InvocationError)
	if !ok {
		t.Fatalf("read returned %T, want *InvocationError", err)
	}
	if ie.Category != CategoryTransient {
		t.Errorf("Category = %q, want transient", ie.Category)
	}
	if ie.Effects != EffectsNone {
		t.Errorf("Effects = %q, want none", ie.Effects)
	}
}

// TestFireError_TimeoutIsPossible: a timeout was dispatched, so it is not
// auto-retry safe for a non-idempotent operation.
func TestFireError_TimeoutIsPossible(t *testing.T) {
	inv := NewInvocationImpl[any, any](context.Background())
	inv.FireError(&InvocationError{Code: ErrCodeTimeout, Message: "deadline exceeded"})
	_, err := inv.Outputs().Read(context.Background())
	ie := err.(*InvocationError)
	if ie.Category != CategoryTransient || ie.Effects != EffectsPossible {
		t.Errorf("timeout classified as %q/%q, want transient/possible", ie.Category, ie.Effects)
	}
}

// TestHTTPError_Effects: a 429/503 proves non-execution (none); a 500 is left
// unset (treated as possible); a 401 is auth and carries no effects.
func TestHTTPError_Effects(t *testing.T) {
	cases := []struct {
		status   int
		wantCode string
		wantCat  Category
		wantEff  Effects
	}{
		{429, ErrCodeExecutionFailed, CategoryService, EffectsNone},
		{503, ErrCodeExecutionFailed, CategoryService, EffectsNone},
		{500, ErrCodeExecutionFailed, CategoryService, ""},
		{404, ErrCodeExecutionFailed, CategoryService, ""},
		{401, ErrCodeAuthRequired, CategoryAuth, ""},
		{403, ErrCodePermissionDenied, CategoryAuth, ""},
	}
	for _, c := range cases {
		e := HTTPError(c.status, "")
		e.classify() // FireError would do this; assert the composed result
		if e.Code != c.wantCode {
			t.Errorf("HTTPError(%d).Code = %q, want %q", c.status, e.Code, c.wantCode)
		}
		if e.Category != c.wantCat {
			t.Errorf("HTTPError(%d).Category = %q, want %q", c.status, e.Category, c.wantCat)
		}
		if e.Effects != c.wantEff {
			t.Errorf("HTTPError(%d).Effects = %q, want %q", c.status, e.Effects, c.wantEff)
		}
	}
}

// TestInvocationError_JSONRoundTrip: category is ALWAYS present on the wire
// (even for a bare struct literal), effects is omitted when unset, and both
// decode back into their typed fields.
func TestInvocationError_JSONRoundTrip(t *testing.T) {
	// A bare literal (no category set) still serializes a derived category.
	bare := &InvocationError{Code: ErrCodeValidationFailed, Message: "bad input"}
	b, err := json.Marshal(bare)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["category"] != string(CategoryValidation) {
		t.Errorf("wire category = %v, want %q", raw["category"], CategoryValidation)
	}
	if _, present := raw["effects"]; present {
		t.Errorf("effects should be omitted for validation, got %v", raw["effects"])
	}

	// A transport code carries its effects default on the wire.
	conn := &InvocationError{Code: ErrCodeConnectFailed, Message: "refused"}
	cb, _ := json.Marshal(conn)
	var craw map[string]any
	_ = json.Unmarshal(cb, &craw)
	if craw["category"] != string(CategoryTransient) || craw["effects"] != string(EffectsNone) {
		t.Errorf("connect wire = %v, want transient/none", craw)
	}

	// Decode back into typed fields.
	var decoded InvocationError
	if err := json.Unmarshal(cb, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Category != CategoryTransient || decoded.Effects != EffectsNone {
		t.Errorf("decoded = %q/%q, want transient/none", decoded.Category, decoded.Effects)
	}
}

// TestContextRequired_NoEffects: the context category is the resolve-and-retry
// hinge; retry is not an effects question, so no effects marker rides.
func TestContextRequired_NoEffects(t *testing.T) {
	e := NewContextRequiredError("need creds", &ContextRequiredDetails{Target: "api.example.com"})
	if e.Category != CategoryContext {
		t.Errorf("Category = %q, want context", e.Category)
	}
	if e.Effects != "" {
		t.Errorf("Effects = %q, want unset", e.Effects)
	}
}
