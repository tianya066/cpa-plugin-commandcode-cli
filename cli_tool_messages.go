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
func cliAssistantMessage(msg map[string]any, callNames map[string]string) map[string]any {
	calls := openAIToolCalls(msg)
	if len(calls) == 0 {
		return map[string]any{
			"role":    "assistant",
			"content": cliContentBlocks(msg["content"]),
		}
	}
	parts := cliTextParts(msg["content"])
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
	text := cliTextContent(msg["content"])
	if id == "" || !openCalls[id] {
		return map[string]any{"role": "user", "content": text}
	}
	return map[string]any{
		"role": "tool",
		"content": []any{map[string]any{
			"type":       "tool-result",
			"toolCallId": id,
			"toolName":   name,
			"output":     map[string]any{"type": "text", "value": text},
		}},
	}
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
