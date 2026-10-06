# Changelog

## 0.2.0 (working draft)

Implements the 0.2 line of the OpenBindings core specification, applying the
0.2.0 working draft at the spec revision `appliedRevision` in `version.go`
names. Validation reports name that text in `ValidationReport.Release` and
`ValidationReport.Revision` (OBI-T-09). The root module is rebuilt as the
core alone, so code written against 0.1.0 needs the changes listed under
Changed and Removed.

### Added

- **Module layout.** Three independently versioned modules: the core,
  `github.com/openbindings/openbindings-go`, which includes the
  `openbindingstest` package; and, installed separately, `schemaeval`, the
  project's JSON Schema evaluator for value validation, and the optional
  `httpdiscovery`, the HTTP Discovery companion.
- **Document validation.** `ParseDocument(data)` checks the input bytes
  (OBI-D-01), refuses an unsupported version (OBI-T-04), applies the document
  schema (OBI-D-02), and decodes. `ValidateDocument(data)` and
  `Document.Validate()` decide every document rule, OBI-D-01 through OBI-D-13
  (`DocumentRules()`), and return a `ValidationReport`: per-rule `Evidence`,
  the `Violated` and `Inconclusive` rules, `Findings` located by JSON Pointer
  and, from input bytes, by `Position` (offset, line, column), and a
  `Conclusion` of conformant, non-conformant, or conformance-undetermined.
  `ConcludeConformance(evidence)` concludes from evidence a caller supplies.
- **Version support.** `SupportedVersions` (`"0.2.x"`) states the versions
  the SDK supports, and `AuthoringVersion` (`"0.2.0"`) the version a document
  written with it declares. `CheckVersion(v)` makes the OBI-T-04 decision and
  returns a `*VersionRefusalError` for a well-formed version outside the
  supported set, the refusal that `ParseDocument`, `ValidateDocument`,
  `Document.Validate`, `Document.References`, and
  `ValueContractCompiler.Resolve` return instead of interpreting a document.
- **`ErrInconclusive`** marks a call that decided nothing because the input
  is beyond the SDK's own limits, such as nesting deeper than it reads. It is
  neither a conformance conclusion nor a value verdict. A document declaring
  no valid version is not inconclusive: `Document.References` and
  `ValueContractCompiler.Resolve` return its OBI-D-09 violation as a
  `*ValidationError`.
- **Operation resolution and binding lookup.** `Document.ResolveOperation(name)`
  resolves an operation's key or alias (OBI-T-07), and
  `Document.OperationBindings(key)` returns the keys of its bindings, sorted.
- **Schema references.** `Document.References()` lists every `$ref` and
  `$dynamicRef` in the schemas a document contains, each a `Reference` with
  the schema its initial lookup identifies or why it identifies none.
- **Dependencies.** `Document.Dependencies` holds named consumption points
  (`Dependency`, §5.5). `Dependency.Kinds` is an optional any-of kind
  constraint, which `Dependency.AcceptsKind(kind)` applies by exact string
  comparison (OBI-T-01).
- **Value contracts (OBI-T-08).**
  `NewValueContractCompiler(evaluator, resources...)` takes a
  `SchemaEvaluator` the application supplies, and its
  `Resolve(ctx, doc)` resolves a document's schemas into `ValueContracts`,
  whose `CompileInput` and `CompileOutput` return a `ValueContract`. Its
  `Validate` and `ValidateJSON` return nil, a `*MismatchError`
  (`ErrMismatch`), a `*NoVerdictError` (`ErrNoVerdict`, with
  `ErrNoValueContract` and `ErrUndefined` marking two causes), or, for input
  that is not JSON, an error matching none of these. Core hands the
  evaluator one closed JSON Schema 2020-12 `SchemaBundle` per value contract;
  the root module contains no evaluator.
- **`schemaeval`**: the project's `SchemaEvaluator`,
  `schemaeval.New(schemaeval.Options{})`, built on
  `santhosh-tekuri/jsonschema/v6`, with patterns matched as ECMA-262 regular
  expressions by `dlclark/regexp2`. Its package documentation states where it
  gives no verdict.
- **`openbindingstest`**: `TestSchemaEvaluator(t, evaluator, Options)` checks
  any evaluator against the `SchemaEvaluator` contract, using the JSON Schema
  Test Suite's draft2020-12 tests and adversarial cases.
- **`httpdiscovery`** implements the HTTP Discovery companion specification
  v0.1.0. `Client.Discover(ctx, origin)` fetches the document at
  `WellKnownPath` and validates it with `ValidateDocument`, returning a
  `Result` that keeps the report. A non-200 status is a `*StatusError`: a 404
  also matches `ErrNotFound`, and a 401 or 403 is gated discovery, not
  absence. `NewHandler(data, HandlerOptions)` serves a document only once
  core concludes it conformant.
- **Model helpers.** `Present(v)` and `Value(member)` set and read optional
  members. `MediaType` is the OBI media type,
  `application/vnd.openbindings+json`.

### Changed

- **Only the 0.2 line is supported.** A document declaring `0.1.0`, or any
  version outside `0.2.x`, gets a `*VersionRefusalError`.
  `MinSupportedVersion`, `MaxTestedVersion`, `SupportedRange`, and
  `IsSupportedVersion` are replaced by `SupportedVersions`,
  `AuthoringVersion`, and `CheckVersion`. OBI-D-09 checks the declared
  version against the SemVer 2.0.0 grammar, where 0.1.0 accepted only
  `MAJOR.MINOR.PATCH` digits.
- **Types follow the 0.2 document model.**
  - `Interface` is `Document`, and `BindingEntry` is `Binding`. `Document`
    adds `Dependencies` and drops `Roles`, `Security`, and `Transforms`.
  - `Source.Format`, a format token, is `Source.Kind`, an opaque string
    compared exactly (§6, OBI-T-01). `Source.Location` and `Source.Priority`
    are removed, and `Source.Content` is a `json.RawMessage`.
  - `BindingEntry.Ref`, `InputTransform`, and `OutputTransform` give way to
    `Binding.Content`, a `json.RawMessage` read under the source's kind, to
    which core gives no meaning. `BindingEntry.Security` is removed.
  - `BindingEntry.Priority` (`*float64`, lower wins) is `Binding.Preference`
    (`*int64`, where a higher value expresses stronger author preference).
    The SDK selects no binding.
  - `Operation.Idempotent` moves to `Binding.Idempotent`.
    `Operation.Satisfies` and the `Satisfies` type are removed.
  - `JSONSchema` is `any`, not `map[string]any`: an object schema decodes as
    `map[string]any` and a boolean schema as `bool`, with every number a
    `json.Number`. A nil `Operation.Input` or `Output` states no value
    contract (§5.1).
  - `OperationExample.Input` and `Output` are `json.RawMessage`, not `any`.
  - Optional strings and booleans are pointers (`Document.Name`,
    `Operation.Description`, `Binding.Deprecated`, and the rest), and
    optional collections distinguish nil (absent) from empty (present).
- **Decoding is exact.** `json.Unmarshal` into a `Document` fails, rather
  than altering the document, on input that is not valid UTF-8, a repeated
  member name, a string escaping a lone UTF-16 surrogate, a JSON null where
  the model types a value, a missing required string member, or a
  `preference` that is not an integer in range. 0.1.0 kept the last of
  repeated members. Members the model does not type stay in
  `LosslessFields`, as before.
- **Encoding order and escaping.** Each object's typed members are written in
  field order, then its kept members by name; 0.1.0 sorted every member by
  name. `<`, `>`, and `&` are escaped only as the calling encoder escapes
  them; 0.1.0 always escaped them.
- **`Validate` decides the document rules.**
  `Interface.Validate(opts ...ValidateOption) error`, a shape check, is
  `Document.Validate() (ValidationReport, error)`, which decides OBI-D-01
  through OBI-D-13 and takes no options. A nil error means no violation was established, not
  conformance: `ValidationReport.Conclusion` carries the conclusion. A member
  the model does not define whose name does not begin with `x-` is an
  OBI-D-02 violation; 0.1.0 accepted one unless `WithRejectUnknownTypedFields`
  was set.
- **`ValidationError`** carries `Findings []Finding` in place of
  `Problems []string`.
- **`ErrOperationNotFound`** marks a name that resolves to no one operation
  (OBI-T-07). `ValueContracts.CompileInput` and `CompileOutput` return
  errors matching it.
- **Go version and dependencies.** The root module requires Go 1.25.12, not
  Go 1.22, and depends on `github.com/santhosh-tekuri/jsonschema/v6` and
  `golang.org/x/text`, used privately to check the embedded document schema
  and the JSON Schema 2020-12 meta-schemas (OBI-D-02, OBI-D-10). 0.1.0 had
  no dependencies.

### Removed

Everything 0.1.0 carried beside the core is removed. Code for the invocation,
synthesis, source inspection, comparison, and binding layers is preserved on
the `legacy/pre-core-rebuild` branch, and each layer returns as it is rebuilt
on the core from its own authority. The `v0.1.0` and `formats/*/v0.1.0` tags
remain resolvable through the Go module proxy.

- **Invocation**: `OperationExecutor`, `NewOperationExecutor`,
  `OperationExecutionInput`, `BindingExecutor`, `CombineExecutors`,
  `BindingExecutionInput`, `BindingExecutionSource`, `ExecuteOutput`,
  `ExecuteError`, `ExecutionOptions`, `StreamEvent`, `SingleEventChannel`,
  `FailedOutput`, `HTTPErrorOutput`, `BindingSelector`,
  `DefaultBindingSelector`, and the `ErrCode*` constants; `InterfaceClient`,
  `NewInterfaceClient`, `NewUnboundClient`, `InterfaceClientState`, and
  `InterfaceClientOption` with its `With*` options.
- **Transforms**: `Transform`, `TransformOrRef`, `TransformEvaluator`, and
  `TransformEvaluatorWithBindings`. Core defines no transforms; adapting
  values belongs to binding content under a kind.
- **Context and security**: `ContextStore`, `NewMemoryStore`,
  `NormalizeContextKey`, `RedactContext`, `ContextString`,
  `ContextBearerToken`, `ContextAPIKey`, `ContextBasicAuth`,
  `SecurityMethod`, `ResolveSecurity`, `PlatformCallbacks`, `PromptOptions`,
  `FileSelectOptions`, `BrowserRedirectResult`, `ErrAuthCancelled`, and
  `IsAuthCancelled`. Credentials are context, not document members.
- **Synthesis and source inspection**: `InterfaceCreator`, `CombineCreators`,
  `CreateInput`, `CreateSource`, `FormatInfo`, `RefLister`,
  `ListRefsResult`, and `BindableRef`.
- **Comparison**: `CheckInterfaceCompatibility`, `CheckCompatibilityOptions`,
  `CompatibilityIssue`, `CompatibilityIssueKind`, and the `schemaprofile`
  package.
- **Other packages and helpers**: the `canonicaljson` and `formattoken`
  packages; `ContentToBytes`, `DetectFormatVersion`, `IsHTTPURL`,
  `IsOBInterface`, `MaybeJSON`, `SanitizeKey`, `UniqueKey`,
  `ResolveKeyCollision`, and `ToStringAnyMap`.
- **Errors of the removed layers**: `ErrNoExecutor`, `ErrNoCreator`,
  `ErrBindingNotFound`, `ErrNilInterface`, `ErrUnknownSource`,
  `ErrNoTransformEvaluator`, `ErrNoSources`, `ErrTransformRefNotFound`,
  `ErrEmptyTransformExpression`, `ErrContextInsufficient`,
  `ErrResolutionUnavailable`, and `ErrRefListingUnsupported`.
- **Validation options**: `ValidateOption`, `WithRejectUnknownTypedFields`,
  and `WithRequireSupportedVersion`.
- **The root `WellKnownPath`**, now `httpdiscovery.WellKnownPath`.
- **The nine `formats/*` binding modules**: `asyncapi`, `connect`, `graphql`,
  `grpc`, `mcp`, `openapi`, `operationgraph`, `usage`, and `workersrpc`.

### Fixed

- **Re-encoding keeps present empty values and exact numbers.** 0.1.0
  dropped present empty strings, `false` values, and empty arrays, and
  rounded numbers in schemas, example values, and source content to float64
  precision.

## 0.1.0 — 2026-03-31

> The date reflects the content freeze; the v0.1.0 tags were created 2026-04-15. From 0.2.0 on, entry dates are tag dates.

Initial public release.

- Core types for OpenBindings interface documents with lossless JSON round-tripping
- Interface validation with strict mode for unknown fields and format token validation
- Schema compatibility checking (Profile v0.1) with covariant/contravariant directionality and diagnostic reasons
- InterfaceClient for OBI resolution via URL, well-known discovery, and synthesis
- OperationExecutor with format token range matching (caret, exact, versionless)
- Unified stream execution model — every operation returns `<-chan StreamEvent`
- BindingKey support for explicit binding selection bypassing the default selector
- Context store with scheme-agnostic key normalization (`host[:port]`)
- Transform pipeline (input + output) with per-event error propagation
- Security types (`SecurityMethod`, `Interface.Security`, `BindingEntry.Security`) for declaring auth methods on bindings
- `ResolveSecurity` helper for interactive credential resolution via `PlatformCallbacks`
- Standard error codes (`errcodes.go`) for protocol-agnostic error handling
- HTTP error helpers (`HTTPErrorOutput`, `httpErrorCode`) for mapping HTTP status codes to error codes
- Security method pass-through in `OperationExecutor` via `BindingExecutionInput.Security`
- Subpackages: `canonicaljson` (RFC 8785), `formattoken` (semver range matching), `schemaprofile` (Profile v0.1)
