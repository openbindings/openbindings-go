package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	openbindings "github.com/openbindings/openbindings-go"
)

// run drives the invocation handle: it reads the single flag/arg object the
// caller writes (or runs the bare command for no-input operations), resolves
// the binary and argv, executes the CLI, and emits the parsed output.
//
// Pre-dispatch failures (unloadable spec, unknown ref, malformed input)
// terminate the handle before the process is spawned. A non-zero exit is a
// terminal ERR_EXECUTION_FAILED carrying the exit code and captured output in
// Details; a missing binary is ERR_SOURCE_CONFIG_ERROR.
func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// Bound the process to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation kills it.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	if usageGenericCredentialPresent(args.Context) {
		inv.FireError(openbindings.NewContextRequiredError(
			"generic credential has no artifact-declared environment-variable destination",
			&openbindings.ContextRequiredDetails{
				Target: args.Source.Location,
				Alternatives: []openbindings.ContextAlternative{{Requirements: []openbindings.ContextRequirement{{
					Type: "auth.apiKey", Description: "supply an explicitly named process-environment mapping",
				}}}},
			},
		))
		return
	}

	// No-input convention: an operation-layer call for an operation that
	// declares no input (InputSchema == nil) closes input on entry and runs
	// the bare command. Otherwise read the single flag/arg object; a bare
	// close (io.EOF) also runs bare (CLI flags are optional).
	var input any
	if args.Binding != nil && args.InputSchema == nil {
		_ = inv.CloseInput()
	} else {
		v, rerr := inv.ReadInput(bctx)
		switch {
		case rerr == io.EOF:
			input = nil
		case rerr != nil:
			inv.FireError(openbindings.AsInvocationError(rerr))
			return
		default:
			input = v
		}
		_ = inv.CloseInput()
	}

	// Direct-binary dispatch (context-metadata hint): SDK-only feature, no
	// spec loaded, ref is a command string. Hooks are NOT consulted on
	// this lane in v1 (stated, not implied).
	if binary := metadataBinary(args.Context); binary != "" {
		e.runDirect(bctx, args, inv, binary, input)
		return
	}

	// Load the bare jdx usage artifact (content, absolute location, or an
	// exec: locator) — the RESTORED pre-wrapper loader, min_usage_version
	// check kept.
	spec, lerr := e.cachedLoadSpec(bctx, args.Source.Location, args.Source.Content)
	if lerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: lerr.Error()})
		return
	}

	// The ref is the format's own grammar: a space-separated command path
	// into the artifact ("context set"; absent = the root command). A
	// whitespace-bearing ref is NOT the root spelling: it flows into
	// findCommand, whose USAGE-D-03 grammar refuses empty segments.
	var cmd *Command
	var inherited []Flag
	var cmdPath []string
	if args.Ref == "" {
		rc := rootCommand(spec)
		if rc == nil {
			rc = &Command{Flags: spec.Flags(), Args: spec.Args()}
		}
		cmd = rc
	} else {
		found, ferr := findCommand(spec, args.Ref)
		if ferr != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: ferr.Error()})
			return
		}
		cmd = found.cmd
		inherited = found.inheritedFlags
		cmdPath = found.path
	}

	meta := spec.Meta()
	binName := meta.Bin
	if binName == "" {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "usage artifact does not define a binary name (bin or name)"})
		return
	}
	if target, present := openbindings.ContextConfiguration(args.Context)["target"]; present {
		text, ok := target.(string)
		if !ok || strings.TrimSpace(text) == "" {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "configuration.target must be a non-empty executable path string"})
			return
		}
		binName = text
	}

	// Complete the site: Target is GUARANTEED on this lane (the kdl's
	// bin/name — consumers' site guards depend on it).
	site := siteFor(args, binName)

	// Route every input field through the seam (specification +
	// configuration: routing is a wire question the usage artifact cannot
	// answer; the assumption is argv, the consumer's FieldRouter overrides
	// per field). Enforcement replaces the wrapper's load-time validation.
	configured, cierr := applyUsageConfiguration(cmd, inherited, input, args.Context, e.Encoders)
	if cierr != nil {
		inv.FireError(cierr)
		return
	}
	routed, rierr := routeFields(site, args.Hooks, cmd, inherited, configured.fields)
	if rierr != nil {
		inv.FireError(rierr)
		return
	}
	defer routed.cleanup()
	if configured.stdin != nil {
		if routed.stdin != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: "more than one field routes to the single stdin channel"})
			return
		}
		routed.stdin = configured.stdin
	}

	var argvInput any
	if input != nil {
		argvInput = routed.fields
	}
	cmdArgs, aerr := buildCLIArgs(cmdPath, cmd, inherited, argvInput)
	if aerr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: aerr.Error()})
		return
	}

	res, runErr := e.executeProcess(bctx, binName, cmdArgs, args.Context, configured.environment, routed.stdin, args.DeliveryUnitLimit())
	// Materialized files only need to outlive the process; the deferred call
	// (idempotent) covers the error paths above.
	routed.cleanup()
	if bctx.Err() != nil {
		// The invocation lifetime ended (caller cancel or deadline): defer to
		// the handle, which classifies a deadline as ERR_TIMEOUT and a cancel as
		// ERR_CANCELLED. Firing our own terminal here would race that one
		// (mirrors the openapi/connect/asyncapi ctx.Err() defer).
		return
	}
	if runErr != nil {
		// Consultation preconditions: spawn failure, cap overflow, and
		// cancellation never consult classify/decode (no completed
		// transport exchange). A missing binary is a configuration error.
		code := openbindings.ErrCodeExecutionFailed
		if errors.Is(runErr, exec.ErrNotFound) {
			code = openbindings.ErrCodeSourceConfigError
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: runErr.Error()})
		return
	}

	// One delivery unit: the completed process. Meta carries the FULL
	// stderr capture (tail-capping applies only to the trailer) and the
	// per-field routing record (x-ob-route provenance).
	exitCode := res.exitCode
	raw := openbindings.RawResult{
		Status: &exitCode,
		Body:   []byte(res.stdout),
		Meta:   resultMeta(res, routed.record),
	}

	// Classify through the seam: the assumption is exit 0; consumers'
	// classifiers widen it (diff-class) or read the code as the output
	// (grep-class, via a decode hook).
	ok, cerr := args.Hooks.Classify(site, raw, builtinClassify)
	if cerr != nil {
		inv.FireError(openbindings.AsInvocationError(cerr))
		return
	}
	if !ok {
		// The format's NATIVE failure: hooks change the verdict, never the
		// error vocabulary. Provenance: the deciding layer is named on the
		// error's decidedBy stamp.
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", res.exitCode),
			Details: map[string]any{"exitCode": res.exitCode, "output": wrapText(res.stdout, res.stderr)},
		})
		return
	}

	output, derr := args.Hooks.DecodeOutput(site, raw, builtinDecodeText)
	if derr != nil {
		inv.FireError(openbindings.AsInvocationError(derr))
		return
	}

	emitWithDiagnostics(inv, output, res, args.Hooks, routed.record)
}

// siteFor completes the core-stamped site with the format-known Target
// (the artifact's binary name). A nil args.Site (direct format-package
// call) gets a minimal site whose Builtin* dispatch stays loud.
func siteFor(args *openbindings.BindingInvocationArgs, binName string) openbindings.InvokeSite {
	var site openbindings.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.BindingSpec = args.Source.BindingSpec
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = binName
	}
	return site
}

// builtinClassify is the exec builtin: success iff exit 0 (the
// documented assumption; the artifact cannot declare exit meanings).
func builtinClassify(_ openbindings.InvokeSite, raw openbindings.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status == 0, nil
}

// builtinDecodeText is the exec builtin: the output value is stdout with
// trailing newlines stripped, exactly as command substitution $(...)
// reads a command's text output — content-independent, lossless,
// recoverable. Machine lanes are a consumer decode hook (or a future
// artifact-native declaration).
func builtinDecodeText(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
	if !utf8.Valid(raw.Body) {
		return nil, fmt.Errorf("process output decode failed: stdout is not valid UTF-8")
	}
	return strings.TrimRight(string(raw.Body), "\r\n"), nil
}

// resultMeta assembles the per-unit Meta: the exit code, the FULL stderr
// capture, and the per-field routing record.
func resultMeta(res *cliResult, record map[string]string) openbindings.Metadata {
	meta := openbindings.Metadata{
		"x-exit-code": {strconv.Itoa(res.exitCode)},
	}
	if res.stderr != "" {
		meta["x-stderr"] = []string{res.stderr}
	}
	for field, route := range record {
		meta["x-ob-route"] = append(meta["x-ob-route"], field+"="+route)
	}
	return meta
}

// BuiltinHooks exposes the exec builtins to the seam's cross-format
// dispatch: text decode and exit-0 classification (both documented
// assumptions — usage is a surface grammar with no invocation-contract
// vocabulary; the assumptions shrink if jdx grows one).
func (e *Invoker) BuiltinHooks() (openbindings.OutputDecoder, openbindings.ResultClassifier) {
	return builtinDecodeText, builtinClassify
}

// emitWithDiagnostics stamps the diagnostics trailer and emits the output.
// The trailer carries the success provenance stamps (the conventions
// record, spec/binding-specs/README.md) — x-ob-decode
// ("assumption/text" | "hook"), x-ob-classify ("assumption/exit-0" |
// "hook"), and the provenance-qualified per-field x-ob-route — plus the
// exec carrier facts (x-exit-code, tail-capped x-stderr). Tier-blind on
// purpose: failure paths are tier-precise; success provenance is not.
// Diagnostics ride trailing metadata, never the output value: the exit code
// and captured stderr (a filter command's human summary). The trailer is
// header-shaped, so stderr is bounded: the LAST maxStderrTrailerBytes (tails
// carry the operative lines), with an explicit truncation marker that also
// fires when the capture itself overflowed.
func emitWithDiagnostics(inv *openbindings.InvocationImpl[any, any], output any, res *cliResult, hooks *openbindings.InvokeHooks, record map[string]string) {
	trailer := openbindings.Metadata{"x-exit-code": {strconv.Itoa(res.exitCode)}}
	decodeStamp, classifyStamp := "assumption/text", "assumption/exit-0"
	if hooks.DecodeDecidedBy() == "hook" {
		decodeStamp = "hook"
	}
	if hooks.ClassifyDecidedBy() == "hook" {
		classifyStamp = "hook"
	}
	trailer["x-ob-decode"] = []string{decodeStamp}
	trailer["x-ob-classify"] = []string{classifyStamp}
	for field, route := range record {
		trailer["x-ob-route"] = append(trailer["x-ob-route"], field+"="+route)
	}
	if res.stderr != "" {
		stderrOut := res.stderr
		truncated := res.stderrTruncated
		if len(stderrOut) > maxStderrTrailerBytes {
			stderrOut = stderrOut[len(stderrOut)-maxStderrTrailerBytes:]
			truncated = true
		}
		trailer["x-stderr"] = []string{stderrOut}
		if truncated {
			trailer["x-stderr-truncated"] = []string{"true"}
		}
	}
	inv.SetTrailer(trailer)
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// runDirect is the direct-binary dispatch path (SDK-only feature, outside
// the format conventions): the ref is split as a command path (USAGE-D-03), every input
// field rides argv as a flag, output decodes under the default text mode.
func (e *Invoker) runDirect(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any], binary string, input any) {
	cmdArgs, err := buildDirectArgsFromRef(args.Ref, input)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()})
		return
	}
	res, runErr := runCLI(ctx, binary, cmdArgs, args.Context, nil, args.DeliveryUnitLimit())
	if ctx.Err() != nil {
		// Defer to the handle for the lifetime terminal (deadline → ERR_TIMEOUT,
		// cancel → ERR_CANCELLED); firing here would race it.
		return
	}
	if runErr != nil {
		code := openbindings.ErrCodeExecutionFailed
		if errors.Is(runErr, exec.ErrNotFound) {
			code = openbindings.ErrCodeSourceConfigError
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: runErr.Error()})
		return
	}
	if res.exitCode != 0 {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("command exited with status %d", res.exitCode),
			Details: map[string]any{"exitCode": res.exitCode, "output": wrapText(res.stdout, res.stderr)},
		})
		return
	}
	output, decodeErr := builtinDecodeText(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte(res.stdout)})
	if decodeErr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRuntime, Message: decodeErr.Error()})
		return
	}
	// Direct-binary dispatch consults no hooks (stated in run); a nil
	// carrier stamps the assumptions, which is what actually decided.
	emitWithDiagnostics(inv, output, res, nil, nil)
}

// metadataBinary extracts the "binary" hint from context metadata.
func metadataBinary(ctx map[string]any) string {
	meta := openbindings.ContextMetadata(ctx)
	if meta == nil {
		return ""
	}
	if b, ok := meta["binary"].(string); ok {
		return b
	}
	return ""
}

func usageGenericCredentialPresent(ctx map[string]any) bool {
	if openbindings.ContextAPIKey(ctx) != "" {
		return true
	}
	if values, ok := ctx["apiKeys"].(map[string]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok && text != "" {
				return true
			}
		}
	}
	return false
}

type usageConfiguredInput struct {
	fields      any
	stdin       []byte
	environment map[string]string
}

// applyUsageConfiguration closes only invocation-time choices the artifact
// leaves open. It never invents field destinations or token encodings.
func applyUsageConfiguration(cmd *Command, inherited []Flag, input any, bindCtx map[string]any, encoders map[string]TokenEncoder) (*usageConfiguredInput, *openbindings.InvocationError) {
	out := &usageConfiguredInput{fields: input, environment: map[string]string{}}
	inputMap := map[string]any{}
	if input != nil {
		var ok bool
		inputMap, ok = openbindings.ToStringAnyMap(input)
		if !ok {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: "usage input must be a JSON object"}
		}
	}
	fields := make(map[string]any, len(inputMap))
	suppliedFields := make(map[string]any, len(inputMap))
	for name, value := range inputMap {
		fields[name] = value
		suppliedFields[name] = value
	}
	if input != nil {
		out.fields = fields
	}

	configuration := openbindings.ContextConfiguration(bindCtx)
	encodeConfig := map[string]any{}
	if raw, present := configuration["encode"]; present {
		var ok bool
		encodeConfig, ok = raw.(map[string]any)
		if !ok {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "configuration.encode must be an object of field-to-encoder names"}
		}
	}
	if rawRoutes, present := configuration["route"]; present {
		routes, ok := rawRoutes.(map[string]any)
		if !ok {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "configuration.route must be an object"}
		}
		stdinField := ""
		for field, raw := range routes {
			entry, ok := raw.(map[string]any)
			if !ok {
				return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: fmt.Sprintf("configuration.route[%q] must be an object", field)}
			}
			kind, _ := entry["kind"].(string)
			value, supplied := fields[field]
			slot, slotKind := findSlot(cmd, inherited, field)
			if slotKind == slotNone {
				return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: fmt.Sprintf("configuration.route names undeclared field %q", field)}
			}
			if !supplied || value == nil {
				continue
			}
			switch kind {
			case "argv", "":
			case "environment":
				envName := ""
				switch definition := slot.(type) {
				case Flag:
					envName = definition.effectiveEnv()
					if definition.Count || definition.Var || definition.valueVariadic() {
						return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("field %q cannot preserve its occurrence structure in one environment value", field)}
					}
				case Arg:
					envName = definition.Env
					if definition.IsVariadic() {
						return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("field %q cannot preserve its occurrence structure in one environment value", field)}
					}
				}
				if envName == "" {
					return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: fmt.Sprintf("field %q has no artifact-declared environment variable", field)}
				}
				text, ok := value.(string)
				if !ok {
					if flag, isFlag := slot.(Flag); isFlag && !flag.Count && flag.ParseUsage().ArgName == "" && len(flag.Args) == 0 {
						if boolean, isBool := value.(bool); isBool {
							text = strconv.FormatBool(boolean)
							ok = true
						}
					}
				}
				if !ok {
					var ierr *openbindings.InvocationError
					text, ierr = configuredUsageEncoding(field, value, encodeConfig, encoders)
					if ierr != nil {
						return nil, ierr
					}
				}
				if configured, present := openbindings.ContextEnvironment(bindCtx)[envName]; present && configured != text {
					return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("environment route for field %q conflicts with configured %s", field, envName)}
				}
				out.environment[envName] = text
				delete(fields, field)
			case "stdin":
				if stdinField != "" {
					return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("fields %q and %q both route to stdin", stdinField, field)}
				}
				stdinField = field
				out.stdin, _ = routeBytes(value)
				operand, _ := entry["operand"].(string)
				if operand == "dash" {
					fields[field] = "-"
				} else {
					delete(fields, field)
				}
			default:
				return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: fmt.Sprintf("configuration.route[%q].kind %q is unsupported", field, kind)}
			}
		}
	}

	effectiveEnvironment := map[string]string{}
	for name, value := range openbindings.ContextEnvironment(bindCtx) {
		effectiveEnvironment[name] = value
	}
	for name, value := range out.environment {
		effectiveEnvironment[name] = value
	}
	if err := validateUsageOverrides(cmd, inherited, suppliedFields); err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	if err := validateUsageRequirements(cmd, inherited, suppliedFields, effectiveEnvironment); err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	if err := validateUsageChoices(cmd, inherited, suppliedFields, effectiveEnvironment); err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}

	for field, value := range fields {
		slot, kind := findSlot(cmd, inherited, field)
		if !needsScalarTokenEncoding(slot, kind, value) {
			continue
		}
		token, ierr := configuredUsageEncoding(field, value, encodeConfig, encoders)
		if ierr != nil {
			return nil, ierr
		}
		fields[field] = token
	}
	return out, nil
}

func configuredUsageEncoding(field string, value any, encodeConfig map[string]any, encoders map[string]TokenEncoder) (string, *openbindings.InvocationError) {
	encoderName, configured := encodeConfig[field].(string)
	if !configured || encoderName == "" {
		return "", &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("field %q has a non-string scalar value but the artifact declares no token encoding and configuration.encode selects none", field)}
	}
	encoder := encoders[encoderName]
	if encoder == nil {
		return "", &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: fmt.Sprintf("configuration.encode[%q] selects unavailable encoder %q", field, encoderName)}
	}
	token, err := encoder(value)
	if err != nil {
		return "", &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: fmt.Sprintf("encode field %q: %v", field, err)}
	}
	return token, nil
}

func needsScalarTokenEncoding(slot any, kind slotKind, value any) bool {
	if value == nil {
		return false
	}
	if _, ok := value.(string); ok {
		return false
	}
	switch kind {
	case slotBoolFlag:
		return false // boolean and count shapes are artifact-declared
	case slotValueFlag:
		if flag, ok := slot.(Flag); ok && (flag.Var || flag.valueVariadic()) {
			return false
		}
		return true
	case slotArg:
		if arg, ok := slot.(Arg); ok && arg.IsVariadic() {
			return false
		}
		return true
	default:
		return false
	}
}

func validateUsageOverrides(cmd *Command, inherited []Flag, fields map[string]any) error {
	flags := cmd.AllFlags(inherited)
	for _, flag := range flags {
		if flag.Overrides == "" {
			continue
		}
		name := flag.PrimaryName()
		if _, left := suppliedFlagValue(flag, fields); !left {
			continue
		}
		for _, reference := range splitUsageReferences(flag.Overrides) {
			other, ok := resolveUsageFlag(flags, reference)
			if !ok {
				return fmt.Errorf("flag %q overrides declaration names unknown flag %q", name, reference)
			}
			if _, right := suppliedFlagValue(other, fields); right {
				return fmt.Errorf("flags %q and %q are both supplied but the artifact declares an overrides relation and JSON object order cannot choose a winner", name, other.PrimaryName())
			}
		}
	}
	return nil
}

func splitUsageReferences(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

func resolveUsageFlag(flags []Flag, reference string) (Flag, bool) {
	normalized := cleanFlagSpelling(reference)
	for _, flag := range flags {
		for _, name := range flag.inputNames() {
			if name == normalized {
				return flag, true
			}
		}
	}
	return Flag{}, false
}

func suppliedFlagValue(flag Flag, fields map[string]any) (any, bool) {
	for _, name := range flag.inputNames() {
		if value, present := fields[name]; present {
			return value, true
		}
	}
	return nil, false
}

func usageFlagSatisfied(flag Flag, fields map[string]any, environment map[string]string) bool {
	if _, supplied := suppliedFlagValue(flag, fields); supplied {
		return true
	}
	if env := flag.effectiveEnv(); env != "" {
		if _, present := environment[env]; present {
			return true
		}
	}
	return flag.effectiveDefault() != nil
}

func usageArgSatisfied(arg Arg, fields map[string]any, environment map[string]string) bool {
	if _, supplied := fields[arg.CleanName()]; supplied {
		return true
	}
	if arg.Env != "" {
		if _, present := environment[arg.Env]; present {
			return true
		}
	}
	return arg.Default != nil
}

func validateUsageRequirements(cmd *Command, inherited []Flag, fields map[string]any, environment map[string]string) error {
	flags := cmd.AllFlags(inherited)
	for _, flag := range flags {
		present := usageFlagSatisfied(flag, fields, environment)
		if flag.Required && !present {
			return fmt.Errorf("required field %s has no caller, environment, or default value", flag.PrimaryName())
		}
		if present {
			continue
		}
		if references := splitUsageReferences(flag.RequiredIf); len(references) > 0 {
			var triggering []string
			for _, reference := range references {
				target, ok := resolveUsageFlag(flags, reference)
				if !ok {
					return fmt.Errorf("field %s requirement names unknown flag %q", flag.PrimaryName(), reference)
				}
				if usageFlagSatisfied(target, fields, environment) {
					triggering = append(triggering, target.PrimaryName())
				}
			}
			if len(triggering) > 0 {
				return fmt.Errorf("field %s is required because %s is present", flag.PrimaryName(), strings.Join(triggering, ", "))
			}
		}
		if references := splitUsageReferences(flag.RequiredUnless); len(references) > 0 {
			var alternatives []string
			anyPresent := false
			for _, reference := range references {
				target, ok := resolveUsageFlag(flags, reference)
				if !ok {
					return fmt.Errorf("field %s requirement names unknown flag %q", flag.PrimaryName(), reference)
				}
				alternatives = append(alternatives, target.PrimaryName())
				anyPresent = anyPresent || usageFlagSatisfied(target, fields, environment)
			}
			if !anyPresent {
				return fmt.Errorf("field %s is required unless one of %s is present", flag.PrimaryName(), strings.Join(alternatives, ", "))
			}
		}
	}
	for _, arg := range cmd.Args {
		if arg.IsRequired() && !usageArgSatisfied(arg, fields, environment) {
			return fmt.Errorf("required field %s has no caller, environment, or default value", arg.CleanName())
		}
	}
	return nil
}

func dynamicUsageChoices(literal []string, envName string, environment map[string]string) ([]string, bool) {
	if len(literal) == 0 && envName == "" {
		return nil, false
	}
	seen := map[string]bool{}
	choices := make([]string, 0, len(literal))
	for _, choice := range literal {
		if !seen[choice] {
			seen[choice] = true
			choices = append(choices, choice)
		}
	}
	if raw, present := environment[envName]; envName != "" && present {
		for _, choice := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			if choice != "" && !seen[choice] {
				seen[choice] = true
				choices = append(choices, choice)
			}
		}
	}
	return choices, true
}

func validateUsageChoiceValue(field string, value any, choices []string) error {
	values := []any{value}
	switch list := value.(type) {
	case []any:
		values = list
	case []string:
		values = make([]any, len(list))
		for i := range list {
			values[i] = list[i]
		}
	}
	for _, candidate := range values {
		text, ok := candidate.(string)
		if !ok || !containsString(choices, text) {
			encoded, _ := json.Marshal(candidate)
			return fmt.Errorf("field %s value %s is outside its artifact-declared choices", field, encoded)
		}
	}
	return nil
}

func validateUsageChoices(cmd *Command, inherited []Flag, fields map[string]any, environment map[string]string) error {
	for _, flag := range cmd.AllFlags(inherited) {
		choices, constrained := dynamicUsageChoices(flag.effectiveChoices(), flag.choicesEnvironment(), environment)
		if !constrained {
			continue
		}
		value, present := suppliedFlagValue(flag, fields)
		if !present {
			if env := flag.effectiveEnv(); env != "" {
				value, present = environment[env]
			}
		}
		if !present && flag.effectiveDefault() != nil {
			value, present = flag.effectiveDefault(), true
		}
		if present {
			if err := validateUsageChoiceValue(flag.PrimaryName(), value, choices); err != nil {
				return err
			}
		}
	}
	for _, arg := range cmd.Args {
		choices, constrained := dynamicUsageChoices(arg.Choices, arg.choicesEnvironment(), environment)
		if !constrained {
			continue
		}
		value, present := fields[arg.CleanName()]
		if !present && arg.Env != "" {
			value, present = environment[arg.Env]
		}
		if !present && arg.Default != nil {
			value, present = arg.Default, true
		}
		if present {
			if err := validateUsageChoiceValue(arg.CleanName(), value, choices); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Invoker) executeProcess(ctx context.Context, binary string, args []string, bindCtx map[string]any, configuredEnvironment map[string]string, stdin []byte, maxStdout int64) (*cliResult, error) {
	environment := map[string]string{}
	for name, value := range openbindings.ContextEnvironment(bindCtx) {
		environment[name] = value
	}
	for name, value := range configuredEnvironment {
		environment[name] = value
	}
	if e.Execute == nil {
		copyContext := make(map[string]any, len(bindCtx)+1)
		for key, value := range bindCtx {
			copyContext[key] = value
		}
		copyContext["environment"] = environment
		return runCLI(ctx, binary, args, copyContext, stdin, maxStdout)
	}
	argv := append([]string{binary}, args...)
	result, err := e.Execute(ctx, ProcessRequest{Argv: argv, Environment: environment, Stdin: stdin, MaxStdout: maxStdout})
	if err != nil {
		return nil, err
	}
	return &cliResult{stdout: result.Stdout, stderr: result.Stderr, exitCode: result.ExitCode}, nil
}

func buildDirectArgsFromRef(ref string, input any) ([]string, error) {
	// USAGE-D-03: a ref is a space-separated command path — single spaces,
	// no quoting mechanism.
	args := strings.Split(ref, " ")
	for _, tok := range args {
		if tok == "" {
			return nil, fmt.Errorf("ref %q is malformed (USAGE-D-03): command-path segments are separated by single spaces", ref)
		}
	}

	if input == nil {
		return args, nil
	}

	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		// A present non-object input is out of contract on the direct lane
		// too (§9.1 / USAGE-P-04): refuse loudly rather than run the bare
		// command with the payload silently dropped.
		return nil, fmt.Errorf("input must be a JSON object; got %s (openbindings.usage@1 §9.1)", jsonKindOf(input))
	}

	names := make([]string, 0, len(inputMap))
	for name := range inputMap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		flagArgs, err := formatFlagWithDef(name, inputMap[name], Flag{})
		if err != nil {
			return nil, fmt.Errorf("format flag %q: %w", name, err)
		}
		args = append(args, flagArgs...)
	}

	return args, nil
}

type findCommandResult struct {
	path           []string
	canonicalPath  []string
	cmd            *Command
	inheritedFlags []Flag
}

var errAmbiguousCommandSpelling = errors.New("ambiguous usage command spelling")

func findCommand(spec *Spec, ref string) (*findCommandResult, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty-string ref is not conformant (USAGE-D-03): the root command is addressed by omitting ref")
	}

	// USAGE-D-03: a ref is a space-separated command path — segments
	// separated by SINGLE spaces, no quoting mechanism, whitespace never
	// collapsed (parity with the direct-binary lane's grammar).
	targetPath := strings.Split(ref, " ")
	for _, seg := range targetPath {
		if seg == "" {
			return nil, fmt.Errorf("ref %q is malformed (USAGE-D-03): command-path segments are separated by single spaces", ref)
		}
	}
	commands := spec.Commands()
	var path []string
	var canonicalPath []string

	// Seed with top-level global flags so they're inherited by all commands.
	var inheritedGlobals []Flag
	for _, f := range spec.Flags() {
		if f.Global {
			inheritedGlobals = append(inheritedGlobals, f)
		}
	}

	for i, target := range targetPath {
		var matches []Command
		for _, cmd := range commands {
			if commandMatchesName(cmd, target) {
				matches = append(matches, cmd)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("command %q not found in usage spec", ref)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("%w: segment %q in ref %q matches %d sibling commands (USAGE-D-03)",
				errAmbiguousCommandSpelling, target, ref, len(matches))
		}
		cmd := matches[0]
		// A command alias is equal in standing to its canonical spelling at
		// the process boundary: emit exactly the ref segment the caller
		// selected, rather than rewriting it to the canonical name.
		path = append(path, target)
		canonicalPath = append(canonicalPath, cmd.Name)
		if i == len(targetPath)-1 {
			cmdCopy := cmd
			return &findCommandResult{
				path:           path,
				canonicalPath:  canonicalPath,
				cmd:            &cmdCopy,
				inheritedFlags: inheritedGlobals,
			}, nil
		}
		for _, f := range cmd.Flags {
			if f.Global {
				inheritedGlobals = append(inheritedGlobals, f)
			}
		}
		commands = cmd.Commands
	}

	return nil, fmt.Errorf("command %q not found in usage spec", ref)
}

// commandMatchesName checks if a command matches a name by its canonical name or any alias.
func commandMatchesName(cmd Command, target string) bool {
	if cmd.Name == target {
		return true
	}
	for _, alias := range cmd.Aliases {
		for _, name := range alias.Names {
			if name == target {
				return true
			}
		}
	}
	return false
}

func buildCLIArgs(cmdPath []string, cmd *Command, inheritedGlobals []Flag, input any) ([]string, error) {
	var args []string
	args = append(args, cmdPath...)

	if input == nil {
		return args, nil
	}

	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		return nil, fmt.Errorf("input must be an object with field names matching the command's flags and args")
	}

	type argDef struct {
		name      string
		cleanName string
		def       Arg
	}
	var argDefs []argDef
	for _, a := range cmd.Args {
		argDefs = append(argDefs, argDef{
			name:      a.Name,
			cleanName: a.CleanName(),
			def:       a,
		})
	}

	processed := make(map[string]bool)

	// Flags emit in artifact declaration order. Input-object member order is
	// semantically absent, so it can never control argv ordering.
	for _, flagDef := range cmd.AllFlags(inheritedGlobals) {
		for _, key := range flagDef.inputNames() {
			if key == "" || processed[key] {
				continue
			}
			value, supplied := inputMap[key]
			if !supplied {
				continue
			}
			flagArgs, err := formatFlagWithDef(key, value, flagDef)
			if err != nil {
				return nil, fmt.Errorf("flag %q: %w", key, err)
			}
			args = append(args, flagArgs...)
			processed[key] = true
			break
		}
	}

	doubleDashInserted := false

	for _, ad := range argDefs {
		value, exists := inputMap[ad.cleanName]
		if !exists {
			continue
		}
		processed[ad.cleanName] = true

		if !doubleDashInserted && (ad.def.DoubleDash == "required" || ad.def.DoubleDash == "optional") {
			args = append(args, "--")
			doubleDashInserted = true
		}

		switch v := value.(type) {
		case []any:
			for _, item := range v {
				args = append(args, argvToken(item))
			}
		case []string:
			args = append(args, v...)
		case string:
			args = append(args, v)
		case nil:
		default:
			args = append(args, argvToken(v))
		}
	}

	for key := range inputMap {
		if !processed[key] {
			return nil, fmt.Errorf("unknown field %q: not defined as a flag or arg in the usage spec for this command", key)
		}
	}

	return args, nil
}

func formatFlagWithDef(name string, value any, flagDef Flag) ([]string, error) {
	prefix := "--"
	if len(name) == 1 {
		prefix = "-"
	}
	flagName := prefix + name

	if flagDef.Count {
		count := 0
		switch v := value.(type) {
		case int:
			count = v
		case int64:
			count = int(v)
		case float64:
			count = int(v)
		case bool:
			if v {
				count = 1
			}
		}
		if count <= 0 {
			return nil, nil
		}
		var args []string
		for i := 0; i < count; i++ {
			args = append(args, flagName)
		}
		return args, nil
	}

	switch v := value.(type) {
	case bool:
		if v {
			return []string{flagName}, nil
		}
		if flagDef.Negate != "" {
			return []string{flagDef.Negate}, nil
		}
		return nil, nil
	case string:
		return []string{flagName, v}, nil
	case []any:
		var args []string
		if flagDef.valueVariadic() {
			args = append(args, flagName)
			for _, item := range v {
				args = append(args, argvToken(item))
			}
		} else {
			for _, item := range v {
				args = append(args, flagName, argvToken(item))
			}
		}
		return args, nil
	case []string:
		args := []string{flagName}
		if flagDef.valueVariadic() {
			return append(args, v...), nil
		}
		args = args[:0]
		for _, item := range v {
			args = append(args, flagName, item)
		}
		return args, nil
	case nil:
		return nil, nil
	default:
		return []string{flagName, argvToken(v)}, nil
	}
}

// argvToken renders one non-string value as one argv token: canonical
// compact JSON (conventions v2 value grammar). Strings pass verbatim and are
// handled before this; JSON canonicalization replaces Go's %v (which printed
// objects as map[k:v] and large floats in exponent form).
func argvToken(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		// Values arrive from JSON, so this is unreachable for wire inputs;
		// fall back to %v rather than panic for hand-constructed ones.
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// cliResult is a completed process's raw capture; decoding stdout into an
// output value is a separate step (decodeStdout) driven by the binding's
// declared stdout mode.
type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
	// stderrTruncated records that the stderr capture overflowed its cap.
	// Diagnostics volume never fails a successful invocation (truncate, mark,
	// carry on); only stdout overflow is fatal, because a truncated document
	// cannot be decoded.
	stderrTruncated bool
}

func runCLI(ctx context.Context, binName string, args []string, bindCtx map[string]any, stdin []byte, maxStdoutBytes int64) (*cliResult, error) {
	cmd := exec.CommandContext(ctx, binName, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	if env := openbindings.ContextEnvironment(bindCtx); len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	// The captured stdout is one delivery unit, consumer-bounded via
	// BindingInvocationArgs.MaxDeliveryUnitBytes (default 10 MiB). The
	// stderr tail is deliberately fixed at maxCLIOutputBytes: diagnostics
	// capture (truncate-and-mark), not a delivery unit.
	stdout := &cappedBuffer{limit: int(maxStdoutBytes)}
	stderr := &tailBuffer{limit: maxCLIOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	if stdout.overflow {
		return nil, fmt.Errorf("command %q output exceeded %d bytes", binName, maxStdoutBytes)
	}

	return &cliResult{
		stdout:          stdout.String(),
		stderr:          stderr.String(),
		exitCode:        exitCode,
		stderrTruncated: stderr.truncated,
	}, nil
}

// wrapText is the raw-capture record used in non-zero-exit error details.
func wrapText(stdoutStr, stderrStr string) map[string]any {
	output := map[string]any{"stdout": stdoutStr}
	if stderrStr != "" {
		output["stderr"] = stderrStr
	}
	return output
}

func resolveCommandArtifact(ctx context.Context, argv []string) (string, error) {
	binName := argv[0]
	args := argv[1:]

	cmd := exec.CommandContext(ctx, binName, args...)
	// Deliberately fixed: an artifact-fetch guard on the usage document
	// produced by the command, not a delivery unit —
	// BindingInvocationArgs.MaxDeliveryUnitBytes does not apply here.
	stdout := &cappedBuffer{limit: maxCLIOutputBytes}
	cmd.Stdout = stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command %q failed: %w", binName, err)
	}
	if stdout.overflow {
		return "", fmt.Errorf("command %q output exceeded %d bytes", binName, maxCLIOutputBytes)
	}

	return stdout.String(), nil
}

// maxCLIOutputBytes bounds the fixed capture lanes: the stderr tail
// (diagnostics, truncate-and-mark), the artifact-fetch guard
// (resolveCommandArtifact), and the input-side routing cap (channels.go) —
// none of which are delivery units, so
// BindingInvocationArgs.MaxDeliveryUnitBytes does not apply to them. The
// invocation lane's stdout capture IS a delivery unit and is
// consumer-bounded (runCLI's maxStdoutBytes).
const maxCLIOutputBytes = 10 << 20 // 10 MiB

// maxStderrTrailerBytes bounds the x-stderr trailer value. Trailing metadata
// is header-shaped and may cross header-framed transports; the full capture
// (up to maxCLIOutputBytes) still travels in non-success error details.
const maxStderrTrailerBytes = 64 << 10 // 64 KiB

// cappedBuffer stops growing past limit and records the overflow, rather than
// letting an unbounded child fill memory. It always reports a full write so the
// child is not killed by a short-write error; the caller inspects overflow
// after the command completes.
//
// bytes.Buffer is held as a field, NOT embedded: embedding would promote
// bytes.Buffer.ReadFrom, which os/exec's io.Copy prefers over Write, bypassing
// the cap entirely.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.overflow {
		return len(p), nil
	}
	if c.buf.Len()+len(p) > c.limit {
		c.overflow = true
		if remain := c.limit - c.buf.Len(); remain > 0 {
			c.buf.Write(p[:remain])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
func (c *cappedBuffer) Len() int       { return c.buf.Len() }

// tailBuffer keeps the MOST RECENT limit bytes: stderr is diagnostics whose
// operative lines (errors, summaries) print last, and whose volume never
// fails a successful invocation — old bytes are discarded as they stream.
type tailBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.truncated = true
		// Copy down rather than re-slice so the discarded prefix is freed.
		keep := t.buf[len(t.buf)-t.limit:]
		t.buf = append(t.buf[:0], keep...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
