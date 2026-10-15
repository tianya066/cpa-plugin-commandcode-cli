package plugin

import (
	"encoding/json"
	"strings"
)

// The CLI route speaks the AI SDK's ModelMessage shape, not OpenAI's. Two
// consequences are load bearing here:
//
//   - an assistant turn that asked for tools carries its calls as tool-call
//     content parts; there is no "tool_calls" field, and a following tool
//     result is rejected unless the call is visible in the assistant content;
//   - a tool turn is a "tool" message whose content is an ARRAY of tool-result
//     parts. A bare string content fails the route's zod schema outright with
//     "Invalid option: expected one of \"user\"|\"assistant\" at
//     params.messages[N].role", which is what a plain OpenAI passthrough sends.

// openAIToolCalls lists the calls an OpenAI assistant turn declared.
func openAIToolCalls(msg map[string]any) []map[string]any {
	list, ok := msg["tool_calls"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if call, ok := item.(map[string]any); ok {
			out = append(out, call)
		}
	}
	return out
}

// cliAssistantMessage renders an assistant turn. Turns without tool calls keep
// their original content shape, so plain conversations travel unchanged.
// Reasoning text (OpenAI reasoning_content / reasoning / reasoning_details)
// becomes a leading {type:"reasoning"} part — the same shape the official CLI
// emits for thinking blocks — so replayed thinking reaches the model instead
// of being silently dropped.
func cliAssistantMessage(msg map[string]any, callNames map[string]string) map[string]any {
	calls := openAIToolCalls(msg)
	reasoning := assistantReasoning(msg)
	if len(calls) == 0 {
		if reasoning == "" {
			return map[string]any{
				"role":    "assistant",
				"content": cliContentBlocks(msg["content"]),
			}
		}
		parts := []any{map[string]any{"type": "reasoning", "text": reasoning}}
		switch content := cliContentBlocks(msg["content"]).(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content})
			}
		case []any:
			parts = append(parts, content...)
		}
		return map[string]any{"role": "assistant", "content": parts}
	}
	parts := cliTextParts(msg["content"])
	if reasoning != "" {
		parts = append([]any{map[string]any{"type": "reasoning", "text": reasoning}}, parts...)
	}
	for _, call := range calls {
		id, _ := call["id"].(string)
		fn, _ := call["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			name = callNames[id]
		}
		parts = append(parts, map[string]any{
			"type":       "tool-call",
			"toolCallId": id,
			"toolName":   name,
			"input":      cliToolArguments(fn["arguments"]),
		})
	}
	return map[string]any{"role": "assistant", "content": parts}
}

// assistantReasoning folds the OpenAI reasoning fields of an assistant turn
// into replay text: reasoning_content (string), then reasoning_details[].text,
// then reasoning (string). The details win over the plain string for the same
// reason as in reasoningText: they are the authoritative per-token text.
func assistantReasoning(msg map[string]any) string {
	if s, ok := msg["reasoning_content"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	if list, ok := msg["reasoning_details"].([]any); ok {
		var b strings.Builder
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		if strings.TrimSpace(b.String()) != "" {
			return b.String()
		}
	}
	if s, ok := msg["reasoning"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return ""
}

// cliToolMessage renders an OpenAI "tool" turn as the route's tool-result shape.
// A result that does not answer the previous assistant turn is rejected by the
// provider ("Messages with role 'tool' must be a response to a preceding
// message with 'tool_calls'"), so an orphan result degrades to user text, which
// the route accepts.
func cliToolMessage(msg map[string]any, callNames map[string]string, openCalls map[string]bool) map[string]any {
	id, _ := msg["tool_call_id"].(string)
	if id == "" {
		id, _ = msg["id"].(string)
	}
	name, _ := msg["name"].(string)
	if name == "" {
		name = callNames[id]
	}
	if id == "" || !openCalls[id] {
		// Orphan results degrade to a user turn; content blocks take the same
		// conversion as user messages so attached images survive as images.
		return map[string]any{"role": "user", "content": cliContentBlocks(msg["content"])}
	}
	return map[string]any{
		"role": "tool",
		"content": []any{map[string]any{
			"type":       "tool-result",
			"toolCallId": id,
			"toolName":   name,
			"output":     cliToolOutput(msg["content"]),
		}},
	}
}

// cliToolOutput renders a tool result for the route. Pure text stays the
// {type:"text",value} form; a result containing images switches to
// {type:"content",value:[...]} with {type:"media",data,mediaType} parts —
// the shape the official CLI produces in toWireToolOutput, which the route
// accepts and the model can actually see.
func cliToolOutput(raw any) map[string]any {
	list, ok := raw.([]any)
	if !ok {
		return map[string]any{"type": "text", "value": cliTextContent(raw)}
	}
	value := make([]any, 0, len(list))
	hasMedia := false
	for _, item := range list {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch kind, _ := block["type"].(string); kind {
		case "text", "input_text", "output_text":
			value = append(value, map[string]any{"type": "text", "text": cliTextContent(block["text"])})
		case "image_url", "input_image", "image":
			if media := cliMediaPart(block); media != nil {
				value = append(value, media)
				hasMedia = true
			} else {
				value = append(value, cliOmittedPart("image"))
			}
		default:
			value = append(value, cliOmittedPart(kind))
		}
	}
	if hasMedia {
		return map[string]any{"type": "content", "value": value}
	}
	texts := make([]string, 0, len(value))
	for _, part := range value {
		if m, ok := part.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				texts = append(texts, s)
			}
		}
	}
	return map[string]any{"type": "text", "value": strings.Join(texts, "\n")}
}

// openToolCallIDs lists the call ids that the assistant turn owning this tool
// result declared. Consecutive "tool" messages are skipped so every result of a
// parallel batch resolves against the single turn that declared the calls; a
// result that finds no such turn must not be emitted with role "tool".
func openToolCallIDs(out []any) map[string]bool {
	ids := map[string]bool{}
	for i := len(out) - 1; i >= 0; i-- {
		prev, ok := out[i].(map[string]any)
		if !ok {
			break
		}
		if prev["role"] == "tool" {
			continue
		}
		if prev["role"] != "assistant" {
			break
		}
		parts, _ := prev["content"].([]any)
		for _, item := range parts {
			part, ok := item.(map[string]any)
			if !ok || part["type"] != "tool-call" {
				continue
			}
			if id, _ := part["toolCallId"].(string); id != "" {
				ids[id] = true
			}
		}
		break
	}
	return ids
}

// cliTextParts flattens content into text blocks for the turns that also carry
// tool calls, where the route wants an array.
func cliTextParts(raw any) []any {
	parts := []any{}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			parts = append(parts, map[string]any{"type": "text", "text": v})
		}
	case []any:
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := block["type"].(string); kind != "text" {
				continue
			}
			if text := cliTextContent(block["text"]); strings.TrimSpace(text) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		}
	}
	return parts
}

// cliToolArguments parses an OpenAI arguments string into the object the route
// expects; absent or unparsable arguments become an empty object.
func cliToolArguments(raw any) any {
	switch v := raw.(type) {
	case string:
		var obj map[string]any
		if strings.TrimSpace(v) != "" && json.Unmarshal([]byte(v), &obj) == nil {
			return obj
		}
	case map[string]any:
		return v
	}
	return map[string]any{}
}
