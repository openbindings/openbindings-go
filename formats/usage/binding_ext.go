package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// The usage binding format's transport members, carried as the `x-usage`
// extension on a binding entry (binding conventions v2; see README
// "Conventions"). They declare how the transport-input fields physically
// reach the process (delivery) and what zero-exit stdout is (stdout mode).
// Shape adaptation (field renames, format forcing) stays in the spec-level
// inputTransform; x-usage members are written against the post-transform
// object, because the transform's output IS the transport's input.

const xUsageKey = "x-usage"

// Delivery modes for input fields. An unlisted field rides argv.
const (
	deliveryStdin = "stdin" // value on child stdin; argv slot gets "-"
	deliveryFile  = "file"  // value in a temp file; argv slot gets the path
)

// Stdout modes for zero-exit output decoding. Empty means the heuristic.
const (
	stdoutJSON = "json" // stdout is one JSON value, parsed strictly
	stdoutText = "text" // stdout is the output value, as a raw string
)

// bindingExt is the parsed x-usage member. The zero value is the default
// behavior: argv-only delivery, heuristic stdout decoding.
type bindingExt struct {
	Delivery map[string]string `json:"delivery,omitempty"`
	Stdout   string            `json:"stdout,omitempty"`
}

// parseBindingExt reads and validates the x-usage member from the selected
// binding entry. A nil binding or absent member yields the zero ext.
func parseBindingExt(binding *openbindings.BindingEntry) (*bindingExt, error) {
	if binding == nil || binding.Extensions == nil {
		return &bindingExt{}, nil
	}
	raw, ok := binding.Extensions[xUsageKey]
	if !ok {
		return &bindingExt{}, nil
	}
	var ext bindingExt
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil, fmt.Errorf("parse x-usage binding member: %w", err)
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
		return nil, fmt.Errorf("x-usage delivery routes %d fields to stdin; a process has one stdin", stdinFields)
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
	cleanup := noop
	fail := func(err error) (any, []byte, func(), error) {
		cleanup()
		return nil, nil, noop, err
	}
	for field, mode := range ext.Delivery {
		value, present := substituted[field]
		if !present {
			continue
		}
		data, isString := deliveryBytes(value)
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
