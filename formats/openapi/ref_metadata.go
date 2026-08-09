package openapi

import "github.com/getkin/kin-openapi/openapi3"

// localizeReferenceMetadata reverses the private raw-loader marker used to
// prevent kin-openapi from applying OpenAPI 3.1 Reference Object metadata by
// mutating a shared resolved target. Each reference site receives a shallow
// target copy before its description override is applied; nested protocol
// objects remain shared because the Reference Object changes no other field.
func localizeReferenceMetadata(doc *openapi3.T) {
	if doc == nil {
		return
	}
	seenPathItems := map[*openapi3.PathItem]bool{}
	var walkPathItem func(*openapi3.PathItem)
	var walkCallback func(*openapi3.CallbackRef)

	localizeParameter := func(ref *openapi3.ParameterRef) {
		if ref == nil {
			return
		}
		summary, description, present := takeReferenceMetadata(ref.Extensions)
		if !present {
			return
		}
		ref.Summary, ref.Description = summary, description
		if ref.Value != nil && description != nil {
			copy := *ref.Value
			copy.Description = *description
			ref.Value = &copy
		}
	}
	localizeHeader := func(ref *openapi3.HeaderRef) {
		if ref == nil {
			return
		}
		summary, description, present := takeReferenceMetadata(ref.Extensions)
		if !present {
			return
		}
		ref.Summary, ref.Description = summary, description
		if ref.Value != nil && description != nil {
			copy := *ref.Value
			copy.Description = *description
			ref.Value = &copy
		}
	}
	localizeRequestBody := func(ref *openapi3.RequestBodyRef) {
		if ref == nil {
			return
		}
		summary, description, present := takeReferenceMetadata(ref.Extensions)
		if !present {
			return
		}
		ref.Summary, ref.Description = summary, description
		if ref.Value != nil && description != nil {
			copy := *ref.Value
			copy.Description = *description
			ref.Value = &copy
		}
	}
	localizeResponse := func(ref *openapi3.ResponseRef) {
		if ref == nil {
			return
		}
		summary, description, present := takeReferenceMetadata(ref.Extensions)
		if !present {
			return
		}
		ref.Summary, ref.Description = summary, description
		if ref.Value != nil && description != nil {
			copy := *ref.Value
			value := *description
			copy.Description = &value
			ref.Value = &copy
		}
	}
	localizeSecurityScheme := func(ref *openapi3.SecuritySchemeRef) {
		if ref == nil {
			return
		}
		summary, description, present := takeReferenceMetadata(ref.Extensions)
		if !present {
			return
		}
		ref.Summary, ref.Description = summary, description
		if ref.Value != nil && description != nil {
			copy := *ref.Value
			copy.Description = *description
			ref.Value = &copy
		}
	}

	walkCallback = func(ref *openapi3.CallbackRef) {
		if ref == nil || ref.Value == nil {
			return
		}
		for _, pathItem := range ref.Value.Map() {
			walkPathItem(pathItem)
		}
	}
	walkPathItem = func(pathItem *openapi3.PathItem) {
		if pathItem == nil || seenPathItems[pathItem] {
			return
		}
		seenPathItems[pathItem] = true
		for _, parameter := range pathItem.Parameters {
			localizeParameter(parameter)
		}
		for _, operation := range pathItem.Operations() {
			for _, parameter := range operation.Parameters {
				localizeParameter(parameter)
			}
			localizeRequestBody(operation.RequestBody)
			if operation.Responses != nil {
				for _, response := range operation.Responses.Map() {
					localizeResponse(response)
					if response != nil && response.Value != nil {
						for _, header := range response.Value.Headers {
							localizeHeader(header)
						}
					}
				}
			}
			for _, callback := range operation.Callbacks {
				walkCallback(callback)
			}
		}
	}

	if doc.Components != nil {
		for _, parameter := range doc.Components.Parameters {
			localizeParameter(parameter)
		}
		for _, header := range doc.Components.Headers {
			localizeHeader(header)
		}
		for _, requestBody := range doc.Components.RequestBodies {
			localizeRequestBody(requestBody)
		}
		for _, response := range doc.Components.Responses {
			localizeResponse(response)
		}
		for _, callback := range doc.Components.Callbacks {
			walkCallback(callback)
		}
		for _, securityScheme := range doc.Components.SecuritySchemes {
			localizeSecurityScheme(securityScheme)
		}
	}
	if doc.Paths != nil {
		for _, pathItem := range doc.Paths.Map() {
			walkPathItem(pathItem)
		}
	}
	for _, pathItem := range doc.Webhooks {
		walkPathItem(pathItem)
	}
}

func takeReferenceMetadata(extensions map[string]any) (summary, description *string, present bool) {
	if extensions == nil {
		return nil, nil, false
	}
	raw, found := extensions[referenceMetadataMarker]
	if !found {
		return nil, nil, false
	}
	delete(extensions, referenceMetadataMarker)
	metadata, _ := raw.(map[string]any)
	if value, ok := metadata["summary"].(string); ok {
		summary = &value
	}
	if value, ok := metadata["description"].(string); ok {
		description = &value
	}
	return summary, description, true
}
