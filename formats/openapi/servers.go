package openapi

import (
	"errors"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	openapiclient "github.com/openbindings/openapi-client/go"
	"github.com/openbindings/openbindings-go/invoke"
)

// resolveServer keeps OpenBindings configuration consultation in the adapter
// while delegating OpenAPI Server Object inheritance, eligibility, variables,
// relative resolution, and configured-base mechanics to the client.
func resolveServer(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, bindCtx map[string]any, sourceLocation string) (string, error) {
	set, err := openapiclient.EffectiveServerSet(doc, pathItem, op, sourceLocation)
	if err != nil {
		return "", err
	}
	if cfg := invoke.ContextConfiguration(bindCtx); cfg != nil {
		if raw, ok := cfg["server"]; ok && raw != nil {
			selection, err := adapterServerSelection(raw, set.Servers())
			if err != nil {
				return "", err
			}
			resolved, err := set.Resolve(selection)
			return resolved, adapterServerResolutionError(err)
		}
	}
	if meta := invoke.ContextMetadata(bindCtx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			resolved, err := set.Resolve(&openapiclient.ServerSelection{BaseURL: base})
			return resolved, adapterServerResolutionError(err)
		}
	}
	resolved, err := set.Resolve(nil)
	return resolved, adapterServerResolutionError(err)
}

func adapterServerSelection(raw any, servers openapi3.Servers) (*openapiclient.ServerSelection, error) {
	switch value := raw.(type) {
	case string:
		for _, server := range servers {
			if server != nil && server.URL == value {
				return &openapiclient.ServerSelection{URL: value}, nil
			}
		}
		if openapiclient.IsServerBaseURL(value) {
			return &openapiclient.ServerSelection{BaseURL: value}, nil
		}
		return nil, fmt.Errorf("configuration.server %q matches no declared server entry and is not an absolute base URL", value)
	case map[string]any:
		if base, ok := value["baseUrl"].(string); ok && base != "" {
			return &openapiclient.ServerSelection{BaseURL: base}, nil
		}
		selection := &openapiclient.ServerSelection{}
		if entryURL, ok := value["url"].(string); ok && entryURL != "" {
			selection.URL = entryURL
		} else if rawIndex, ok := value["index"]; ok {
			index, ok := configIndex(rawIndex)
			if !ok {
				return nil, fmt.Errorf("configuration.server.index %v is not an integer", rawIndex)
			}
			selection.Index = &index
		}
		if rawVariables, ok := value["variables"].(map[string]any); ok {
			selection.Variables = make(map[string]string, len(rawVariables))
			for name, rawValue := range rawVariables {
				text, ok := rawValue.(string)
				if !ok {
					return nil, fmt.Errorf("configuration.server.variables[%q] must be a string, got %T", name, rawValue)
				}
				selection.Variables[name] = text
			}
		}
		return selection, nil
	default:
		return nil, fmt.Errorf("configuration.server must be a string or an object, got %T", raw)
	}
}

func adapterServerResolutionError(err error) error {
	var required *openapiclient.ServerResolutionRequiredError
	if !errors.As(err, &required) {
		return err
	}
	durable := (*bool)(nil)
	if required.Path == "/url" {
		durable = &serverChoiceDurable
	}
	return &configRequired{
		point: "server", path: required.Path, description: required.Description,
		schema: enumSchema(required.Enum), durable: durable,
	}
}

// eligibleServers is retained for synthesis coverage; the eligibility
// decision itself is client-owned.
func eligibleServers(servers openapi3.Servers, edition, sourceLocation string) (openapi3.Servers, error) {
	set, err := openapiclient.NewServerSet(servers, edition, sourceLocation)
	if err != nil {
		return nil, err
	}
	return set.Servers(), nil
}

func configIndex(raw any) (int, bool) {
	switch number := raw.(type) {
	case int:
		return number, true
	case float64:
		if number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}

// configRequired is adapter vocabulary for a missing OpenBindings
// configuration point. OpenAPI-native resolution errors are translated into
// this shape only at this boundary.
type configRequired struct {
	point       string
	path        string
	description string
	schema      map[string]any
	durable     *bool
}

func (c *configRequired) Error() string { return c.description }

func enumSchema(values []string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	members := make([]any, len(values))
	for index, value := range values {
		members[index] = value
	}
	return map[string]any{"enum": members}
}

var serverChoiceDurable = true
