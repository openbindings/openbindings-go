package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	maxExternalReferenceDocuments = 64
	maxExternalReferenceBytes     = 40 << 20
)

// artifactReferenceDocument retains the base URI against which references in
// one physical AsyncAPI document resolve. The primary document is special:
// its internal refs can remain in place for the typed resolver, whereas an
// internal ref in an external document must be resolved before its target is
// transplanted into the primary tree.
type artifactReferenceDocument struct {
	location string
	root     map[string]any
	primary  bool
}

type artifactReferenceResolver struct {
	ctx        context.Context
	client     *http.Client
	documents  map[string]*artifactReferenceDocument
	fetchCount int
	totalBytes int
}

// resolveArtifactReferences applies the AsyncAPI artifact's native external
// reference composition before the existing typed parser runs. It leaves the
// primary document's internal refs intact, but fully resolves refs inside an
// external fragment so those pointers do not accidentally rebase onto the
// primary document after composition.
func resolveArtifactReferences(ctx context.Context, client *http.Client, location string, data []byte) ([]byte, error) {
	root, err := decodeReferenceDocument(data)
	if err != nil {
		return nil, err
	}
	if !containsExternalReference(root) {
		return data, nil
	}

	base, err := referenceDocumentLocation(location)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = newDefaultHTTPClient()
	}
	primary := &artifactReferenceDocument{location: base, root: root, primary: true}
	resolver := &artifactReferenceResolver{
		ctx:        ctx,
		client:     client,
		documents:  map[string]*artifactReferenceDocument{},
		totalBytes: len(data),
	}
	if base != "" {
		resolver.documents[base] = primary
	}

	resolved, err := resolver.walk(root, primary, map[string]bool{})
	if err != nil {
		return nil, fmt.Errorf("resolve AsyncAPI references: %w", err)
	}
	resolvedRoot, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resolve AsyncAPI references: document root is not an object")
	}
	normalizeResolvedReferenceUnions(resolvedRoot)
	encoded, err := json.Marshal(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("encode resolved AsyncAPI document: %w", err)
	}
	return encoded, nil
}

func decodeReferenceDocument(data []byte) (map[string]any, error) {
	var root map[string]any
	if isJSON(data) {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("parse AsyncAPI JSON: %w", err)
		}
	} else if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse AsyncAPI YAML: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("parse AsyncAPI document: root is not an object")
	}
	return root, nil
}

func containsExternalReference(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			parsed, err := url.Parse(ref)
			if err == nil && (parsed.Path != "" || parsed.Host != "" || parsed.Scheme != "" || parsed.RawQuery != "") {
				return true
			}
		}
		for _, child := range typed {
			if containsExternalReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsExternalReference(child) {
				return true
			}
		}
	}
	return false
}

func referenceDocumentLocation(location string) (string, error) {
	if location == "" {
		return "", nil
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return "", fmt.Errorf("AsyncAPI reference base %q is not an absolute hierarchical URI", location)
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func (resolver *artifactReferenceResolver) walk(value any, current *artifactReferenceDocument, stack map[string]bool) (any, error) {
	switch typed := value.(type) {
	case []any:
		for index, child := range typed {
			resolved, err := resolver.walk(child, current, stack)
			if err != nil {
				return nil, err
			}
			typed[index] = resolved
		}
		return typed, nil
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			targetDocument, fragment, external, err := resolver.referenceTarget(current, ref)
			if err != nil {
				return nil, err
			}
			if external || !current.primary {
				key := targetDocument.location + "#" + fragment
				if targetDocument.location == "" {
					key = "<inline>#" + fragment
				}
				if stack[key] {
					return nil, fmt.Errorf("cyclic external reference at %q cannot be composed without changing its reference base", ref)
				}
				target, err := resolveReferenceFragment(targetDocument.root, fragment)
				if err != nil {
					return nil, fmt.Errorf("reference %q: %w", ref, err)
				}
				stack[key] = true
				resolved, err := resolver.walk(cloneReferenceValue(target), targetDocument, stack)
				delete(stack, key)
				if err != nil {
					return nil, err
				}
				// Reference Object siblings such as summary/description remain
				// local declarations and therefore override the resolved target.
				if object, ok := resolved.(map[string]any); ok {
					for name, sibling := range typed {
						if name == "$ref" {
							continue
						}
						resolvedSibling, siblingErr := resolver.walk(sibling, current, stack)
						if siblingErr != nil {
							return nil, siblingErr
						}
						object[name] = resolvedSibling
					}
				}
				return resolved, nil
			}
		}

		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			resolved, err := resolver.walk(typed[key], current, stack)
			if err != nil {
				return nil, err
			}
			typed[key] = resolved
		}
		return typed, nil
	default:
		return value, nil
	}
}

func (resolver *artifactReferenceResolver) referenceTarget(current *artifactReferenceDocument, ref string) (*artifactReferenceDocument, string, bool, error) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid reference %q: %w", ref, err)
	}
	external := parsed.Path != "" || parsed.Host != "" || parsed.Scheme != "" || parsed.RawQuery != ""
	fragment := parsed.Fragment
	if !external {
		return current, fragment, false, nil
	}
	if !parsed.IsAbs() {
		if current.location == "" {
			return nil, "", true, fmt.Errorf("relative reference %q has no document base URI", ref)
		}
		base, _ := url.Parse(current.location)
		parsed = base.ResolveReference(parsed)
	}
	fragment = parsed.Fragment
	parsed.Fragment = ""
	parsed.RawFragment = ""
	location := parsed.String()
	if existing := resolver.documents[location]; existing != nil {
		return existing, fragment, true, nil
	}
	if resolver.fetchCount >= maxExternalReferenceDocuments {
		return nil, "", true, fmt.Errorf("external reference closure exceeds %d documents", maxExternalReferenceDocuments)
	}
	data, err := sourceToBytes(resolver.ctx, resolver.client, location, nil)
	if err != nil {
		return nil, "", true, err
	}
	resolver.fetchCount++
	resolver.totalBytes += len(data)
	if resolver.totalBytes > maxExternalReferenceBytes {
		return nil, "", true, fmt.Errorf("external reference closure exceeds %d bytes", maxExternalReferenceBytes)
	}
	root, err := decodeReferenceDocument(data)
	if err != nil {
		return nil, "", true, fmt.Errorf("parse external reference document %q: %w", location, err)
	}
	document := &artifactReferenceDocument{location: location, root: root}
	resolver.documents[location] = document
	return document, fragment, true, nil
}

func resolveReferenceFragment(root any, fragment string) (any, error) {
	if fragment == "" {
		return root, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, fmt.Errorf("URI-fragment anchors are not supported; expected a JSON Pointer")
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		if strings.Contains(encoded, "~") {
			for index := 0; index < len(encoded); index++ {
				if encoded[index] == '~' && (index+1 >= len(encoded) || (encoded[index+1] != '0' && encoded[index+1] != '1')) {
					return nil, fmt.Errorf("invalid RFC 6901 escape in fragment %q", fragment)
				}
			}
		}
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var present bool
			current, present = value[token]
			if !present {
				return nil, fmt.Errorf("JSON Pointer fragment %q does not resolve", fragment)
			}
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("JSON Pointer fragment %q has an invalid array index", fragment)
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, fmt.Errorf("JSON Pointer fragment %q does not resolve", fragment)
			}
			current = value[index]
		default:
			return nil, fmt.Errorf("JSON Pointer fragment %q traverses a non-container", fragment)
		}
	}
	return current, nil
}

func cloneReferenceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, child := range typed {
			clone[key] = cloneReferenceValue(child)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, child := range typed {
			clone[index] = cloneReferenceValue(child)
		}
		return clone
	default:
		return value
	}
}

// The internal document model stores operation channels, operation messages,
// reply channels/messages, and channel server subsets as references. External
// resolution yields inline objects at those positions, so hoist them into the
// corresponding primary-document maps and rewrite only those structural union
// positions to deterministic internal pointers before typed decoding.
func normalizeResolvedReferenceUnions(root map[string]any) {
	servers := ensureReferenceObjectMap(root, "servers")
	channels := ensureReferenceObjectMap(root, "channels")
	operations, _ := root["operations"].(map[string]any)

	normalizeChannel := func(channelName string, raw any) {
		channel, ok := raw.(map[string]any)
		if !ok {
			return
		}
		rawServers, _ := channel["servers"].([]any)
		for index, rawServer := range rawServers {
			server, ok := rawServer.(map[string]any)
			if !ok || server["$ref"] != nil {
				continue
			}
			name := uniqueReferenceKey(servers, "__openbindings_external_"+sanitizeReferenceKey(channelName)+"_server_"+strconv.Itoa(index))
			servers[name] = server
			rawServers[index] = map[string]any{"$ref": "#/servers/" + escapeReferenceToken(name)}
		}
	}

	channelNames := sortedReferenceKeys(channels)
	for _, name := range channelNames {
		normalizeChannel(name, channels[name])
	}

	for _, operationName := range sortedReferenceKeys(operations) {
		operation, ok := operations[operationName].(map[string]any)
		if !ok {
			continue
		}
		if inline, ok := inlineReferenceObject(operation["channel"]); ok {
			name := uniqueReferenceKey(channels, "__openbindings_external_"+sanitizeReferenceKey(operationName)+"_channel")
			channels[name] = inline
			normalizeChannel(name, inline)
			operation["channel"] = map[string]any{"$ref": "#/channels/" + escapeReferenceToken(name)}
		}
		normalizeInlineMessages(root, operationName+"_message", operation, "messages")
		if reply, ok := operation["reply"].(map[string]any); ok {
			if inline, ok := inlineReferenceObject(reply["channel"]); ok {
				name := uniqueReferenceKey(channels, "__openbindings_external_"+sanitizeReferenceKey(operationName)+"_reply_channel")
				channels[name] = inline
				normalizeChannel(name, inline)
				reply["channel"] = map[string]any{"$ref": "#/channels/" + escapeReferenceToken(name)}
			}
			normalizeInlineMessages(root, operationName+"_reply_message", reply, "messages")
		}
	}

	if len(servers) == 0 {
		delete(root, "servers")
	}
	if len(channels) == 0 {
		delete(root, "channels")
	}
}

func normalizeInlineMessages(root map[string]any, prefix string, owner map[string]any, field string) {
	values, _ := owner[field].([]any)
	for index, raw := range values {
		message, ok := inlineReferenceObject(raw)
		if !ok {
			continue
		}
		components := ensureReferenceObjectMap(root, "components")
		messages := ensureReferenceObjectMap(components, "messages")
		name := uniqueReferenceKey(messages, "__openbindings_external_"+sanitizeReferenceKey(prefix)+"_"+strconv.Itoa(index))
		messages[name] = message
		values[index] = map[string]any{"$ref": "#/components/messages/" + escapeReferenceToken(name)}
	}
}

func inlineReferenceObject(raw any) (map[string]any, bool) {
	object, ok := raw.(map[string]any)
	return object, ok && object["$ref"] == nil
}

func ensureReferenceObjectMap(owner map[string]any, field string) map[string]any {
	if existing, ok := owner[field].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	owner[field] = created
	return created
}

func sortedReferenceKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueReferenceKey(values map[string]any, base string) string {
	if _, exists := values[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := base + "_" + strconv.Itoa(suffix)
		if _, exists := values[candidate]; !exists {
			return candidate
		}
	}
}

func sanitizeReferenceKey(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "item"
	}
	return builder.String()
}

func escapeReferenceToken(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
