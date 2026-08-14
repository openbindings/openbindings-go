package asyncapi

import "strings"

// The Avro correspondence (§9.2's named-correspondence list, ruled
// 2026-08-14): an Avro-declared payload crosses the boundary as logical
// application values under the Avro specification's own JSON Encoding.
// deriveAvroSchema maps the Avro schema to the JSON Schema of its
// Avro-JSON-encoded data, under the specification's pinned table. Named
// types materialize once under `$defs` keyed by Avro fullname; recursive
// references become `$ref: "#/$defs/<fullname>"`, which the cyclic-hoisting
// pass rewrites to the operation schema's own pointer at emission.
// A declaration that does not parse as an Avro schema reports !ok and is
// handled as an inexpressible contract under the byte rule.

type avroContext struct {
	defs       map[string]any
	inProgress map[string]bool
	shortNames map[string]string // short name -> fullname, first definition wins
}

func deriveAvroSchema(schema any) (map[string]any, bool) {
	ctx := &avroContext{defs: map[string]any{}, inProgress: map[string]bool{}, shortNames: map[string]string{}}
	derived, ok := deriveAvro(schema, "", ctx)
	if !ok {
		return nil, false
	}
	if len(ctx.defs) > 0 {
		derived["$defs"] = ctx.defs
	}
	return derived, true
}

var avroBytesPattern = "^[\\u0000-\\u00ff]*$"

func deriveAvro(node any, namespace string, ctx *avroContext) (map[string]any, bool) {
	switch v := node.(type) {
	case string:
		return deriveAvroName(v, namespace, ctx)
	case []any:
		// A union: null branch as the null schema; every other branch as
		// the JSON Encoding's single-key wrapper, in declaration order.
		branches := make([]any, 0, len(v))
		for _, member := range v {
			if s, isStr := member.(string); isStr && s == "null" {
				branches = append(branches, map[string]any{"type": "null"})
				continue
			}
			derived, ok := deriveAvro(member, namespace, ctx)
			if !ok {
				return nil, false
			}
			name, ok := avroBranchName(member, namespace, ctx)
			if !ok {
				return nil, false
			}
			branches = append(branches, map[string]any{
				"type":                 "object",
				"properties":           map[string]any{name: derived},
				"required":             []any{name},
				"additionalProperties": false,
			})
		}
		if len(branches) == 0 {
			return nil, false
		}
		return map[string]any{"oneOf": branches}, true
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "record", "error":
			return deriveAvroRecord(v, namespace, ctx)
		case "enum":
			symbols, ok := v["symbols"].([]any)
			if !ok {
				return nil, false
			}
			full, ok := registerAvroName(v, namespace, ctx)
			if !ok {
				return nil, false
			}
			ctx.defs[full] = map[string]any{"enum": append([]any(nil), symbols...)}
			delete(ctx.inProgress, full)
			return map[string]any{"$ref": "#/$defs/" + escapeDefsPointerSegment(full)}, true
		case "array":
			items, ok := deriveAvro(v["items"], namespace, ctx)
			if !ok {
				return nil, false
			}
			return map[string]any{"type": "array", "items": items}, true
		case "map":
			values, ok := deriveAvro(v["values"], namespace, ctx)
			if !ok {
				return nil, false
			}
			return map[string]any{"type": "object", "additionalProperties": values}, true
		case "fixed":
			size, sok := avroInt(v["size"])
			full, ok := registerAvroName(v, namespace, ctx)
			if !sok || !ok {
				return nil, false
			}
			ctx.defs[full] = map[string]any{"type": "string", "pattern": avroBytesPattern, "minLength": size, "maxLength": size}
			delete(ctx.inProgress, full)
			return map[string]any{"$ref": "#/$defs/" + escapeDefsPointerSegment(full)}, true
		case "":
			return nil, false
		default:
			// A primitive spelled in object form ({"type":"long"}), with or
			// without a logicalType member: the JSON Encoding is of the
			// underlying type.
			return deriveAvroName(typ, namespace, ctx)
		}
	default:
		return nil, false
	}
}

// avroBranchName is the JSON Encoding's union wrapper key: the named
// type's fullname, or the type keyword for primitives and unnamed complex
// types ("array", "map", "fixed" uses its fullname).
func avroBranchName(member any, namespace string, ctx *avroContext) (string, bool) {
	switch v := member.(type) {
	case string:
		switch v {
		case "boolean", "int", "long", "float", "double", "bytes", "string":
			return v, true
		}
		full := resolveAvroName(v, namespace, ctx)
		if full == "" {
			return "", false
		}
		return full, true
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "record", "error", "enum", "fixed":
			name, ok := v["name"].(string)
			if !ok || name == "" {
				return "", false
			}
			ns, _ := v["namespace"].(string)
			if ns == "" && !strings.Contains(name, ".") {
				ns = namespace
			}
			if ns != "" && !strings.Contains(name, ".") {
				return ns + "." + name, true
			}
			return name, true
		case "array", "map":
			return typ, true
		default:
			// Primitive in object form.
			switch typ {
			case "boolean", "int", "long", "float", "double", "bytes", "string":
				return typ, true
			}
			return "", false
		}
	default:
		return "", false
	}
}

func deriveAvroRecord(v map[string]any, namespace string, ctx *avroContext) (map[string]any, bool) {
	full, ok := registerAvroName(v, namespace, ctx)
	if !ok {
		return nil, false
	}
	childNS := full[:strings.LastIndex(full+".", ".")]
	if i := strings.LastIndex(full, "."); i >= 0 {
		childNS = full[:i]
	} else {
		childNS = ""
	}
	fields, ok := v["fields"].([]any)
	if !ok {
		return nil, false
	}
	properties := map[string]any{}
	required := make([]any, 0, len(fields))
	for _, rawField := range fields {
		field, isMap := rawField.(map[string]any)
		if !isMap {
			return nil, false
		}
		name, isStr := field["name"].(string)
		if !isStr || name == "" {
			return nil, false
		}
		derived, ok := deriveAvro(field["type"], childNS, ctx)
		if !ok {
			return nil, false
		}
		properties[name] = derived
		required = append(required, name)
	}
	ctx.defs[full] = map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	delete(ctx.inProgress, full)
	return map[string]any{"$ref": "#/$defs/" + escapeDefsPointerSegment(full)}, true
}

// deriveAvroName handles primitives and references to named types.
func deriveAvroName(name, namespace string, ctx *avroContext) (map[string]any, bool) {
	switch name {
	case "null":
		return map[string]any{"type": "null"}, true
	case "boolean":
		return map[string]any{"type": "boolean"}, true
	case "int":
		return map[string]any{"type": "integer", "minimum": -2147483648, "maximum": 2147483647}, true
	case "long":
		return map[string]any{"type": "integer"}, true
	case "float", "double":
		return map[string]any{"type": "number"}, true
	case "bytes":
		return map[string]any{"type": "string", "pattern": avroBytesPattern}, true
	case "string":
		return map[string]any{"type": "string"}, true
	}
	full := resolveAvroName(name, namespace, ctx)
	if full == "" {
		return nil, false
	}
	if _, defined := ctx.defs[full]; defined || ctx.inProgress[full] {
		return map[string]any{"$ref": "#/$defs/" + escapeDefsPointerSegment(full)}, true
	}
	return nil, false
}

func resolveAvroName(name, namespace string, ctx *avroContext) string {
	if strings.Contains(name, ".") {
		return name
	}
	if namespace != "" {
		candidate := namespace + "." + name
		if _, defined := ctx.defs[candidate]; defined || ctx.inProgress[candidate] {
			return candidate
		}
	}
	if full, known := ctx.shortNames[name]; known {
		return full
	}
	if _, defined := ctx.defs[name]; defined || ctx.inProgress[name] {
		return name
	}
	return ""
}

func registerAvroName(v map[string]any, namespace string, ctx *avroContext) (string, bool) {
	name, ok := v["name"].(string)
	if !ok || name == "" {
		return "", false
	}
	ns, _ := v["namespace"].(string)
	if ns == "" && !strings.Contains(name, ".") {
		ns = namespace
	}
	full := name
	if ns != "" && !strings.Contains(name, ".") {
		full = ns + "." + name
	}
	if _, taken := ctx.shortNames[shortAvroName(full)]; !taken {
		ctx.shortNames[shortAvroName(full)] = full
	}
	ctx.inProgress[full] = true
	return full, true
}

func shortAvroName(full string) string {
	if i := strings.LastIndex(full, "."); i >= 0 {
		return full[i+1:]
	}
	return full
}

func avroInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) && n >= 0 {
			return int(n), true
		}
	case int:
		if n >= 0 {
			return n, true
		}
	}
	return 0, false
}
