package gemini

import (
	"encoding/json"
	"fmt"
	"rllmx/app/llm/translate"
	"strings"
)

// unsupportedSchemaKeys are JSON Schema keywords Gemini's function declarations
// reject with "Unknown name ...: Cannot find field", which fails the whole
// request. Agent tool schemas carry them routinely. The list follows the
// reference's own, by hand: one occurrence of any of these is enough to fail.
var unsupportedSchemaKeys = translate.Set(
	// Length, count and numeric bounds.
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum", "minItems", "maxItems", "format", "multipleOf",
	// Array keywords the Gemini schema proto has no field for.
	"uniqueItems", "contains",
	// 2020-12 keywords with no Gemini equivalent.
	"unevaluatedProperties", "unevaluatedItems", "contentSchema",
	// Tuple-array keywords, stripped rather than converted to items.
	"prefixItems", "additionalItems",
	// Rejected in Claude's validated mode.
	"default", "examples",
	// JSON Schema meta keywords.
	"$schema", "$id", "$defs", "definitions", "const", "$ref", "$comment",
	// Annotation keywords.
	"deprecated", "readOnly", "writeOnly",
	// Object validation keywords.
	"additionalProperties", "propertyNames", "patternProperties", "enumDescriptions", "minProperties", "maxProperties",
	// Any left after the merge and collapse below, and `not`, which has no form.
	"anyOf", "oneOf", "allOf", "not",
	// Dependency keywords.
	"dependencies", "dependentSchemas", "dependentRequired",
	// Other unsupported keywords.
	"title", "optional", "~optional", "if", "then", "else", "contentMediaType", "contentEncoding",
	// UI and styling properties some clients put in a tool schema.
	"cornerRadius", "fillColor", "fontFamily", "fontSize", "fontWeight",
	"gap", "padding", "strokeColor", "strokeThickness", "textColor",
	// Non-standard annotation and error keywords used by MCP tool schemas.
	"errorMessage", "errorMessages", "x-errorMessage", "x-errorMessages",
	"markdownDescription", "x-intellij-html-description",
	"x-taplo-info", "x-taplo", "doNotSuggest", "suggestSortText",
)

// CleanGeminiSchema rewrites a JSON Schema to the subset Gemini accepts: a const
// becomes a one-value string enum, enums are strings with an explicit type,
// allOf is merged, anyOf and oneOf collapse to their first non-null member, a
// type list keeps its first non-null type, and unsupported keywords go. Keys are
// emitted sorted, so equal schemas encode to equal bytes.
func CleanGeminiSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	out, err := json.Marshal(cleanSchemaNode(m))
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func cleanSchemaNode(n map[string]any) translate.Obj {
	n = mergeAllOf(n)
	n = collapseAny(n)
	out := translate.Obj{}
	for k, v := range n {
		if unsupportedSchemaKeys[k] {
			if k == "const" {
				out["enum"] = []any{fmt.Sprint(v)}
				out["type"] = "string"
			}
			continue
		}
		switch k {
		case "type":
			out["type"] = firstNonNullType(v)
		case "enum":
			if list, ok := v.([]any); ok {
				strs := make([]any, len(list))
				for i, e := range list {
					strs[i] = fmt.Sprint(e)
				}
				out["enum"] = strs
				out["type"] = "string"
			}
		case "properties":
			props := translate.Obj{}
			if pm, ok := v.(map[string]any); ok {
				for name, sub := range pm {
					if sm, ok := sub.(map[string]any); ok {
						props[name] = cleanSchemaNode(sm)
					}
				}
			}
			out["properties"] = props
		case "items":
			if im, ok := v.(map[string]any); ok {
				out["items"] = cleanSchemaNode(im)
			} else if list, ok := v.([]any); ok && len(list) > 0 {
				if im, ok := list[0].(map[string]any); ok {
					out["items"] = cleanSchemaNode(im)
				}
			}
		case "required":
			out["required"] = v
		default:
			out[k] = v
		}
	}
	// An enum is always strings with an explicit string type. This runs after the
	// loop so the order a map is walked in cannot let a numeric "type" win.
	if _, hasEnum := out["enum"]; hasEnum {
		out["type"] = "string"
	}
	// A required name that is not a property is refused by Gemini.
	if req, ok := out["required"].([]any); ok {
		props, _ := out["properties"].(translate.Obj)
		var kept []any
		for _, r := range req {
			if name, ok := r.(string); ok {
				if _, exists := props[name]; exists {
					kept = append(kept, name)
				}
			}
		}
		if len(kept) == 0 {
			delete(out, "required")
		} else {
			out["required"] = kept
		}
	}
	if _, ok := out["type"]; !ok {
		if _, hasProps := out["properties"]; hasProps {
			out["type"] = "object"
		}
	}
	return out
}

func firstNonNullType(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	for _, t := range list {
		if s, ok := t.(string); ok && s != "null" {
			return s
		}
	}
	return "string"
}

func mergeAllOf(n map[string]any) map[string]any {
	list, ok := n["allOf"].([]any)
	if !ok {
		return n
	}
	merged := map[string]any{}
	for k, v := range n {
		if k != "allOf" {
			merged[k] = v
		}
	}
	for _, item := range list {
		sub, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sub = mergeAllOf(sub)
		for k, v := range sub {
			switch k {
			case "properties":
				props, _ := merged["properties"].(map[string]any)
				if props == nil {
					props = map[string]any{}
				}
				if sp, ok := v.(map[string]any); ok {
					for name, s := range sp {
						props[name] = s
					}
				}
				merged["properties"] = props
			case "required":
				a, _ := merged["required"].([]any)
				if b, ok := v.([]any); ok {
					merged["required"] = append(a, b...)
				}
			default:
				if _, exists := merged[k]; !exists {
					merged[k] = v
				}
			}
		}
	}
	return merged
}

// collapseAny replaces anyOf and oneOf by their first member that is not null.
func collapseAny(n map[string]any) map[string]any {
	for _, key := range []string{"anyOf", "oneOf"} {
		list, ok := n[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		out := map[string]any{}
		for k, v := range n {
			if k != key {
				out[k] = v
			}
		}
		for _, item := range list {
			sub, ok := item.(map[string]any)
			if !ok || sub["type"] == "null" {
				continue
			}
			for k, v := range sub {
				if _, exists := out[k]; !exists {
					out[k] = v
				}
			}
			break
		}
		n = out
	}
	return n
}

// SanitizeGeminiName makes a function name Gemini accepts: it starts with a
// letter or underscore, holds letters, digits and _ . : -, and is at most 64
// characters.
func SanitizeGeminiName(name string) string {
	if name == "" {
		return "_unknown"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == ':', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if c := s[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_') {
		s = "_" + s
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
