package asyncapi

import (
	"encoding/json"
	"strings"
)

// resolveRefs resolves all internal $ref pointers (JSON Pointers starting with
// #/) within a parsed AsyncAPI document. It mutates the document in place.
// Only internal refs are handled; external refs (http://, ./file.json) are
// left as-is.
func resolveRefs(doc *Document) {
	if doc == nil {
		return
	}

	// Build a raw representation of the full document for pointer lookups.
	// We marshal then unmarshal to get a uniform map[string]any tree.
	rawBytes, err := json.Marshal(doc)
	if err != nil {
		return
	}
	var rawDoc map[string]any
	if err := json.Unmarshal(rawBytes, &rawDoc); err != nil {
		return
	}

	// Resolve $refs in schema maps (map[string]any) throughout the document.
	// Schemas live in: message payloads, component schemas, etc.

	// 1. Resolve component schemas themselves (they can reference each other).
	if doc.Components != nil {
		for name, schema := range doc.Components.Schemas {
			if m, ok := schema.(map[string]any); ok {
				doc.Components.Schemas[name] = resolveSchemaRefs(m, rawDoc, nil)
			}
		}

		// 2. Resolve message payloads in components.
		for name, msg := range doc.Components.Messages {
			if msg.Payload != nil {
				msg.Payload = resolveSchemaRefs(msg.Payload, rawDoc, nil)
				doc.Components.Messages[name] = msg
			}
			// If the message itself is a $ref, it was already deserialized into
			// the Message struct with the Ref field set. We resolve it here.
			if msg.Ref != "" {
				if resolved := resolveMessageRefByPointer(msg.Ref, rawDoc); resolved != nil {
					// Preserve any fields from the original that aren't set in resolved.
					doc.Components.Messages[name] = *resolved
				}
			}
		}
	}

	// 3. Resolve message payloads in channels.
	for chName, ch := range doc.Channels {
		for msgName, msg := range ch.Messages {
			if msg.Ref != "" {
				if resolved := resolveMessageRefByPointer(msg.Ref, rawDoc); resolved != nil {
					msg = *resolved
				}
			}
			if msg.Payload != nil {
				msg.Payload = resolveSchemaRefs(msg.Payload, rawDoc, nil)
			}
			ch.Messages[msgName] = msg
		}
		doc.Channels[chName] = ch
	}

	// Note: message ref resolution and security scheme ref resolution are
	// handled at conversion time by create.go (resolveMessageRef looks up
	// $ref pointers against doc.Components.Messages, and security schemes are
	// looked up by name from doc.Components.SecuritySchemes). No additional
	// resolution is needed here beyond schemas and channel parameters above.
}

// resolveSchemaRefs recursively walks a map[string]any schema and replaces any
// {"$ref": "#/..."} objects with the referenced content from the raw document.
// The visited set tracks pointers to detect circular references.
func resolveSchemaRefs(schema map[string]any, rawDoc map[string]any, visited map[string]bool) map[string]any {
	if schema == nil {
		return nil
	}
	if visited == nil {
		visited = map[string]bool{}
	}

	// Check if this is a $ref object.
	if ref, ok := schema["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
		if visited[ref] {
			// Circular ref: return the schema as-is (keeps the $ref string).
			return schema
		}
		visited[ref] = true

		resolved := resolveJSONPointer(rawDoc, ref)
		if resolvedMap, ok := resolved.(map[string]any); ok {
			// Deep copy to avoid mutating the raw doc lookup cache.
			copied := deepCopyMap(resolvedMap)
			// Recursively resolve refs within the resolved schema.
			return resolveSchemaRefs(copied, rawDoc, visited)
		}
		// If resolution fails, return the original.
		return schema
	}

	// Recursively walk all values.
	for key, val := range schema {
		switch v := val.(type) {
		case map[string]any:
			schema[key] = resolveSchemaRefs(v, rawDoc, copyVisited(visited))
		case []any:
			schema[key] = resolveSchemaRefsInSlice(v, rawDoc, visited)
		}
	}

	return schema
}

// resolveSchemaRefsInSlice handles arrays within schemas (e.g., allOf, oneOf, anyOf, items).
func resolveSchemaRefsInSlice(arr []any, rawDoc map[string]any, visited map[string]bool) []any {
	for i, item := range arr {
		if m, ok := item.(map[string]any); ok {
			arr[i] = resolveSchemaRefs(m, rawDoc, copyVisited(visited))
		}
	}
	return arr
}

// resolveJSONPointer resolves a JSON Pointer (RFC 6901) like "#/components/schemas/Foo"
// against a raw document tree.
func resolveJSONPointer(root map[string]any, pointer string) any {
	fragment := strings.TrimPrefix(pointer, "#/")
	if fragment == "" || fragment == pointer {
		return nil
	}

	tokens := strings.Split(fragment, "/")
	var current any = root
	for _, token := range tokens {
		// Unescape JSON Pointer encoding.
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")

		switch v := current.(type) {
		case map[string]any:
			current = v[token]
		default:
			return nil
		}
		if current == nil {
			return nil
		}
	}
	return current
}

// resolveMessageRefByPointer resolves a message $ref pointer and returns a Message.
func resolveMessageRefByPointer(ref string, rawDoc map[string]any) *Message {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}

	resolved := resolveJSONPointer(rawDoc, ref)
	if resolved == nil {
		return nil
	}

	resolvedMap, ok := resolved.(map[string]any)
	if !ok {
		return nil
	}

	// Marshal back to JSON and unmarshal into Message struct.
	data, err := json.Marshal(resolvedMap)
	if err != nil {
		return nil
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}

	return &msg
}

// deepCopyMap creates a deep copy of a map[string]any.
func deepCopyMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			result[k] = deepCopyMap(val)
		case []any:
			result[k] = deepCopySlice(val)
		default:
			result[k] = v
		}
	}
	return result
}

// deepCopySlice creates a deep copy of a []any.
func deepCopySlice(s []any) []any {
	result := make([]any, len(s))
	for i, v := range s {
		switch val := v.(type) {
		case map[string]any:
			result[i] = deepCopyMap(val)
		case []any:
			result[i] = deepCopySlice(val)
		default:
			result[i] = v
		}
	}
	return result
}

// copyVisited creates a copy of the visited set for branching recursion paths.
func copyVisited(visited map[string]bool) map[string]bool {
	cp := make(map[string]bool, len(visited))
	for k, v := range visited {
		cp[k] = v
	}
	return cp
}
