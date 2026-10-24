package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/log"
)

// cleanGeminiSchema recursively removes JSON Schema Draft 7/8 keywords
// that Google's Protobuf parser rejects for function declarations.
func cleanGeminiSchema(schema map[string]interface{}) {
	if schema == nil {
		return
	}

	// Keywords rejected by Gemini
	unsupported := []string{
		"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
		"minItems", "maxItems", "format", "multipleOf",
		// Array/2020-12 keywords the Gemini schema proto has no field for.
		// Agent tool schemas set these routinely; one occurrence rejects the
		// whole request (Next.js UNSUPPORTED_SCHEMA_CONSTRAINTS parity).
		"uniqueItems", "contains", "unevaluatedProperties", "unevaluatedItems", "contentSchema",
		"default", "examples",
		"$schema", "$defs", "definitions", "const", "$ref", "$comment",
		"deprecated", "readOnly", "writeOnly",
		"additionalProperties", "propertyNames", "patternProperties", "enumDescriptions",
		"allOf", "not",
		"dependencies", "dependentSchemas", "dependentRequired",
		"title", "optional", "if", "then", "else", "contentMediaType", "contentEncoding",
		"cornerRadius", "fillColor", "fontFamily", "fontSize", "fontWeight",
		"gap", "padding", "strokeColor", "strokeThickness", "textColor",
	}

	// stripUnsupported removes keywords Gemini rejects. Called again after anyOf/oneOf
	// flattening, which can re-inject them (e.g. const) from a merged branch.
	stripUnsupported := func(s map[string]interface{}) {
		for _, k := range unsupported {
			delete(s, k)
		}
		// Delete all vendor extensions (x- prefixes)
		for k := range s {
			if len(k) > 2 && k[0] == 'x' && k[1] == '-' {
				delete(s, k)
			}
		}
	}
	stripUnsupported(schema)

	// Ensure type="object" if properties exist (Gemini requirement)
	if _, hasProps := schema["properties"]; hasProps {
		if _, hasType := schema["type"]; !hasType {
			schema["type"] = "object"
		}
	}

	// Ensure enum is string array and type="string" (Gemini requirement)
	if enumRaw, hasEnum := schema["enum"]; hasEnum {
		if enumArr, ok := enumRaw.([]interface{}); ok {
			strArr := make([]string, 0, len(enumArr))
			for _, item := range enumArr {
				if s, ok := item.(string); ok {
					strArr = append(strArr, s)
				} else {
					if b, err := json.Marshal(item); err == nil {
						if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
							strArr = append(strArr, string(b[1:len(b)-1]))
						} else {
							strArr = append(strArr, string(b))
						}
					}
				}
			}
			schema["enum"] = strArr
			if _, hasType := schema["type"]; !hasType {
				schema["type"] = "string"
			}
		}
	}

	// Flatten anyOf/oneOf (Google doesn't support them)
	for _, key := range []string{"anyOf", "oneOf"} {
		if rawArr, has := schema[key]; has {
			if arr, ok := rawArr.([]interface{}); ok && len(arr) > 0 {
				for _, itemRaw := range arr {
					if item, ok := itemRaw.(map[string]interface{}); ok {
						if t, hasT := item["type"]; !hasT || t != "null" {
							for k, v := range item {
								schema[k] = v
							}
							break
						}
					}
				}
			}
			delete(schema, key)
		}
	}

	// anyOf/oneOf merge copies branch keys (including const/$ref/etc.) back in.
	stripUnsupported(schema)

	// Flatten type arrays
	if typeRaw, hasType := schema["type"]; hasType {
		if typeArr, ok := typeRaw.([]interface{}); ok && len(typeArr) > 0 {
			var firstValid string
			for _, tRaw := range typeArr {
				if t, ok := tRaw.(string); ok && t != "null" {
					firstValid = t
					break
				}
			}
			if firstValid != "" {
				schema["type"] = firstValid
			} else {
				schema["type"] = "string"
			}
		}
	}

	// Handle prefixItems (JSON Schema 2020-12 tuple) — Gemini only supports single items.
	if prefixItemsRaw, hasPrefix := schema["prefixItems"]; hasPrefix {
		if _, hasItems := schema["items"]; !hasItems {
			if arr, ok := prefixItemsRaw.([]interface{}); ok && len(arr) > 0 {
				if firstMap, ok := arr[0].(map[string]interface{}); ok {
					schema["items"] = firstMap
				}
			}
		}
		delete(schema, "prefixItems")
	}

	// Ensure every array has an items schema (Gemini protobuf requires it).
	// Missing items causes: "...items.items: missing field." or "...items: missing field."
	if t, ok := schema["type"].(string); ok && t == "array" {
		if _, hasItems := schema["items"]; !hasItems {
			schema["items"] = map[string]interface{}{"type": "string"}
		} else {
			// Tuple form: items: [ {...}, {...} ] — take first element as single schema.
			switch items := schema["items"].(type) {
			case []interface{}:
				if len(items) > 0 {
					if firstMap, ok := items[0].(map[string]interface{}); ok {
						schema["items"] = firstMap
					} else {
						schema["items"] = map[string]interface{}{"type": "string"}
					}
				} else {
					schema["items"] = map[string]interface{}{"type": "string"}
				}
			case map[string]interface{}:
				if len(items) == 0 {
					schema["items"] = map[string]interface{}{"type": "string"}
				} else if _, hasType := items["type"]; !hasType {
					if _, hasProps := items["properties"]; !hasProps {
						if _, hasItemsInner := items["items"]; !hasItemsInner {
							if _, hasEnum := items["enum"]; !hasEnum {
								if _, hasAnyOf := items["anyOf"]; !hasAnyOf {
									if _, hasOneOf := items["oneOf"]; !hasOneOf {
										items["type"] = "string"
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Fix misplaced `required` inside `properties` (common generator bug: required as property instead of sibling)
	// e.g. {"properties":{"query":{...},"required":["query"]}} -> {"properties":{"query":{...}},"required":["query"]}
	if propsRaw, hasProps := schema["properties"]; hasProps {
		if props, ok := propsRaw.(map[string]interface{}); ok {
			if reqInProps, hasReqInProps := props["required"]; hasReqInProps {
				// If value is not an object schema, it's misplaced required array
				if _, isObj := reqInProps.(map[string]interface{}); !isObj {
					delete(props, "required")
					if _, hasTopReq := schema["required"]; !hasTopReq {
						// Only promote if top-level required missing
						if reqArr, ok := reqInProps.([]interface{}); ok {
							schema["required"] = reqArr
						} else if reqStrArr, ok := reqInProps.([]string); ok {
							schema["required"] = reqStrArr
						} else {
							// Fallback: marshal and unmarshal to handle mixed types
							if b, err := json.Marshal(reqInProps); err == nil {
								var arr []interface{}
								if err := json.Unmarshal(b, &arr); err == nil {
									schema["required"] = arr
								}
							}
						}
					}
				}
			}
		}
	}

	// Recurse into properties definitions (each value is a property schema, NOT the container map)
	if propsRaw, hasProps := schema["properties"]; hasProps {
		if props, ok := propsRaw.(map[string]interface{}); ok {
			for _, propVal := range props {
				if propSchema, ok := propVal.(map[string]interface{}); ok {
					cleanGeminiSchema(propSchema)
				}
			}
		}
	}

	// Recurse into items (array schema element)
	if itemsRaw, hasItems := schema["items"]; hasItems {
		switch items := itemsRaw.(type) {
		case map[string]interface{}:
			cleanGeminiSchema(items)
		case []interface{}:
			for _, elem := range items {
				if elemMap, ok := elem.(map[string]interface{}); ok {
					cleanGeminiSchema(elemMap)
				}
			}
		}
	}

	// Recurse into additionalProperties if it is a schema (some generators put constraints there)
	if apRaw, hasAP := schema["additionalProperties"]; hasAP {
		if apMap, ok := apRaw.(map[string]interface{}); ok {
			cleanGeminiSchema(apMap)
			// additionalProperties is stripped above via unsupported list, but if
			// it survived (e.g. future list change), still ensure its content is clean.
		}
	}

	// Clean up required array (must only contain keys present in properties)
	if reqRaw, hasReq := schema["required"]; hasReq {
		var reqStrs []string
		switch arr := reqRaw.(type) {
		case []interface{}:
			for _, r := range arr {
				if s, ok := r.(string); ok {
					reqStrs = append(reqStrs, s)
				}
			}
		case []string:
			reqStrs = arr
		}

		if len(reqStrs) > 0 {
			var validReqs []string
			if propsRaw, hasProps := schema["properties"]; hasProps {
				if props, ok := propsRaw.(map[string]interface{}); ok {
					for _, rStr := range reqStrs {
						if _, exists := props[rStr]; exists {
							validReqs = append(validReqs, rStr)
						}
					}
				}
			}
			if len(validReqs) > 0 {
				schema["required"] = validReqs
			} else {
				delete(schema, "required")
			}
		} else {
			delete(schema, "required")
		}
	}

	// Add placeholder for empty object schemas (Antigravity requirement)
	if t, hasT := schema["type"]; hasT && t == "object" {
		needsPlaceholder := true
		if propsRaw, hasProps := schema["properties"]; hasProps {
			if props, ok := propsRaw.(map[string]interface{}); ok && len(props) > 0 {
				needsPlaceholder = false
			}
		}
		if needsPlaceholder {
			schema["properties"] = map[string]interface{}{
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Brief explanation of why you are calling this tool",
				},
			}
			schema["required"] = []string{"reason"}
		}
	}

	// Empty schema {} (no type at all) must also become the object placeholder
	if len(schema) == 0 {
		schema["type"] = "object"
		schema["properties"] = map[string]interface{}{
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Brief explanation of why you are calling this tool",
			},
		}
		schema["required"] = []string{"reason"}
	}
}

// CleanParametersSchema parses raw JSON schema, cleans it, and returns it.
func CleanParametersSchema(raw jsontext.Value) jsontext.Value {
	if len(raw) == 0 || string(raw) == "null" {
		return jsontext.Value(`{"type":"object","properties":{}}`)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return raw // fallback
	}

	cleanGeminiSchema(parsed)

	cleaned, err := json.Marshal(parsed)
	if err != nil {
		return raw // fallback
	}
	if string(cleaned) != string(raw) {
		// Detailed debug for schema cleaning - helps diagnose Gemini 400s
		log.Debug("translator", "cleaned schema", "before", string(raw[:min(500, len(raw))]), "after", string(cleaned[:min(500, len(cleaned))]))
	}
	return cleaned
}

// SanitizeOpenAITools rewrites every tool's parameters schema through
// CleanParametersSchema in an OpenAI-format request body, and normalises every
// function name to the charset upstreams accept — both in the tools list and in
// the history's tool_calls. Used for OpenAI-compat endpoints whose tool
// validation is as strict as Gemini's: one bad keyword, or one illegal name
// anywhere in the payload, rejects the whole request.
//
// History matters as much as the tools list: the API is stateless, so every
// turn re-sends the assistant messages that carry the earlier tool calls. A
// name sanitised on turn 1 but not on turn 2 fails validation just as hard.
//
// Returns the body unchanged when there is nothing to clean.
func SanitizeOpenAITools(body []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	changed := false

	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]any)
			if !ok {
				continue
			}
			if name, ok := fn["name"].(string); ok {
				if fixed := SanitizeToolName(name); fixed != name {
					fn["name"] = fixed
					changed = true
				}
			}
			params, ok := fn["parameters"]
			if !ok {
				continue
			}
			raw, err := json.Marshal(params)
			if err != nil {
				continue
			}
			cleaned := CleanParametersSchema(raw)
			var cleanedOut any
			if err := json.Unmarshal(cleaned, &cleanedOut); err != nil {
				continue
			}
			fn["parameters"] = cleanedOut
			changed = true
		}
	}

	// Replayed history: assistant messages carry the tool calls the model made
	// on earlier turns, and those names are validated exactly like the tools
	// list above.
	if msgs, ok := m["messages"].([]any); ok {
		for _, msg := range msgs {
			mm, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			calls, ok := mm["tool_calls"].([]any)
			if !ok {
				continue
			}
			for _, c := range calls {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				cfn, ok := cm["function"].(map[string]any)
				if !ok {
					continue
				}
				if name, ok := cfn["name"].(string); ok {
					if fixed := SanitizeToolName(name); fixed != name {
						cfn["name"] = fixed
						changed = true
					}
				}
			}
		}
	}

	if !changed {
		return body, nil
	}
	return json.Marshal(m)
}

// toolNameAllowed mirrors the charset strict OpenAI-compatible backends enforce
// on function names: ^[a-zA-Z0-9_.-]+$.
func toolNameAllowed(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// SanitizeToolName replaces every character outside ^[a-zA-Z0-9_.-]+$ with "_".
// The mapping is deterministic and local: the same incoming name always yields
// the same outgoing name, so a tool call the model echoes back can be mapped to
// the caller's original. Only some names are rewritten, so the caller should
// prefer ToolNameAliases.
//
// A name made entirely of illegal characters collapses to "_"; that is still a
// valid name, which is what matters — the alternative is the upstream rejecting
// the whole request.
func SanitizeToolName(name string) string {
	if toolNameAllowed(name) {
		return name
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '_' || r == '.' || r == '-':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// ToolNameAliases maps every tool name in an OpenAI body whose sanitised form
// differs from the original, sanitised -> original. The handler stores it on the
// request so reply-side tool calls can be restored to the names the caller sent
// and will send again next turn.
func ToolNameAliases(body []byte) map[string]string {
	var m struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.Tools) == 0 {
		return nil
	}
	var aliases map[string]string
	for _, t := range m.Tools {
		if fixed := SanitizeToolName(t.Function.Name); fixed != t.Function.Name {
			if aliases == nil {
				aliases = map[string]string{}
			}
			aliases[fixed] = t.Function.Name
		}
	}
	return aliases
}
