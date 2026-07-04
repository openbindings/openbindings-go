package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// The usage binding format's transport members, carried as the `x-usage`
// extension on a binding entry (binding conventions v2; see README
// "Conventions"). They declare the binding author's per-command elections:
// how each transport-input field physically reaches the process (delivery),
// what zero-exit stdout is (stdout mode), and which exit codes are success
// (exit). Shape adaptation (field renames, format forcing) stays in the
// spec-level inputTransform; x-usage members are written against the
// post-transform object, because the transform's output IS the transport's
// input.
//
// Parsing is fail-closed (conventions v2 forward rule): unknown x-usage
// members and unknown mode values are invocation errors, never ignored, so
// a tool at conventions level N loudly refuses a binding authored against
// level N+1 instead of half-applying it.

const xUsageKey = "x-usage"

// ConventionsVersion is the usage binding-format conventions level this
// package implements (the README's Conventions section is the authority).
// It versions the transport conventions, independent of the usage@ artifact
// token, mirroring the Min/MaxSupportedVersion idiom for spec versions.
const ConventionsVersion = 2

// Delivery modes for input fields. An unlisted field rides argv.
const (
	deliveryStdin = "stdin" // value on child stdin; mapped argv slot gets "-", unmapped fields emit nothing
	deliveryFile  = "file"  // value in a temp file; argv slot gets the path
)

// Stdout modes for zero-exit output decoding. Empty means the heuristic.
const (
	stdoutJSON = "json" // stdout is one JSON value, parsed strictly
	stdoutText = "text" // stdout is the output value; trailing newlines stripped
)

// maxDeliveryBytes bounds a single routed field's materialized bytes (stdin
// payload or temp file), the input-side sibling of maxCLIOutputBytes: a
// document too large to round-trip back out is refused on the way in.
const maxDeliveryBytes = maxCLIOutputBytes

// bindingExt is the parsed x-usage member. The zero value is the default
// behavior: argv-only delivery, heuristic stdout decoding, exit 0 = success.
type bindingExt struct {
	Delivery map[string]string `json:"delivery,omitempty"`
	Stdout   string            `json:"stdout,omitempty"`
	Exit     *exitExt          `json:"exit,omitempty"`
}

// exitExt classifies exit codes. OK lists the codes that are successful
// completions (default [0]) — diff(1)-style CLIs exit 1 for "differences
// found", a result, not a failure.
type exitExt struct {
	OK []int `json:"ok"`
}

// exitOK reports whether code is a declared-success exit.
func (e *bindingExt) exitOK(code int) bool {
	if e.Exit == nil || len(e.Exit.OK) == 0 {
		return code == 0
	}
	for _, ok := range e.Exit.OK {
		if code == ok {
			return true
		}
	}
	return false
}

// stdinField returns the name of the stdin-routed field, if any.
func (e *bindingExt) stdinField() string {
	for field, mode := range e.Delivery {
		if mode == deliveryStdin {
			return field
		}
	}
	return ""
}

// parseBindingExt reads and validates the x-usage member from the selected
// binding entry. A nil binding or absent member yields the zero ext.
// Validation is static: it judges the declaration alone, regardless of which
// fields the input carries.
func parseBindingExt(binding *openbindings.BindingEntry) (*bindingExt, error) {
	if binding == nil || binding.Extensions == nil {
		return &bindingExt{}, nil
	}
	raw, ok := binding.Extensions[xUsageKey]
	if !ok {
		return &bindingExt{}, nil
	}
	var ext bindingExt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ext); err != nil {
		return nil, fmt.Errorf("parse x-usage binding member (unknown members are refused, not ignored — conventions %d): %w", ConventionsVersion, err)
	}
	stdinFields := 0
	for field, mode := range ext.Delivery {
		switch mode {
		case deliveryStdin:
			stdinFields++
		case deliveryFile:
		default:
			return nil, fmt.Errorf("x-usage delivery for field %q: unknown mode %q (want %q or %q)", field, mode, deliveryStdin, deliveryFile)
		}
	}
	if stdinFields > 1 {
		return nil, fmt.Errorf("x-usage delivery declares %d stdin fields; a binding must not declare more than one", stdinFields)
	}
	switch ext.Stdout {
	case "", stdoutJSON, stdoutText:
	default:
		return nil, fmt.Errorf("x-usage stdout: unknown mode %q (want %q or %q)", ext.Stdout, stdoutJSON, stdoutText)
	}
	return &ext, nil
}

// applyDelivery reroutes delivery-listed input fields off argv. The routed
// field keeps its flag/arg mapping but its value is substituted — "-" for
// stdin, an absolute temp-file path for file — and the real bytes are carried
// out of band. Returns the substituted input, the stdin payload (nil when
// nothing rides stdin), and a cleanup for materialized files. Input passes
// through untouched when nothing routes; a listed field absent from the input
// — or present with JSON null, matching the argv grammar's null-omitted rule —
// is a no-op (optional fields).
func applyDelivery(input any, ext *bindingExt) (any, []byte, func(), error) {
	noop := func() {}
	if len(ext.Delivery) == 0 || input == nil {
		return input, nil, noop, nil
	}
	inputMap, ok := openbindings.ToStringAnyMap(input)
	if !ok {
		// Non-object input is rejected downstream by field mapping.
		return input, nil, noop, nil
	}

	substituted := make(map[string]any, len(inputMap))
	for k, v := range inputMap {
		substituted[k] = v
	}

	var stdin []byte
	var tmpDir string
	usedNames := make(map[string]bool)
	cleanup := noop
	fail := func(err error) (any, []byte, func(), error) {
		cleanup()
		return nil, nil, noop, err
	}
	for field, mode := range ext.Delivery {
		value, present := substituted[field]
		if !present || value == nil {
			// null routes nowhere, exactly as it rides no argv slot. Remove
			// it so the field mapper doesn't see a null either.
			delete(substituted, field)
			continue
		}
		data, isString := deliveryBytes(value)
		if len(data) > maxDeliveryBytes {
			return fail(fmt.Errorf("field %q: %d bytes exceeds the %d-byte delivery cap", field, len(data), maxDeliveryBytes))
		}
		switch mode {
		case deliveryStdin:
			stdin = data
			substituted[field] = "-"
		case deliveryFile:
			if tmpDir == "" {
				dir, err := os.MkdirTemp("", "usage-delivery-*")
				if err != nil {
					return fail(fmt.Errorf("materialize field %q: %w", field, err))
				}
				tmpDir = dir
				cleanup = func() { _ = os.RemoveAll(dir) }
			}
			name := deliveryFileName(field, isString)
			// Sanitization can collide distinct field names; disambiguate
			// with an ordinal so every field gets its own file.
			for i := 2; usedNames[name]; i++ {
				name = fmt.Sprintf("%d-%s", i, deliveryFileName(field, isString))
			}
			usedNames[name] = true
			path := filepath.Join(tmpDir, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return fail(fmt.Errorf("materialize field %q: %w", field, err))
			}
			substituted[field] = path
		}
	}
	return substituted, stdin, cleanup, nil
}

// deliveryBytes renders a routed field's value as file/stdin bytes: a string
// is written raw (the value already is the document text); any other JSON
// value is written as its compact JSON serialization. Also reports whether
// the value was a raw string (which drives the temp-file name).
func deliveryBytes(value any) (data []byte, isString bool) {
	if s, ok := value.(string); ok {
		return []byte(s), true
	}
	// Values arrive from JSON, so marshaling cannot fail.
	data, _ = json.Marshal(value)
	return data, false
}

// deliveryFileName names a materialized temp file after its field —
// "<field>.json" for JSON-encoded values, bare "<field>" for raw strings
// (their format is opaque). Post-transform field names are author-chosen,
// so path-hostile runes are replaced rather than trusted.
func deliveryFileName(field string, isString bool) string {
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
		return sanitized
	}
	return sanitized + ".json"
}
