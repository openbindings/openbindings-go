package openbindings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type validateOptions struct {
	rejectUnknownTypedFields bool
	requireEventPayload      bool
	requireSupportedVersion  bool
}

// ValidateOption configures Interface.Validate.
type ValidateOption func(*validateOptions)

// WithRejectUnknownTypedFields treats unknown (non-`x-`) fields in typed OpenBindings objects as errors.
// Default behavior is forward-compatible (unknowns allowed/ignored), so this is an opt-in "strict" mode.
func WithRejectUnknownTypedFields() ValidateOption {
	return func(o *validateOptions) { o.rejectUnknownTypedFields = true }
}

// WithRequireEventPayload requires kind="event" operations to have a payload schema.
// By default, event payload is optional. An empty object is still considered present.
func WithRequireEventPayload() ValidateOption {
	return func(o *validateOptions) { o.requireEventPayload = true }
}

// WithRequireSupportedVersion requires the openbindings version to be within the SDK's supported range.
// By default, versions outside the supported range are allowed for forward compatibility.
func WithRequireSupportedVersion() ValidateOption {
	return func(o *validateOptions) { o.requireSupportedVersion = true }
}

var semverish = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Validate performs shape-level checks useful for tooling correctness.
// It is intentionally not full JSON Schema validation.
func (i Interface) Validate(opts ...ValidateOption) error {
	o := validateOptions{
		rejectUnknownTypedFields: false,
		requireEventPayload:      false,
		requireSupportedVersion:  false,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	var errs []string

	if strings.TrimSpace(i.OpenBindings) == "" {
		errs = append(errs, "openbindings: required")
	} else if !semverish.MatchString(i.OpenBindings) {
		errs = append(errs, "openbindings: must be MAJOR.MINOR.PATCH (e.g. 0.1.0)")
	} else if o.requireSupportedVersion {
		ok, err := IsSupportedVersion(i.OpenBindings)
		if err != nil {
			errs = append(errs, fmt.Sprintf("openbindings: invalid version: %v", err))
		} else if !ok {
			errs = append(errs, fmt.Sprintf("openbindings: unsupported version %q (supported %s-%s)", i.OpenBindings, MinSupportedVersion, MaxTestedVersion))
		}
	}

	// Validate imports: values must be non-empty.
	for k, v := range i.Imports {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Sprintf("imports[%q]: value must be non-empty", k))
		}
	}

	if i.Operations == nil {
		errs = append(errs, "operations: required")
	}

	opKeys := make([]string, 0, len(i.Operations))
	for k := range i.Operations {
		opKeys = append(opKeys, k)
	}
	sort.Strings(opKeys)

	// aliases MUST NOT be shared across different operations (avoids ambiguous matching).
	aliasOwner := map[string]string{}
	opKeySet := map[string]struct{}{}
	for _, k := range opKeys {
		opKeySet[k] = struct{}{}
	}

	for _, k := range opKeys {
		op := i.Operations[k]
		switch op.Kind {
		case OperationKindMethod, OperationKindEvent:
			// ok
		default:
			errs = append(errs, fmt.Sprintf("operations[%q].kind: must be %q or %q", k, OperationKindMethod, OperationKindEvent))
			continue
		}

		if o.requireEventPayload && op.Kind == OperationKindEvent {
			if op.Payload == nil {
				errs = append(errs, fmt.Sprintf("operations[%q].payload: required for kind=%q", k, OperationKindEvent))
			}
		}

		// Alias checks.
		for _, a := range op.Aliases {
			if strings.TrimSpace(a) == "" {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: must not contain empty strings", k))
				continue
			}
			if _, isOpKey := opKeySet[a]; isOpKey && a != k {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q conflicts with operation key %q", k, a, a))
				continue
			}
			if owner, ok := aliasOwner[a]; ok && owner != k {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q is also an alias of %q", k, a, owner))
				continue
			}
			aliasOwner[a] = k
		}

		// Satisfies sanity.
		for idx, s := range op.Satisfies {
			if strings.TrimSpace(s.Interface) == "" {
				errs = append(errs, fmt.Sprintf("operations[%q].satisfies[%d].interface: required", k, idx))
			} else if _, ok := i.Imports[s.Interface]; !ok {
				errs = append(errs, fmt.Sprintf("operations[%q].satisfies[%d].interface: references unknown import %q", k, idx, s.Interface))
			}
			if strings.TrimSpace(s.Operation) == "" {
				errs = append(errs, fmt.Sprintf("operations[%q].satisfies[%d].operation: required", k, idx))
			}
		}

		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("operations[%q]", k), op.Unknown)
			for idx, s := range op.Satisfies {
				appendUnknownFieldProblems(&errs, fmt.Sprintf("operations[%q].satisfies[%d]", k, idx), s.Unknown)
			}
			for ek, ex := range op.Examples {
				appendUnknownFieldProblems(&errs, fmt.Sprintf("operations[%q].examples[%q]", k, ek), ex.Unknown)
			}
		}
	}

	// Validate sources.
	srcKeys := make([]string, 0, len(i.Sources))
	for k := range i.Sources {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)
	for _, k := range srcKeys {
		src := i.Sources[k]
		if strings.TrimSpace(src.Format) == "" {
			errs = append(errs, fmt.Sprintf("sources[%q].format: required", k))
		}
		hasLocation := strings.TrimSpace(src.Location) != ""
		hasContent := src.Content != nil
		if hasLocation && hasContent {
			errs = append(errs, fmt.Sprintf("sources[%q]: cannot have both location and content", k))
		}
		if !hasLocation && !hasContent {
			errs = append(errs, fmt.Sprintf("sources[%q]: must have location or content", k))
		}
		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("sources[%q]", k), src.Unknown)
		}
	}

	// Validate transforms.
	trKeys := make([]string, 0, len(i.Transforms))
	for k := range i.Transforms {
		trKeys = append(trKeys, k)
	}
	sort.Strings(trKeys)
	for _, k := range trKeys {
		tr := i.Transforms[k]
		if strings.TrimSpace(tr.Type) == "" {
			errs = append(errs, fmt.Sprintf("transforms[%q].type: required", k))
		} else if tr.Type != "jsonata" {
			errs = append(errs, fmt.Sprintf("transforms[%q].type: must be \"jsonata\" (got %q)", k, tr.Type))
		}
		if strings.TrimSpace(tr.Expression) == "" {
			errs = append(errs, fmt.Sprintf("transforms[%q].expression: required", k))
		}
		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("transforms[%q]", k), tr.Unknown)
		}
	}

	// Validate bindings.
	bndKeys := make([]string, 0, len(i.Bindings))
	for k := range i.Bindings {
		bndKeys = append(bndKeys, k)
	}
	sort.Strings(bndKeys)
	for _, k := range bndKeys {
		b := i.Bindings[k]
		if strings.TrimSpace(b.Operation) == "" {
			errs = append(errs, fmt.Sprintf("bindings[%q].operation: required", k))
		} else if _, ok := i.Operations[b.Operation]; !ok {
			errs = append(errs, fmt.Sprintf("bindings[%q].operation: references unknown operation %q", k, b.Operation))
		}
		if strings.TrimSpace(b.Source) == "" {
			errs = append(errs, fmt.Sprintf("bindings[%q].source: required", k))
		} else if _, ok := i.Sources[b.Source]; !ok {
			errs = append(errs, fmt.Sprintf("bindings[%q].source: references unknown source %q", k, b.Source))
		}

		// Validate transform references.
		if b.InputTransform != nil && b.InputTransform.IsRef() {
			if err := validateTransformRef(b.InputTransform.Ref, i.Transforms); err != nil {
				errs = append(errs, fmt.Sprintf("bindings[%q].inputTransform.$ref: %v", k, err))
			}
		}
		if b.OutputTransform != nil && b.OutputTransform.IsRef() {
			if err := validateTransformRef(b.OutputTransform.Ref, i.Transforms); err != nil {
				errs = append(errs, fmt.Sprintf("bindings[%q].outputTransform.$ref: %v", k, err))
			}
		}

		// Validate inline transforms.
		if b.InputTransform != nil && !b.InputTransform.IsRef() && b.InputTransform.Transform != nil {
			validateInlineTransform(&errs, fmt.Sprintf("bindings[%q].inputTransform", k), b.InputTransform.Transform)
		}
		if b.OutputTransform != nil && !b.OutputTransform.IsRef() && b.OutputTransform.Transform != nil {
			validateInlineTransform(&errs, fmt.Sprintf("bindings[%q].outputTransform", k), b.OutputTransform.Transform)
		}

		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("bindings[%q]", k), b.Unknown)
			if b.InputTransform != nil && !b.InputTransform.IsRef() && b.InputTransform.Transform != nil {
				appendUnknownFieldProblems(&errs, fmt.Sprintf("bindings[%q].inputTransform", k), b.InputTransform.Transform.Unknown)
			}
			if b.OutputTransform != nil && !b.OutputTransform.IsRef() && b.OutputTransform.Transform != nil {
				appendUnknownFieldProblems(&errs, fmt.Sprintf("bindings[%q].outputTransform", k), b.OutputTransform.Transform.Unknown)
			}
		}
	}

	if o.rejectUnknownTypedFields {
		appendUnknownFieldProblems(&errs, "", i.Unknown)
	}

	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Problems: errs}
}

func appendUnknownFieldProblems(errs *[]string, prefix string, unknown map[string]json.RawMessage) {
	if len(unknown) == 0 {
		return
	}
	keys := make([]string, 0, len(unknown))
	for k := range unknown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if prefix == "" {
		*errs = append(*errs, fmt.Sprintf("unknown fields: %s", strings.Join(keys, ", ")))
		return
	}
	*errs = append(*errs, fmt.Sprintf("%s: unknown fields: %s", prefix, strings.Join(keys, ", ")))
}

// ValidationError is a deterministic, multi-problem validation error.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "invalid interface"
	}
	return "invalid interface: " + strings.Join(e.Problems, "; ")
}

// validateTransformRef validates that a $ref points to a valid transform.
func validateTransformRef(ref string, transforms map[string]Transform) error {
	const prefix = "#/transforms/"
	if !strings.HasPrefix(ref, prefix) {
		return fmt.Errorf("must start with %q", prefix)
	}
	name := strings.TrimPrefix(ref, prefix)
	if name == "" {
		return fmt.Errorf("transform name is empty")
	}
	if _, ok := transforms[name]; !ok {
		return fmt.Errorf("references unknown transform %q", name)
	}
	return nil
}

// validateInlineTransform validates an inline transform definition.
func validateInlineTransform(errs *[]string, prefix string, tr *Transform) {
	if strings.TrimSpace(tr.Type) == "" {
		*errs = append(*errs, fmt.Sprintf("%s.type: required", prefix))
	} else if tr.Type != "jsonata" {
		*errs = append(*errs, fmt.Sprintf("%s.type: must be \"jsonata\" (got %q)", prefix, tr.Type))
	}
	if strings.TrimSpace(tr.Expression) == "" {
		*errs = append(*errs, fmt.Sprintf("%s.expression: required", prefix))
	}
}
