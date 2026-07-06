package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// The exec channel vocabulary: the value space of a FieldRouter for this
// format, exported as constants (an SDK vocabulary, never a document one;
// versioned with the SDK like any API). Semantics preserve the settled
// slotted/slotless ruling.
const (
	// RouteArgv (the default): the value rides its argv slot — strings
	// verbatim, other values compact JSON (the settled value grammar).
	RouteArgv = "argv"
	// RouteStdinDash: bytes to the child's stdin, `-` in the field's slot
	// (the slotted Guideline-13 operand — the filter lane's majority
	// route). The field must map to a value-taking flag or a positional.
	RouteStdinDash = "stdin-dash"
	// RouteStdin: bytes to the child's stdin, nothing on argv (the
	// slotless pure channel). The field must map to NO flag or arg.
	RouteStdin = "stdin"
	// RouteFile: temp-file materialization; the file's path rides the
	// field's slot. Deliberately a regular seekable file, not FIFO
	// process-substitution mechanics.
	RouteFile = "file"
)

// maxRouteBytes bounds a routed field's materialized bytes.
const maxRouteBytes = maxCLIOutputBytes

// routedInput is the result of routing one invocation's input fields:
// the argv-facing field map (with `-`/path substitutions applied), the
// stdin payload (nil when no field rides stdin), a cleanup for any
// materialized temp files, and the per-field routing record (for
// x-ob-route provenance and tests).
type routedInput struct {
	fields  map[string]any
	stdin   []byte
	cleanup func()
	record  map[string]string // field -> effective channel token
}

// routeFields consults the FieldRouter chain for every input field and
// applies the channel mechanics. ARGV-ASSEMBLY ENFORCEMENT (replacing the
// wrapper's deleted load-time unit validation — refusals, never silent
// behavior):
//   - an unrecognized channel token is ErrCodeValidationFailed pre-spawn;
//   - at most one field may ride stdin (either mode);
//   - slot compatibility: routing a boolean/count flag is refused; a
//     RouteStdinDash slot whose declared choices exclude "-" is refused;
//     RouteStdin on a field that DOES map to a flag/arg is refused with
//     the teaching message "did you mean stdin-dash?".
//
// A routed field absent from the input, or present as JSON null, is a
// no-op.
func routeFields(site openbindings.InvokeSite, hooks *openbindings.InvokeHooks, cmd *Command, inherited []Flag, input any) (*routedInput, *openbindings.InvocationError) {
	noop := func() {}
	out := &routedInput{cleanup: noop, record: map[string]string{}}
	if input == nil {
		return out, nil
	}
	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		// Non-object input is rejected downstream by field mapping.
		out.fields = nil
		return out, nil
	}

	fields := make(map[string]any, len(inputMap))
	for k, v := range inputMap {
		fields[k] = v
	}
	out.fields = fields

	var tmpDir string
	usedNames := map[string]bool{}
	stdinField := ""
	fail := func(ie *openbindings.InvocationError) (*routedInput, *openbindings.InvocationError) {
		out.cleanup()
		return nil, ie
	}

	for field, value := range inputMap {
		route, rerr := hooks.RouteField(site, field, value)
		if rerr != nil {
			return fail(openbindings.AsInvocationError(rerr))
		}
		if route == "" || route == RouteArgv {
			out.record[field] = RouteArgv
			continue
		}
		switch route {
		case RouteStdinDash, RouteStdin, RouteFile:
		default:
			// A typo'd token must never quietly put a value on argv.
			return fail(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("field %q: unrecognized channel token %q (want %q, %q, %q, or %q)", field, route, RouteArgv, RouteStdinDash, RouteStdin, RouteFile),
			})
		}

		// Slot compatibility, enforced here because the load-time home is
		// gone.
		slot, kind := findSlot(cmd, inherited, field)
		switch route {
		case RouteStdinDash, RouteFile:
			if kind == slotNone {
				return fail(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("field %q (%s) names no flag or arg of this command", field, route),
				})
			}
			if kind == slotBoolFlag {
				return fail(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("field %q (%s) names a boolean/count flag, which cannot receive a token", field, route),
				})
			}
			if route == RouteStdinDash {
				if cs := slotChoices(slot); len(cs) > 0 && !containsString(cs, "-") {
					return fail(&openbindings.InvocationError{
						Code:    openbindings.ErrCodeValidationFailed,
						Message: fmt.Sprintf("field %q (stdin-dash) names a slot whose choices do not include \"-\"", field),
					})
				}
			}
		case RouteStdin:
			if kind != slotNone {
				return fail(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("field %q (stdin) names a flag or arg; the slotless pure channel requires a field that maps to none — did you mean %q?", field, RouteStdinDash),
				})
			}
		}

		value, present := fields[field]
		if !present || value == nil {
			delete(fields, field)
			out.record[field] = route
			continue
		}
		data, isString := routeBytes(value)
		if len(data) > maxRouteBytes {
			return fail(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("field %q: %d bytes exceeds the %d-byte routing cap", field, len(data), maxRouteBytes),
			})
		}

		switch route {
		case RouteStdinDash, RouteStdin:
			if stdinField != "" {
				return fail(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("fields %q and %q both route to stdin; at most one field may ride it", stdinField, field),
				})
			}
			stdinField = field
			out.stdin = data
			if route == RouteStdinDash {
				fields[field] = "-"
			} else {
				delete(fields, field)
			}
		case RouteFile:
			if tmpDir == "" {
				dir, err := os.MkdirTemp("", "usage-route-*")
				if err != nil {
					return fail(&openbindings.InvocationError{
						Code:    openbindings.ErrCodeExecutionFailed,
						Message: fmt.Sprintf("materialize field %q: %v", field, err),
					})
				}
				tmpDir = dir
				out.cleanup = func() { _ = os.RemoveAll(dir) }
			}
			name := routeFileName(field, isString)
			for i := 2; usedNames[name]; i++ {
				name = fmt.Sprintf("%d-%s", i, routeFileName(field, isString))
			}
			usedNames[name] = true
			path := filepath.Join(tmpDir, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return fail(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeExecutionFailed,
					Message: fmt.Sprintf("materialize field %q: %v", field, err),
				})
			}
			fields[field] = path
		}
		out.record[field] = route
	}
	return out, nil
}

// routeBytes renders a routed value as channel bytes: strings raw, any
// other JSON value as compact JSON (the settled value grammar).
func routeBytes(value any) (data []byte, isString bool) {
	if s, ok := value.(string); ok {
		return []byte(s), true
	}
	data, _ = json.Marshal(value)
	return data, false
}

// routeFileName names a materialized temp file: "<field>.json" for
// JSON-encoded values, bare "<field>" for raw strings (a raw string's
// name must not end in .json), path-hostile runes replaced.
func routeFileName(field string, isString bool) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, field)
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		sanitized = "field"
	}
	if isString {
		if strings.HasSuffix(sanitized, ".json") {
			sanitized = strings.TrimSuffix(sanitized, ".json") + "_json"
		}
		return sanitized
	}
	if strings.HasSuffix(sanitized, ".json") {
		return sanitized
	}
	return sanitized + ".json"
}

// slotKind classification for the enforcement checks above.
type slotKind int

const (
	slotNone slotKind = iota
	slotBoolFlag
	slotValueFlag
	slotArg
)

// findSlot locates the flag or positional arg a field name maps to, and
// whether it can receive a value token.
func findSlot(cmd *Command, inherited []Flag, field string) (any, slotKind) {
	for _, f := range cmd.AllFlags(inherited) {
		names := []string{f.PrimaryName()}
		parsed := f.ParseUsage()
		names = append(names, parsed.Short...)
		names = append(names, parsed.Long...)
		for _, n := range names {
			if n == field && n != "" {
				takesValue := parsed.ArgName != "" || len(f.Args) > 0
				if f.Count || !takesValue {
					return f, slotBoolFlag
				}
				return f, slotValueFlag
			}
		}
	}
	for _, a := range cmd.Args {
		if a.CleanName() == field {
			return a, slotArg
		}
	}
	return nil, slotNone
}

func slotChoices(slot any) []string {
	switch s := slot.(type) {
	case Flag:
		return s.Choices
	case Arg:
		return s.Choices
	}
	return nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
