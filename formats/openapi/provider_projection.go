package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	openapiprovider "github.com/openbindings/openapi-client/go/provider"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

func unrealizableOperation(operationKey, reason string) error {
	return fmt.Errorf("cannot synthesize OpenAPI operation %q: %s; synthesis would return a statically unbindable partial interface", operationKey, reason)
}

func absolutizeArtifactLocation(location string) (string, error) {
	if location == "" || strings.Contains(location, "://") {
		return location, nil
	}
	abs := location
	if !filepath.IsAbs(location) {
		var err error
		abs, err = filepath.Abs(location)
		if err != nil {
			return "", fmt.Errorf("resolve OpenAPI artifact path: %w", err)
		}
	}
	return "file://" + abs, nil
}

func validateDocumentAddress(location string) error {
	u, err := url.Parse(location)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return fmt.Errorf("openapi location %q is not an absolute URI addressing the document (OAPI-D-02): a local artifact is addressed as file:// or embedded as the source's content", location)
	}
	return nil
}

func escapeJSONPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

type providerProjectionObservation struct {
	iface    *openbindings.Interface
	coverage []synthesize.SynthesisCoverageEntry
	failures []openapiprovider.ProjectionFailure
}

// synthesizeProviderProjection is the complete OpenBindings-owned projection
// over the standalone client's detached analysis. All OpenAPI loading,
// reference resolution, declaration planning, schema projection, and
// smallest-owner classification have already happened in the provider.
func (c *Synthesizer) synthesizeProviderProjection(
	ctx context.Context,
	in *synthesize.SynthesizeInput,
	tolerant bool,
) (*providerProjectionObservation, error) {
	if in == nil || len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &providerProjectionObservation{iface: &skeleton}, err
	}
	if len(in.Sources) > 1 {
		return nil, synthesize.ErrMultipleSources
	}
	source := in.Sources[0]
	if source.BindingSpec == BindingSpecOpenAPI20 || !isRequestImplementedOpenAPIBindingSpec(source.BindingSpec) {
		return nil, fmt.Errorf("%s: binding specification %q is not implemented by the OpenAPI 3.x provider projection", ErrCodeUnsupportedBindingSpec, source.BindingSpec)
	}
	if source.OutputLocation != "" {
		if err := validateDocumentAddress(source.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	loadLocation, err := absolutizeArtifactLocation(source.Location)
	if err != nil {
		return nil, err
	}
	artifactContent := source.Content
	if source.Embed && artifactContent == nil {
		data, readErr := readAuthoringArtifact(ctx, c.resolverClient(), loadLocation)
		if readErr != nil {
			return nil, fmt.Errorf("embed OpenAPI source: %w", readErr)
		}
		artifactContent = openbindings.TextContent(string(data))
	}
	observed, err := analyzeProviderProjection(
		ctx,
		c.resolverClient(),
		source,
		artifactContent,
		loadLocation,
		in.OnWarning,
	)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	if failure := firstProviderFailure(observed.failures); failure != nil && !tolerant {
		if failure.SourceRef == "#" && failure.Status == "excluded" {
			return nil, &openAPISourceExcludedError{message: failure.Message, rule: failure.Rule}
		}
		return nil, unrealizableOperation(failure.OperationKey, failure.Message)
	}
	if observed.iface == nil {
		return nil, fmt.Errorf("native provider returned no projection")
	}
	if len(observed.coverage) == 0 && len(observed.failures) == 1 && observed.failures[0].SourceRef == "#" {
		failure := observed.failures[0]
		observed.coverage = []synthesize.SynthesisCoverageEntry{{
			SourceIndex: 0,
			SourceRef:   "#",
			Scope:       synthesize.SynthesisCoverageSource,
			Status:      synthesize.SynthesisCoverageStatus(failure.Status),
			ReasonCode:  failure.ReasonCode,
			Rule:        failure.Rule,
			Message:     failure.Message,
		}}
	}
	if err := synthesize.FinalizeSynthesis(observed.iface, in, DefaultSourceName, source.BindingSpec); err != nil {
		return nil, err
	}
	return observed, nil
}

func analyzeProviderProjection(
	ctx context.Context,
	client *http.Client,
	source synthesize.SynthesizeSource,
	content json.RawMessage,
	location string,
	onWarning func(synthesize.SynthesizerWarning),
) (*providerProjectionObservation, error) {
	var bytes []byte
	var err error
	if content != nil {
		bytes, err = openbindings.ContentToBytes(content)
		if err != nil {
			return nil, err
		}
	}
	analysis, err := openapiprovider.AnalyzeProjection(ctx, openapiprovider.Source{
		Location: location,
		Content:  bytes,
	}, openapiprovider.ClientOptions{LoadHTTPClient: client})
	if err != nil {
		return nil, err
	}
	if analysis.OpenAPI3 == nil {
		if len(analysis.Failures) > 0 {
			return &providerProjectionObservation{
				iface:    providerProjectionSkeleton(source, content, location),
				coverage: providerCoverage(analysis.Coverage, source),
				failures: analysis.Failures,
			}, nil
		}
		return nil, fmt.Errorf("native provider returned no OpenAPI 3.x projection")
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpecValue(string(analysis.Edition), source.BindingSpec); err != nil {
		return nil, err
	}
	for _, warning := range analysis.Warnings {
		if onWarning != nil {
			onWarning(synthesize.SynthesizerWarning{Code: warning.Code, Message: warning.Message, Path: warning.Path})
		}
	}
	iface := providerProjectionSkeleton(source, content, location)
	iface.Name = analysis.OpenAPI3.Name
	iface.Version = analysis.OpenAPI3.Version
	iface.Description = analysis.OpenAPI3.Description
	for key, operation := range analysis.OpenAPI3.Operations {
		projected := openbindings.Operation{
			Description: operation.Description,
			Deprecated:  operation.Deprecated,
			Tags:        append([]string(nil), operation.Tags...),
			Input:       operation.Input,
			Output:      operation.Output,
		}
		iface.Operations[key] = projected
	}
	for _, binding := range analysis.OpenAPI3.Bindings {
		key := binding.Operation + "." + DefaultSourceName
		projected := openbindings.BindingEntry{
			Operation: binding.Operation,
			Source:    DefaultSourceName,
			Selector:  binding.Selector,
		}
		if binding.Input != nil {
			projected.InputTransform = &openbindings.TransformOrRef{Inline: providerInputTransform(binding.Input)}
		}
		iface.Bindings[key] = projected
	}
	for key, dependency := range analysis.OpenAPI3.Dependencies {
		iface.Dependencies[key] = openbindings.DependencyEntry{Operation: dependency.Operation}
	}
	return &providerProjectionObservation{
		iface:    iface,
		coverage: providerCoverage(analysis.Coverage, source),
		failures: analysis.Failures,
	}, nil
}

// providerInputTransform is intentionally OpenBindings-owned: the provider
// reports native correspondence facts, while this adapter renders ordinary
// Core JSONata into the published binding entry.
func providerInputTransform(input *openapiprovider.ProjectionInputCorrespondence) string {
	parameterFields := map[string]string{}
	excluded := map[string]bool{}
	for _, route := range input.Parameters {
		parameterFields[route.CallerKey] = route.Field
		excluded[route.Field] = true
	}
	parametersExpression := jsonataObject(parameterFields)

	bodyFields := cloneStringMap(input.BodyProperties)
	bodyExpression := jsonataObject(bodyFields)
	bodyPresence := make([]string, 0, len(bodyFields)+1)
	bodyNames := make([]string, 0, len(bodyFields))
	for name := range bodyFields {
		bodyNames = append(bodyNames, name)
	}
	sort.Strings(bodyNames)
	for _, name := range bodyNames {
		field := bodyFields[name]
		excluded[field] = true
		bodyPresence = append(bodyPresence, "$exists("+jsonataLookup(field)+")")
	}
	if input.WholeBodyField != "" {
		excluded[input.WholeBodyField] = true
	}
	if input.OpenBody {
		keys := make([]string, 0, len(excluded))
		for key := range excluded {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		condition := "true"
		if len(keys) > 0 {
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, "$key != "+quotedJSONata(key))
			}
			condition = strings.Join(parts, " and ")
		}
		passthrough := "$sift($,function($value,$key){" + condition + "})"
		if len(bodyFields) > 0 {
			bodyExpression = "$merge([" + passthrough + "," + bodyExpression + "])"
		} else {
			bodyExpression = passthrough
		}
		bodyPresence = append(bodyPresence, "$count($keys("+passthrough+")) > 0")
	}
	if input.BodyRequired && input.WholeBodyField == "" {
		bodyPresence = append(bodyPresence, "true")
	}
	parameterValue := "$count($keys($parameters)) > 0 ? $parameters : " + jsonataUndefined
	bodyValue := jsonataUndefined
	if len(bodyFields) > 0 || input.OpenBody || input.BodyRequired {
		condition := "false"
		if len(bodyPresence) > 0 {
			condition = strings.Join(bodyPresence, " or ")
		}
		bodyValue = "(" + condition + ") ? $bodyObject : " + jsonataUndefined
	}
	if input.WholeBodyField != "" {
		whole := jsonataLookup(input.WholeBodyField)
		bodyValue = "$exists(" + whole + ") ? " + whole + " : (" + bodyValue + ")"
	}
	return "($parameters := " + parametersExpression + "; $bodyObject := " + bodyExpression +
		`; {"parameters":` + parameterValue + `,"body":` + bodyValue + "})"
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for name, member := range value {
		result[name] = member
	}
	return result
}

func providerProjectionSkeleton(source synthesize.SynthesizeSource, content json.RawMessage, location string) *openbindings.Interface {
	entry := openbindings.Source{BindingSpec: source.BindingSpec, Location: location, Description: source.Description}
	if content != nil {
		entry.Content = append(json.RawMessage(nil), content...)
	}
	return &openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Dependencies: map[string]openbindings.DependencyEntry{},
		Sources:      map[string]openbindings.Source{DefaultSourceName: entry},
	}
}

func providerCoverage(entries []openapiprovider.ProjectionCoverageEntry, source synthesize.SynthesizeSource) []synthesize.SynthesisCoverageEntry {
	result := make([]synthesize.SynthesisCoverageEntry, 0, len(entries))
	for _, entry := range entries {
		converted := synthesize.SynthesisCoverageEntry{
			SourceIndex: entry.SourceIndex, SourceRef: entry.SourceRef,
			Scope: synthesize.SynthesisCoverageScope(entry.Scope), Status: synthesize.SynthesisCoverageStatus(entry.Status),
			OperationKey: entry.OperationKey, BindingSelector: entry.BindingSelector,
			ReasonCode: entry.ReasonCode, Rule: entry.Rule, Message: entry.Message,
			Requirements: append([]string(nil), entry.Requirements...), Details: cloneAnyMap(entry.Details),
		}
		if entry.Scope != "source" && entry.Scope != "dependency" &&
			(converted.Status == synthesize.SynthesisRepresented || converted.Status == synthesize.SynthesisLossy) {
			converted.SourceKey = DefaultSourceName
			converted.BindingKey = converted.OperationKey + "." + DefaultSourceName
		}
		result = append(result, converted)
	}
	return result
}

func firstProviderFailure(failures []openapiprovider.ProjectionFailure) *openapiprovider.ProjectionFailure {
	if len(failures) == 0 {
		return nil
	}
	ordered := append([]openapiprovider.ProjectionFailure(nil), failures...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SourceRef < ordered[j].SourceRef })
	return &ordered[0]
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}
