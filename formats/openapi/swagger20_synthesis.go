package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	openapiprovider "github.com/openbindings/openapi-client/go/provider"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// synthesizeSwagger20 is the thin OpenBindings projection over the client's
// native, raw-preserving Swagger 2.0 analysis. Artifact structure, reference
// closure, effective declarations, and lane usability remain client work;
// this file owns only flat Core contracts, the public envelope transform, and
// durable OpenBindings coverage vocabulary.
func (c *Synthesizer) synthesizeSwagger20(ctx context.Context, in *synthesize.SynthesizeInput, tolerant bool) (*openbindings.Interface, *openapiprovider.Swagger20SynthesisDocument, []synthesize.SynthesisCoverageEntry, error) {
	if in == nil || len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &skeleton, nil, nil, err
	}
	if len(in.Sources) > 1 {
		return nil, nil, nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpecOpenAPI20 || !isImplementedOpenAPIBindingSpec(src.BindingSpec) {
		return nil, nil, nil, fmt.Errorf("%s: binding specification %q is not implemented", ErrCodeUnsupportedBindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateDocumentAddress(src.OutputLocation); err != nil {
			return nil, nil, nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	loadLocation, err := absolutizeArtifactLocation(src.Location)
	if err != nil {
		return nil, nil, nil, err
	}
	artifactContent := src.Content
	if src.Embed && artifactContent == nil {
		data, readErr := readAuthoringArtifact(ctx, c.resolverClient(), loadLocation)
		if readErr != nil {
			return nil, nil, nil, fmt.Errorf("embed Swagger 2.0 source: %w", readErr)
		}
		artifactContent = openbindings.TextContent(string(data))
	}
	var content []byte
	if artifactContent != nil {
		content, err = openbindings.ContentToBytes(artifactContent)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load Swagger 2.0 document: %w", err)
		}
	}
	client, err := openapiprovider.LoadSwagger20(ctx, openapiprovider.Swagger20Source{
		Location: loadLocation,
		Content:  content,
	}, openapiprovider.ClientOptions{HTTPClient: c.resolverClient()})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load Swagger 2.0 document: %w", err)
	}
	model, err := client.SynthesisModel()
	if err != nil {
		return nil, nil, nil, err
	}
	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         model.Name,
		Version:      model.Version,
		Description:  model.Description,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: {BindingSpec: BindingSpecOpenAPI20, Location: loadLocation},
		},
	}
	if artifactContent != nil {
		source := iface.Sources[DefaultSourceName]
		source.Content = append(json.RawMessage(nil), artifactContent...)
		iface.Sources[DefaultSourceName] = source
	}
	used := map[string]bool{}
	var coverage []synthesize.SynthesisCoverageEntry
	for _, operation := range model.Operations {
		opKey := swagger20OperationKey(operation, used)
		if operation.Excluded {
			if !tolerant {
				return nil, nil, nil, fmt.Errorf("cannot synthesize Swagger 2.0 operation at %q: %s", operation.Ref, operation.Reason)
			}
			status := synthesize.SynthesisExcluded
			if operation.Disposition == "invalid" {
				status = synthesize.SynthesisInvalid
			}
			rule := operation.Rule
			if rule == "" && status == synthesize.SynthesisExcluded {
				rule = swagger20TargetRule(operation.Reason)
			}
			coverage = append(coverage, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: operation.Ref, Scope: synthesize.SynthesisCoverageTarget,
				Status: status, ReasonCode: swagger20TargetReasonCode(operation.Reason),
				Rule: rule, Message: operation.Reason,
			})
			continue
		}
		used[opKey] = true
		obiOperation, inputTransform, losses, projectionErr := projectSwagger20Operation(operation)
		if projectionErr != nil {
			if !tolerant {
				return nil, nil, nil, fmt.Errorf("cannot synthesize Swagger 2.0 operation at %q: %w", operation.Ref, projectionErr)
			}
			coverage = append(coverage, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: operation.Ref, Scope: synthesize.SynthesisCoverageTarget,
				Status: synthesize.SynthesisExcluded, ReasonCode: "openapi20.schema_projection_excluded",
				Rule: "OAPI20-P-01", Message: projectionErr.Error(),
			})
			continue
		}
		iface.Operations[opKey] = obiOperation
		bindingKey := opKey + "." + DefaultSourceName
		binding := openbindings.BindingEntry{
			Operation: opKey, Source: DefaultSourceName, Selector: operation.Ref,
			Deprecated: operation.Deprecated,
		}
		if inputTransform != "" {
			binding.InputTransform = &openbindings.TransformOrRef{Inline: inputTransform}
		}
		iface.Bindings[bindingKey] = binding
		coverage = append(coverage, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: operation.Ref, Scope: synthesize.SynthesisCoverageTarget,
			Status: synthesize.SynthesisRepresented, OperationKey: opKey, BindingKey: bindingKey,
			BindingSelector: operation.Ref, Requirements: sortedUniqueStrings(operation.Requirements),
		})
		coverage = append(coverage, swagger20AlternativeCoverage(operation, opKey, bindingKey)...)
		for _, loss := range losses {
			coverage = append(coverage, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: loss.sourceRef, Scope: synthesize.SynthesisCoverageProjection,
				Status: synthesize.SynthesisLossy, OperationKey: opKey, BindingKey: bindingKey,
				BindingSelector: operation.Ref, ReasonCode: loss.reasonCode, Rule: "OAPI20-P-01", Message: loss.message,
			})
		}
	}
	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, BindingSpecOpenAPI20); err != nil {
		return nil, nil, nil, err
	}
	return &iface, model, coverage, nil
}

func swagger20OperationKey(operation openapiprovider.Swagger20SynthesisOperation, used map[string]bool) string {
	if operation.OperationID != "" {
		key := synthesize.SanitizeKey(operation.OperationID)
		if !used[key] {
			return key
		}
	}
	segments := strings.Split(strings.Trim(operation.Path, "/"), "/")
	parts := make([]string, 0, len(segments)+1)
	for _, segment := range segments {
		if segment != "" && !(strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")) {
			parts = append(parts, segment)
		}
	}
	parts = append(parts, strings.ToLower(operation.Method))
	return synthesize.UniqueKey(synthesize.SanitizeKey(strings.Join(parts, ".")), used)
}

type swagger20ProjectionLoss struct {
	sourceRef, reasonCode, message string
}

func projectSwagger20Operation(operation openapiprovider.Swagger20SynthesisOperation) (openbindings.Operation, string, []swagger20ProjectionLoss, error) {
	result := openbindings.Operation{
		Description: operation.Description,
		Deprecated:  operation.Deprecated,
		Tags:        append([]string(nil), operation.Tags...),
	}
	properties := map[string]any{}
	required := []string{}
	parameterFields := map[string]string{}
	locations := map[string]openapiprovider.Swagger20ParameterLocation{}
	qualified := false
	for _, parameter := range operation.Parameters {
		if previous, present := locations[parameter.Name]; present && previous != parameter.In {
			qualified = true
		}
		locations[parameter.Name] = parameter.In
	}
	usedFields := map[string]bool{}
	for _, parameter := range operation.Parameters {
		callerKey := parameter.Name
		if qualified {
			callerKey = string(parameter.In) + "/" + escapeJSONPointerSegment(parameter.Name)
		}
		field := uniqueSwagger20InputField(callerKey, usedFields)
		schema, losses, err := projectSwagger20Schema(parameter.Schema, true, operation.Ref+"/parameters/"+string(parameter.In)+"/"+escapeJSONPointerSegment(parameter.Name))
		if err != nil {
			return result, "", nil, err
		}
		properties[field] = schema
		parameterFields[callerKey] = field
		if parameter.Required {
			required = append(required, field)
		}
		_ = losses // Parameter schemas have no OAS 2.0-only assertion loss after projection.
	}
	bodyField := ""
	var projectionLosses []swagger20ProjectionLoss
	if operation.Body != nil {
		bodyField = uniqueSwagger20InputField("body", usedFields)
		schema, losses, err := projectSwagger20Schema(operation.Body.Schema, true, operation.Ref+"/body/schema")
		if err != nil {
			return result, "", nil, err
		}
		properties[bodyField] = schema
		projectionLosses = append(projectionLosses, losses...)
		if operation.Body.Required {
			required = append(required, bodyField)
		}
	}
	if len(properties) > 0 {
		input := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			sort.Strings(required)
			input["required"] = stringSliceAsAny(required)
		}
		result.Input = input
	}
	for _, response := range operation.Responses {
		if !response.CanSucceed || !response.Usable || !response.SchemaPresent {
			continue
		}
		schema, losses, err := projectSwagger20Schema(response.Schema, false, response.SourceRef+"/schema")
		if err != nil {
			return result, "", nil, err
		}
		projectionLosses = append(projectionLosses, losses...)
		if result.Output == nil {
			result.Output = schema
			continue
		}
		if union, ok := result.Output.(map[string]any); ok {
			if branches, present := union["anyOf"].([]any); present {
				union["anyOf"] = append(branches, schema)
				continue
			}
		}
		result.Output = map[string]any{"anyOf": []any{result.Output, schema}}
	}
	return result, swagger20EnvelopeTransform(parameterFields, bodyField), projectionLosses, nil
}

func uniqueSwagger20InputField(base string, used map[string]bool) string {
	if !used[base] {
		used[base] = true
		return base
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s_%d", base, index)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func swagger20EnvelopeTransform(parameters map[string]string, bodyField string) string {
	if len(parameters) == 0 && bodyField == "" {
		return ""
	}
	parameterObject := jsonataObject(parameters)
	parameterValue := "$count($keys($parameters)) > 0 ? $parameters : " + jsonataUndefined
	bodyValue := jsonataUndefined
	if bodyField != "" {
		lookup := jsonataLookup(bodyField)
		bodyValue = "$exists(" + lookup + ") ? " + lookup + " : " + jsonataUndefined
	}
	return "($parameters := " + parameterObject + `; {"parameters":` + parameterValue + `,"body":` + bodyValue + "})"
}

func projectSwagger20Schema(raw json.RawMessage, request bool, sourceRef string) (any, []swagger20ProjectionLoss, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, fmt.Errorf("Swagger 2.0 schema at %s has no JSON image: %w", sourceRef, err)
	}
	projected, losses, err := projectSwagger20SchemaValue(value, request, sourceRef)
	return projected, losses, err
}

func projectSwagger20SchemaValue(value any, request bool, sourceRef string) (any, []swagger20ProjectionLoss, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("Swagger 2.0 schema at %s is not an object", sourceRef)
	}
	result := map[string]any{}
	var losses []swagger20ProjectionLoss
	allowed := map[string]bool{
		"$ref": true, "$defs": true, "format": true, "title": true, "description": true, "default": true,
		"multipleOf": true, "maximum": true, "exclusiveMaximum": true, "minimum": true, "exclusiveMinimum": true,
		"maxLength": true, "minLength": true, "pattern": true, "maxItems": true, "minItems": true,
		"uniqueItems": true, "maxProperties": true, "minProperties": true, "required": true, "enum": true,
		"type": true, "items": true, "allOf": true, "properties": true, "additionalProperties": true,
	}
	for key, member := range object {
		if allowed[key] {
			result[key] = member
		}
	}
	if result["type"] == "file" || result["format"] == "binary" || result["format"] == "byte" {
		result["type"] = "string"
		result["contentEncoding"] = "base64"
		delete(result, "format")
	}
	projectExclusive := func(limit, exclusive string) {
		flag, present := result[exclusive].(bool)
		if !present {
			return
		}
		delete(result, exclusive)
		if flag {
			if bound, exists := result[limit]; exists {
				result[exclusive] = bound
				delete(result, limit)
			}
		}
	}
	projectExclusive("maximum", "exclusiveMaximum")
	projectExclusive("minimum", "exclusiveMinimum")
	if items, present := object["items"].(map[string]any); present {
		projected, childLosses, err := projectSwagger20SchemaValue(items, request, sourceRef+"/items")
		if err != nil {
			return nil, nil, err
		}
		result["items"] = projected
		losses = append(losses, childLosses...)
	}
	if additional, present := object["additionalProperties"].(map[string]any); present {
		projected, childLosses, err := projectSwagger20SchemaValue(additional, request, sourceRef+"/additionalProperties")
		if err != nil {
			return nil, nil, err
		}
		result["additionalProperties"] = projected
		losses = append(losses, childLosses...)
	}
	if branches, present := object["allOf"].([]any); present {
		projected := make([]any, len(branches))
		for index, branch := range branches {
			child, childLosses, err := projectSwagger20SchemaValue(branch, request, fmt.Sprintf("%s/allOf/%d", sourceRef, index))
			if err != nil {
				return nil, nil, err
			}
			projected[index] = child
			losses = append(losses, childLosses...)
		}
		result["allOf"] = projected
	}
	if defs, present := object["$defs"].(map[string]any); present {
		projected := map[string]any{}
		for name, definition := range defs {
			child, childLosses, err := projectSwagger20SchemaValue(definition, request, sourceRef+"/$defs/"+escapeJSONPointerSegment(name))
			if err != nil {
				return nil, nil, err
			}
			projected[name] = child
			losses = append(losses, childLosses...)
		}
		result["$defs"] = projected
	}
	if properties, present := object["properties"].(map[string]any); present {
		projected := map[string]any{}
		removed := map[string]bool{}
		for name, property := range properties {
			propertyObject, _ := property.(map[string]any)
			if request && propertyObject["readOnly"] == true {
				removed[name] = true
				continue
			}
			child, childLosses, err := projectSwagger20SchemaValue(property, request, sourceRef+"/properties/"+escapeJSONPointerSegment(name))
			if err != nil {
				return nil, nil, err
			}
			projected[name] = child
			losses = append(losses, childLosses...)
		}
		result["properties"] = projected
		if required, ok := result["required"].([]any); ok && len(removed) > 0 {
			kept := required[:0]
			for _, rawName := range required {
				name, _ := rawName.(string)
				if !removed[name] {
					kept = append(kept, rawName)
				}
			}
			if len(kept) == 0 {
				delete(result, "required")
			} else {
				result["required"] = kept
			}
		}
	}
	if _, present := object["discriminator"]; present {
		losses = append(losses, swagger20ProjectionLoss{
			sourceRef: sourceRef + "/discriminator", reasonCode: "openapi20.discriminator_projection_loss",
			message: "the OAS 2.0 discriminator annotation has no Core JSON Schema assertion with equivalent artifact semantics",
		})
	}
	return result, losses, nil
}

func swagger20AlternativeCoverage(operation openapiprovider.Swagger20SynthesisOperation, operationKey, bindingKey string) []synthesize.SynthesisCoverageEntry {
	var result []synthesize.SynthesisCoverageEntry
	hasUsableServer := false
	hasInvalidServer := false
	for _, alternative := range operation.Alternatives {
		hasUsableServer = hasUsableServer || alternative.Kind == "server" && alternative.Usable
		hasInvalidServer = hasInvalidServer || alternative.Kind == "server" && alternative.Disposition == "invalid"
	}
	for _, projection := range operation.ParameterCoverage {
		result = append(result, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: projection.SourceRef, Scope: synthesize.SynthesisCoverageProjection,
			Status: synthesize.SynthesisCoverageStatus(projection.Status), Rule: projection.Rule,
			ReasonCode: "openapi20.parameter_projection_" + projection.Status, Message: projection.Reason,
		})
	}
	for _, alternative := range operation.Alternatives {
		if alternative.Kind == "security" {
			continue
		}
		if alternative.Kind == "server" {
			isWebSocket := strings.Contains(alternative.Reason, `scheme "ws"`) || strings.Contains(alternative.Reason, `scheme "wss"`)
			if alternative.Usable && !hasInvalidServer || isWebSocket && !hasUsableServer {
				continue
			}
		}
		entry := synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: alternative.SourceRef, Scope: synthesize.SynthesisCoverageAlternative,
			OperationKey: operationKey, BindingKey: bindingKey, BindingSelector: operation.Ref,
			Requirements: sortedUniqueStrings(alternative.Requirements),
		}
		if alternative.Usable {
			entry.Status = synthesize.SynthesisRepresented
		} else {
			entry.Status = synthesize.SynthesisExcluded
			if alternative.Disposition == "invalid" {
				entry.Status = synthesize.SynthesisInvalid
			}
			kind := strings.ReplaceAll(alternative.Kind, "requestMedia", "request_media")
			kind = strings.ReplaceAll(kind, "responseMedia", "response_media")
			entry.ReasonCode = "openapi20." + kind + "_excluded"
			entry.Rule = alternative.Rule
			if entry.Rule == "" {
				entry.Rule = swagger20AlternativeRule(alternative.Kind)
			}
			if alternative.Kind == "requestMedia" && strings.Contains(alternative.Reason, "cannot safely represent form parameter name") {
				entry.Rule = "OAPI20-P-25"
			}
			entry.Message = alternative.Reason
			entry.OperationKey, entry.BindingKey, entry.BindingSelector = "", "", ""
		}
		result = append(result, entry)
	}
	for _, security := range operation.Security {
		if security.Usable {
			continue
		}
		entry := synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: security.SourceRef, Scope: synthesize.SynthesisCoverageAlternative,
			OperationKey: operationKey, BindingKey: bindingKey, BindingSelector: operation.Ref,
		}
		entry.Status = synthesize.SynthesisExcluded
		entry.ReasonCode = "openapi20.security_alternative_excluded"
		entry.Rule = security.Rule
		if entry.Rule == "" {
			entry.Rule = "OAPI20-P-04"
		}
		entry.Message = security.Reason
		entry.OperationKey, entry.BindingKey, entry.BindingSelector = "", "", ""
		result = append(result, entry)
	}
	for _, response := range operation.Responses {
		if len(operation.Responses) == 1 && response.Key == "default" && response.SchemaPresent && response.Usable {
			result = append(result, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: response.SourceRef + "/schema", Scope: synthesize.SynthesisCoverageProjection,
				Status: synthesize.SynthesisRepresented, OperationKey: operationKey, BindingSelector: operation.Ref,
			})
		}
		if !response.CanSucceed && response.Reason != "" {
			result = append(result, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: response.SourceRef, Scope: synthesize.SynthesisCoverageProjection,
				Status: synthesize.SynthesisInvalid, ReasonCode: "openapi20.invalid_declaration",
				Rule: "OAPI20-P-07", Message: response.Reason,
			})
		}
		if response.SchemaPresent && (response.Key == "204" || response.Key == "205" || response.Key == "304") {
			result = append(result, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: response.SourceRef + "/schema", Scope: synthesize.SynthesisCoverageProjection,
				Status: synthesize.SynthesisExcluded, ReasonCode: "openapi20.excluded_declaration",
				Rule: "OAPI20-S-03", Message: "response " + response.Key + " cannot carry response content",
			})
		}
		for _, header := range response.Headers {
			entry := synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: header.SourceRef, Scope: synthesize.SynthesisCoverageProjection,
				OperationKey: operationKey, BindingKey: bindingKey, BindingSelector: operation.Ref,
			}
			if header.Usable {
				entry.Status = synthesize.SynthesisRepresented
			} else {
				entry.Status = synthesize.SynthesisExcluded
				if strings.Contains(header.Reason, "not admitted") || strings.Contains(header.Reason, "not an object") {
					entry.Status = synthesize.SynthesisInvalid
				}
				entry.ReasonCode = "openapi20.response_header_excluded"
				if strings.Contains(header.Reason, "field name") {
					entry.Rule = "OAPI20-S-04"
				}
				entry.Message = header.Reason
				entry.OperationKey, entry.BindingKey, entry.BindingSelector = "", "", ""
			}
			result = append(result, entry)
		}
	}
	return result
}

func swagger20AlternativeRule(kind string) string {
	switch kind {
	case "server", "security":
		return "OAPI20-P-04"
	default:
		return "OAPI20-P-03"
	}
}

func swagger20TargetRule(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "response"), strings.Contains(lower, "consumes"), strings.Contains(lower, "produces"), strings.Contains(lower, "payload"):
		return "OAPI20-P-03"
	case strings.Contains(lower, "security"), strings.Contains(lower, "scheme"), strings.Contains(lower, "host"), strings.Contains(lower, "server"):
		return "OAPI20-P-04"
	case strings.Contains(lower, "parameter"), strings.Contains(lower, "path template"):
		return "OAPI20-P-02"
	default:
		return "OAPI20-P-01"
	}
}

func swagger20TargetReasonCode(reason string) string {
	return "openapi20.target_excluded"
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func stringSliceAsAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
