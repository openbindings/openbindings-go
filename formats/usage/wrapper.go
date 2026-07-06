package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/formattoken"
)

// The openbindings.usage binding-unit format: a JSON document that wraps a
// pristine jdx usage-spec artifact and adds the invocation semantics the
// artifact cannot express, so (source, ref) resolves to a complete
// invocation recipe. Authority: spec/formats/usage/openbindings.usage.md.

const (
	// WrapperToken is the exact format token wrapper documents carry.
	WrapperToken = "openbindings.usage@0.1.0"
	// WrapperRange is the token range this implementation claims.
	WrapperRange = "openbindings.usage@^0.1.0"
	// WrapperVersion is the format version this implementation authors.
	WrapperVersion = "0.1.0"
	// ArtifactRange is the accepted jdx usage-spec artifact range
	// (openbindings.usage.md §12).
	ArtifactRange = "usage@^2.0.0"
)

// unitVersionKey is the per-unit version member (§2 signal 2).
const unitVersionKey = "openbindings.usage"

// Delivery modes (§5). An unlisted field rides argv.
const (
	deliveryStdinDash = "stdin-dash" // bytes to stdin; mapped slot receives "-"
	deliveryStdin     = "stdin"      // bytes to stdin; nothing on argv (slotless)
	deliveryFile      = "file"       // bytes to a temp file; slot receives the path
)

// Stdout modes (§6). Absent means text.
const (
	stdoutJSON = "json"
	stdoutText = "text"
	stdoutNone = "none"
)

// maxDeliveryBytes bounds a routed field's materialized bytes (§5).
const maxDeliveryBytes = maxCLIOutputBytes

var unitNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// Wrapper is a parsed openbindings.usage document with its artifact loaded.
type Wrapper struct {
	Spec        WrapperSpec
	Units       map[string]*Unit
	Description string

	kdl *Spec // the parsed artifact
}

// WrapperSpec is the wrapped-artifact member (§3).
type WrapperSpec struct {
	Format   string
	Content  string
	Location string
	Hash     string
}

// Unit is the value a binding ref resolves to (§4).
type Unit struct {
	Version     string
	Command     string
	Delivery    map[string]string
	Stdout      string
	Exit        *ExitSpec
	Description string
}

// ExitSpec classifies exit codes (§7).
type ExitSpec struct {
	OK     []int
	Values map[string]any // decimal code -> literal output value
}

// exitOK reports whether code is a declared-ok exit for the unit.
func (u *Unit) exitOK(code int) bool {
	if u == nil || u.Exit == nil {
		return code == 0
	}
	for _, ok := range u.Exit.OK {
		if code == ok {
			return true
		}
	}
	return false
}

// exitValue returns the literal output value mapped to code, if any (§7).
func (u *Unit) exitValue(code int) (any, bool) {
	if u == nil || u.Exit == nil || u.Exit.Values == nil {
		return nil, false
	}
	v, ok := u.Exit.Values[strconv.Itoa(code)]
	return v, ok
}

// ---------------------------------------------------------------------------
// Parsing (fail-closed: unknown non-x- members refuse at every level, §3)
// ---------------------------------------------------------------------------

// objectFields decodes raw into a member map, enforcing the unknown-member
// policy against the allowed set: unknown non-x- members refuse, x- members
// are ignored (and dropped here; carriage-level preservation is the host
// document's concern).
func objectFields(raw json.RawMessage, where string, allowed ...string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range m {
		if strings.HasPrefix(k, "x-") {
			continue
		}
		if !ok[k] {
			return nil, fmt.Errorf("%s: unknown member %q (unknown non-x- members are refused, never ignored)", where, k)
		}
	}
	return m, nil
}

func decodeString(m map[string]json.RawMessage, key, where string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s.%s: %w", where, key, err)
	}
	return s, nil
}

// ParseWrapper parses an openbindings.usage document from its JSON-object
// content form (map), or its textual form (string / []byte). The artifact is
// loaded and verified per §3; units are validated per §10 lazily, per unit,
// at resolution time (failure granularity is per-unit).
func ParseWrapper(content any) (*Wrapper, error) {
	var data []byte
	switch c := content.(type) {
	case []byte:
		data = c
	case string:
		data = []byte(c)
	case json.RawMessage:
		data = c
	default:
		// The spec's JSON-object content form arrives as a decoded map.
		b, err := json.Marshal(content)
		if err != nil {
			return nil, fmt.Errorf("openbindings.usage: unsupported content type %T", content)
		}
		data = b
	}

	root, err := objectFields(data, "openbindings.usage document", "spec", "units", "description")
	if err != nil {
		return nil, err
	}
	if root["spec"] == nil {
		return nil, fmt.Errorf("openbindings.usage document: required member \"spec\" is missing")
	}
	if root["units"] == nil {
		return nil, fmt.Errorf("openbindings.usage document: required member \"units\" is missing")
	}

	w := &Wrapper{Units: map[string]*Unit{}}
	if w.Description, err = decodeString(root, "description", "document"); err != nil {
		return nil, err
	}

	// spec member.
	sm, err := objectFields(root["spec"], "spec", "format", "content", "location", "hash")
	if err != nil {
		return nil, err
	}
	if w.Spec.Format, err = decodeString(sm, "format", "spec"); err != nil {
		return nil, err
	}
	if w.Spec.Content, err = decodeString(sm, "content", "spec"); err != nil {
		return nil, err
	}
	if w.Spec.Location, err = decodeString(sm, "location", "spec"); err != nil {
		return nil, err
	}
	if w.Spec.Hash, err = decodeString(sm, "hash", "spec"); err != nil {
		return nil, err
	}

	// units map.
	var units map[string]json.RawMessage
	if err := json.Unmarshal(root["units"], &units); err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}
	for name, raw := range units {
		if !unitNameRe.MatchString(name) {
			return nil, fmt.Errorf("units: name %q does not match the identifier pattern", name)
		}
		u, err := parseUnit(raw, name)
		if err != nil {
			return nil, err
		}
		w.Units[name] = u
	}

	return w, nil
}

func parseUnit(raw json.RawMessage, name string) (*Unit, error) {
	where := "unit " + strconv.Quote(name)
	m, err := objectFields(raw, where, unitVersionKey, "command", "delivery", "stdout", "exit", "description")
	if err != nil {
		return nil, err
	}
	u := &Unit{}

	if u.Version, err = decodeString(m, unitVersionKey, where); err != nil {
		return nil, err
	}
	if u.Version == "" {
		return nil, fmt.Errorf("%s: required member %q is missing", where, unitVersionKey)
	}
	if err := wrapperVersionSupported(u.Version); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}

	cmdRaw, ok := m["command"]
	if !ok {
		return nil, fmt.Errorf("%s: required member \"command\" is missing", where)
	}
	if err := json.Unmarshal(cmdRaw, &u.Command); err != nil {
		return nil, fmt.Errorf("%s.command: %w", where, err)
	}

	if u.Description, err = decodeString(m, "description", where); err != nil {
		return nil, err
	}

	if dRaw, ok := m["delivery"]; ok {
		if err := json.Unmarshal(dRaw, &u.Delivery); err != nil {
			return nil, fmt.Errorf("%s.delivery: %w", where, err)
		}
		stdinCount := 0
		for field, mode := range u.Delivery {
			switch mode {
			case deliveryStdinDash, deliveryStdin:
				stdinCount++
			case deliveryFile:
			default:
				return nil, fmt.Errorf("%s.delivery[%q]: unknown mode %q (want %q, %q, or %q)", where, field, mode, deliveryStdinDash, deliveryStdin, deliveryFile)
			}
		}
		if stdinCount > 1 {
			return nil, fmt.Errorf("%s: %d fields declare a stdin mode; a unit must not declare more than one", where, stdinCount)
		}
	}

	if u.Stdout, err = decodeString(m, "stdout", where); err != nil {
		return nil, err
	}
	switch u.Stdout {
	case "", stdoutJSON, stdoutText, stdoutNone:
	default:
		return nil, fmt.Errorf("%s.stdout: unknown mode %q (want %q, %q, or %q)", where, u.Stdout, stdoutJSON, stdoutText, stdoutNone)
	}

	if eRaw, ok := m["exit"]; ok {
		em, err := objectFields(eRaw, where+".exit", "ok", "values")
		if err != nil {
			return nil, err
		}
		e := &ExitSpec{}
		okRaw, present := em["ok"]
		if !present {
			return nil, fmt.Errorf("%s.exit: required member \"ok\" is missing (an exit member with no ok list is a nonsense declaration)", where)
		}
		if err := json.Unmarshal(okRaw, &e.OK); err != nil {
			return nil, fmt.Errorf("%s.exit.ok: %w", where, err)
		}
		if len(e.OK) == 0 {
			return nil, fmt.Errorf("%s.exit.ok: must be non-empty (rejected fail-closed)", where)
		}
		for _, c := range e.OK {
			if c < 0 || c > 255 {
				return nil, fmt.Errorf("%s.exit.ok: code %d out of range 0-255", where, c)
			}
		}
		if vRaw, present := em["values"]; present {
			if err := json.Unmarshal(vRaw, &e.Values); err != nil {
				return nil, fmt.Errorf("%s.exit.values: %w", where, err)
			}
			if u.Stdout != stdoutNone {
				return nil, fmt.Errorf("%s: exit.values requires stdout \"none\" (one producer of the output value)", where)
			}
			okSet := make(map[int]bool, len(e.OK))
			for _, c := range e.OK {
				okSet[c] = true
			}
			for k := range e.Values {
				code, err := strconv.Atoi(k)
				if err != nil {
					return nil, fmt.Errorf("%s.exit.values: key %q is not a decimal exit code", where, k)
				}
				if !okSet[code] {
					return nil, fmt.Errorf("%s.exit.values: code %q is not in ok", where, k)
				}
			}
		}
		u.Exit = e
	}

	return u, nil
}

// wrapperVersionSupported enforces the per-unit version refusal rule (§2):
// pre-1.0, any higher minor than this implementation authors is unsupported.
func wrapperVersionSupported(v string) error {
	got, err := parseSemverLoose(v)
	if err != nil {
		return fmt.Errorf("invalid %s version %q: %w", unitVersionKey, v, err)
	}
	max, _ := parseSemverLoose(WrapperVersion)
	if got.major != max.major || compareSemver(got, max) > 0 {
		return fmt.Errorf("unit declares %s %q; this implementation supports %s (refused, not ignored)", unitVersionKey, v, WrapperVersion)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Artifact loading (§3) and unit validation (§10)
// ---------------------------------------------------------------------------

// loadArtifact resolves the wrapper's kdl per §3 and parses it. Called once
// per wrapper; the parsed Spec is cached on the Wrapper.
func (w *Wrapper) loadArtifact() (*Spec, error) {
	if w.kdl != nil {
		return w.kdl, nil
	}

	// spec.format: the declared artifact-version carrier, checked here.
	if w.Spec.Format == "" {
		return nil, fmt.Errorf("spec.format is required (the declared jdx artifact version)")
	}
	if err := artifactFormatAccepted(w.Spec.Format); err != nil {
		return nil, err
	}

	var text string
	switch {
	case w.Spec.Content != "":
		text = w.Spec.Content
		// content + hash: provenance only, never a refusal (§3).
	case w.Spec.Location != "":
		if w.Spec.Hash == "" {
			return nil, fmt.Errorf("spec.hash is required when spec.content is absent (location-only mode is fail-closed against artifact drift)")
		}
		fetched, err := fetchArtifactText(w.Spec.Location)
		if err != nil {
			return nil, err
		}
		if err := verifyArtifactHash(fetched, w.Spec.Hash); err != nil {
			return nil, err
		}
		text = fetched
	default:
		return nil, fmt.Errorf("spec must carry content or location")
	}

	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("parse wrapped usage artifact: %w", err)
	}

	// min_usage_version cross-check (§10).
	if min := spec.Meta().MinUsageVersion; min != "" {
		ok, err := IsSupportedVersion(min)
		if err != nil {
			return nil, fmt.Errorf("artifact min_usage_version %q: %w", min, err)
		}
		if !ok {
			return nil, fmt.Errorf("artifact declares min_usage_version %q, outside the accepted range %s-%s", min, MinSupportedVersion, MaxTestedVersion)
		}
	}

	w.kdl = spec
	return spec, nil
}

// artifactFormatAccepted checks spec.format against ArtifactRange (§12).
func artifactFormatAccepted(format string) error {
	if _, err := formattoken.Parse(format); err != nil {
		return fmt.Errorf("spec.format %q: %w", format, err)
	}
	rng, err := formattoken.ParseRange(ArtifactRange)
	if err != nil {
		return err
	}
	if !formattoken.Matches(rng, format) {
		return fmt.Errorf("spec.format %q is outside the accepted artifact range %q", format, ArtifactRange)
	}
	return nil
}

func fetchArtifactText(location string) (string, error) {
	if strings.HasPrefix(location, "exec:") {
		return resolveCommandArtifact(context.Background(), location)
	}
	if !filepath.IsAbs(location) && !strings.Contains(location, "://") {
		return "", fmt.Errorf("spec.location %q must be absolute (never resolved against a carriage base)", location)
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return "", fmt.Errorf("read spec.location: %w", err)
	}
	return string(data), nil
}

// verifyArtifactHash checks sha256 over the exact UTF-8 octets, no
// normalization (§3).
func verifyArtifactHash(text, declared string) error {
	hexWant, ok := strings.CutPrefix(declared, "sha256:")
	if !ok {
		return fmt.Errorf("spec.hash %q: want sha256:<hex>", declared)
	}
	sum := sha256.Sum256([]byte(text))
	if !strings.EqualFold(hex.EncodeToString(sum[:]), hexWant) {
		return fmt.Errorf("spec.hash mismatch: the artifact changed under the units (fetched sha256:%s)", hex.EncodeToString(sum[:]))
	}
	return nil
}

// ArtifactHash computes the spec.hash value for artifact text.
func ArtifactHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resolveUnitRef resolves a binding ref (§11): an RFC 3986 fragment carrying
// a JSON Pointer whose target is a unit. Bare names are refused.
func (w *Wrapper) resolveUnitRef(ref string) (*Unit, string, error) {
	name, ok := strings.CutPrefix(strings.TrimSpace(ref), "#/units/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return nil, "", fmt.Errorf("ref %q does not resolve to a unit (want #/units/<name>)", ref)
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
	u, exists := w.Units[name]
	if !exists {
		return nil, "", fmt.Errorf("ref %q: no unit %q in this document", ref, name)
	}
	return u, name, nil
}

// validateUnit runs the §10 static checks for one unit against the parsed
// artifact. Failure makes bindings referencing this unit unactionable; other
// units are unaffected (per-unit granularity).
func (w *Wrapper) validateUnit(u *Unit, name string) (*Command, []Flag, []string, error) {
	spec, err := w.loadArtifact()
	if err != nil {
		return nil, nil, nil, err
	}

	var cmd *Command
	var inherited []Flag
	var path []string
	if strings.TrimSpace(u.Command) == "" {
		rc := rootCommand(spec)
		if rc == nil {
			rc = &Command{Flags: spec.Flags(), Args: spec.Args()}
		}
		cmd = rc
	} else {
		found, err := findCommand(spec, u.Command)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unit %q: command %q does not resolve in the artifact", name, u.Command)
		}
		cmd = found.cmd
		inherited = found.inheritedFlags
		path = found.path
	}

	for field, mode := range u.Delivery {
		slot, kind := findSlot(cmd, inherited, field)
		switch mode {
		case deliveryStdinDash, deliveryFile:
			if kind == slotNone {
				return nil, nil, nil, fmt.Errorf("unit %q: delivery key %q (%s) names no flag or arg of command %q", name, field, mode, u.Command)
			}
			if kind == slotBoolFlag {
				return nil, nil, nil, fmt.Errorf("unit %q: delivery key %q (%s) names a boolean/count flag, which cannot receive a token", name, field, mode)
			}
			if mode == deliveryStdinDash && len(slotChoices(slot)) > 0 && !containsString(slotChoices(slot), "-") {
				return nil, nil, nil, fmt.Errorf("unit %q: delivery key %q (stdin-dash) names a slot whose choices do not include \"-\"", name, field)
			}
		case deliveryStdin:
			if kind != slotNone {
				return nil, nil, nil, fmt.Errorf("unit %q: delivery key %q (stdin) names a flag or arg; slotless stdin requires a field that maps to none (did you mean %q?)", name, field, deliveryStdinDash)
			}
		}
	}

	return cmd, inherited, path, nil
}

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

// GeneratedUnitName derives the deterministic unit name for a command path
// (§11): the path joined with ".".
func GeneratedUnitName(path []string) string {
	return strings.Join(path, ".")
}

// UnitRef returns the binding ref for a unit name (§11).
func UnitRef(name string) string {
	return "#/units/" + name
}

// TrivialWrapper builds a wrapper document (as a JSON-object map, the OBI
// source content form) around artifact text: one trivial unit per bindable
// command, named per §11. Used by synthesis from a bare kdl and by sync
// backfill.
func TrivialWrapper(artifactText string, spec *Spec) map[string]any {
	units := map[string]any{}

	unitFor := func(command string) map[string]any {
		return map[string]any{
			unitVersionKey: WrapperVersion,
			"command":      command,
		}
	}

	meta := spec.Meta()
	if rc := rootCommand(spec); rc != nil {
		rootName := meta.Bin
		if rootName == "" {
			rootName = meta.Name
		}
		if rootName != "" {
			units[rootName] = unitFor("")
		}
	}
	walkWithGlobals(spec, func(path []string, cmd Command, _ []Flag) {
		if len(path) == 0 || cmd.SubcommandRequired {
			return
		}
		units[GeneratedUnitName(path)] = unitFor(strings.Join(path, " "))
	})

	artifactFormat := formattoken.FormatToken{Name: "usage", Version: MaxTestedVersion}
	return map[string]any{
		"spec": map[string]any{
			"format":  artifactFormat.String(),
			"content": artifactText,
		},
		"units": units,
	}
}

// ---------------------------------------------------------------------------
// Delivery application (§5)
// ---------------------------------------------------------------------------

// applyDelivery reroutes delivery-listed input fields off argv per the
// unit's map. stdin-dash substitutes the literal "-" in the field's slot;
// slotless stdin removes the field entirely (load validation guarantees it
// maps to nothing); file substitutes an absolute temp path. The real bytes
// travel out of band. A listed field absent from the input, or present with
// JSON null, is a no-op.
func applyDelivery(input any, u *Unit) (any, []byte, func(), error) {
	noop := func() {}
	if u == nil || len(u.Delivery) == 0 || input == nil {
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
	for field, mode := range u.Delivery {
		value, present := substituted[field]
		if !present || value == nil {
			delete(substituted, field)
			continue
		}
		data, isString := deliveryBytes(value)
		if len(data) > maxDeliveryBytes {
			return fail(fmt.Errorf("field %q: %d bytes exceeds the %d-byte delivery cap", field, len(data), maxDeliveryBytes))
		}
		switch mode {
		case deliveryStdinDash:
			stdin = data
			substituted[field] = "-"
		case deliveryStdin:
			stdin = data
			delete(substituted, field)
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

// deliveryBytes renders a routed value as channel bytes: strings raw, any
// other JSON value as compact JSON (§5).
func deliveryBytes(value any) (data []byte, isString bool) {
	if s, ok := value.(string); ok {
		return []byte(s), true
	}
	data, _ = json.Marshal(value)
	return data, false
}

// deliveryFileName names a materialized temp file: "<field>.json" for
// JSON-encoded values, bare "<field>" for raw strings, path-hostile runes
// replaced; a raw string's name must not end in .json (§5).
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
