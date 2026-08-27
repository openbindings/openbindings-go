package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/invoke"
)

func propertyMediaMap(bindCtx map[string]any) (map[string]any, bool, error) {
	raw, present := invoke.ContextConfiguration(bindCtx)["propertyMedia"]
	if !present || raw == nil {
		return nil, false, nil
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, true, nil
	case map[string]string:
		result := make(map[string]any, len(value))
		for name, mediaType := range value {
			result[name] = mediaType
		}
		return result, true, nil
	default:
		return nil, true, fmt.Errorf("configuration.propertyMedia must be an object keyed by property name")
	}
}

func configuredPropertyMedia(plan *bodyPlan, bindCtx map[string]any) (map[string]string, error) {
	if plan == nil || len(plan.propertyMedia) == 0 {
		return nil, nil
	}
	configured, _, err := propertyMediaMap(bindCtx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(plan.propertyMedia))
	for _, name := range plan.propertyMedia {
		raw, present := configured[name]
		if !present || raw == nil {
			return nil, &configRequired{
				point:       "propertyMedia",
				path:        "/" + escapeJSONPointerSegment(name),
				description: fmt.Sprintf("OpenAPI property %q requires one concrete propertyMedia choice", name),
			}
		}
		choice, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("configuration.propertyMedia.%s must be a concrete media-type string", name)
		}
		selected, selectErr := selectPropertyMedia(plan, name, choice)
		if selectErr != nil {
			return nil, selectErr
		}
		result[name] = selected
	}
	return result, nil
}

func selectPropertyMedia(plan *bodyPlan, name, choice string) (string, error) {
	wanted, err := parseRevision3MediaType(choice)
	if err != nil || wanted.rangeSpecificity != 2 {
		if err == nil {
			err = fmt.Errorf("media ranges are not concrete")
		}
		return "", fmt.Errorf("configuration.propertyMedia.%s: %w", name, err)
	}
	declaredContentType := plan.propertyMediaDeclarations[name]
	if declaredContentType == "" {
		// OAS 3.0's typeless multipart cell has no artifact default or member
		// to narrow. The configured concrete type fills that missing choice.
		return wanted.canonical, nil
	}
	members, err := splitHTTPList(declaredContentType)
	if err != nil {
		return "", fmt.Errorf("property %q Encoding contentType: %w", name, err)
	}
	type match struct {
		declaration parsedMediaType
	}
	identities := map[string]int{}
	parsedMembers := make([]parsedMediaType, 0, len(members))
	for _, member := range members {
		declared, parseErr := parseMediaDeclaration(member)
		if parseErr != nil {
			return "", fmt.Errorf("property %q Encoding contentType: %w", name, parseErr)
		}
		identities[declared.identity]++
		parsedMembers = append(parsedMembers, declared)
	}
	var best []match
	bestSpecificity, bestParams := -1, -1
	for _, declared := range parsedMembers {
		// Normalized collisions are confined to the colliding declaration and
		// therefore cannot be selected by a configured concrete value.
		if identities[declared.identity] > 1 || !requestMediaDeclarationMatches(declared, wanted) {
			continue
		}
		specificity, parameterCount := declared.rangeSpecificity, len(declared.params)
		switch {
		case specificity > bestSpecificity || (specificity == bestSpecificity && parameterCount > bestParams):
			bestSpecificity, bestParams = specificity, parameterCount
			best = []match{{declaration: declared}}
		case specificity == bestSpecificity && parameterCount == bestParams:
			best = append(best, match{declaration: declared})
		}
	}
	if len(best) == 0 {
		return "", fmt.Errorf("configuration.propertyMedia.%s %q matches no declared Encoding contentType member", name, choice)
	}
	if len(best) > 1 {
		labels := make([]string, len(best))
		for index, candidate := range best {
			labels[index] = candidate.declaration.canonical
		}
		sort.Strings(labels)
		return "", fmt.Errorf("configuration.propertyMedia.%s %q ambiguously matches %s", name, choice, strings.Join(labels, ", "))
	}
	return wanted.canonical, nil
}
