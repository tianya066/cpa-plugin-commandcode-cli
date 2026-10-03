package plugin

import (
	"encoding/json"
	"testing"
)

// cliBodyMessages builds the CLI envelope for a raw OpenAI payload and returns
// the params.messages list.
func cliBodyMessages(t *testing.T, payload string) []any {
	t.Helper()
	cfg := parseConfig([]byte("transport: cli\nmodels:\n  - alias: deepseek-flash\n    name: deepseek/deepseek-v4.1-flash\n"))
	e := NewExecutor(cfg, nil)
	body := e.buildCLIBody("deepseek/deepseek-v4.1-flash", []byte(payload))
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	msgs, ok := env["params"].(map[string]any)["messages"].([]any)
	if !ok {
		t.Fatalf("params.messages missing: %s", body)
	}
	return msgs
}

func msgAt(t *testing.T, msgs []any, i int) map[string]any {
	t.Helper()
	if i >= len(msgs) {
		t.Fatalf("message %d missing, got %d", i, len(msgs))
	}
	msg, ok := msgs[i].(map[string]any)
	if !ok {
		t.Fatalf("message %d is %T", i, msgs[i])
	}
	return msg
}

// A tool result is a "tool" message whose content is an ARRAY of tool-result
// parts. Sending the OpenAI shape (role "tool" with a string content) is what
// production rejected with
// "Invalid option: expected one of \"user\"|\"assistant\" at params.messages[4].role".
func TestCLIToolResultUsesArrayParts(t *testing.T) {
	msgs := cliBodyMessages(t, `{"model":"deepseek-flash","messages":[
		{"role":"user","content":"run echo"},
		{"role":"assistant","content":"checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"echo hi\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"{\"stdout\":\"hi\"}"}
	]}`)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}

	assistant := msgAt(t, msgs, 1)
	if _, ok := assistant["tool_calls"]; ok {
		t.Error("assistant message must not carry an OpenAI tool_calls field")
	}
	parts, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("assistant content must be an array when it declares calls, got %T", assistant["content"])
	}
	var call map[string]any
	for _, item := range parts {
		if part, ok := item.(map[string]any); ok && part["type"] == "tool-call" {
			call = part
		}
	}
	if call == nil {
		t.Fatalf("no tool-call part in %#v", parts)
	}
	if call["toolCallId"] != "call_1" || call["toolName"] != "Bash" {
		t.Errorf("tool-call part = %#v", call)
	}
	input, ok := call["input"].(map[string]any)
	if !ok || input["command"] != "echo hi" {
		t.Errorf("tool-call input must be the parsed arguments object, got %#v", call["input"])
	}

	tool := msgAt(t, msgs, 2)
	if tool["role"] != "tool" {
		t.Fatalf("tool turn role = %v", tool["role"])
	}
	results, ok := tool["content"].([]any)
	if !ok {
		t.Fatalf("tool content must be an array, got %T (%#v)", tool["content"], tool["content"])
	}
	if len(results) != 1 {
		t.Fatalf("want one tool-result part, got %d", len(results))
	}
	part := results[0].(map[string]any)
	if part["type"] != "tool-result" || part["toolCallId"] != "call_1" || part["toolName"] != "Bash" {
		t.Errorf("tool-result part = %#v", part)
	}
	output, ok := part["output"].(map[string]any)
	if !ok || output["type"] != "text" || output["value"] != `{"stdout":"hi"}` {
		t.Errorf("tool-result output = %#v", part["output"])
	}
}

// Several calls in one assistant turn, each answered by its own tool message:
// what an agent client sends for a parallel tool batch.
func TestCLIParallelToolCallsKeepOneResultEach(t *testing.T) {
	msgs := cliBodyMessages(t, `{"model":"deepseek-flash","messages":[
		{"role":"assistant","tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"a\"}"}},
			{"id":"call_b","type":"function","function":{"name":"Read","arguments":"{\"path\":\"b\"}"}}
		]},
		{"role":"tool","tool_call_id":"call_a","content":"out-a"},
		{"role":"tool","tool_call_id":"call_b","content":"out-b"}
	]}`)
	parts := msgAt(t, msgs, 0)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("want 2 tool-call parts, got %d", len(parts))
	}
	for i, want := range []string{"call_a", "call_b"} {
		part := parts[i].(map[string]any)
		if part["type"] != "tool-call" || part["toolCallId"] != want {
			t.Errorf("part %d = %#v", i, part)
		}
	}
	for i, want := range []struct{ id, name string }{{"call_a", "Bash"}, {"call_b", "Read"}} {
		msg := msgAt(t, msgs, i+1)
		if msg["role"] != "tool" {
			t.Fatalf("message %d role = %v", i+1, msg["role"])
		}
		result := msg["content"].([]any)[0].(map[string]any)
		if result["toolCallId"] != want.id || result["toolName"] != want.name {
			t.Errorf("result %d = %#v", i, result)
		}
	}
}

// A tool result whose predecessor is not the assistant turn that declared the
// call is rejected upstream, so it must degrade to plain user text instead of
// keeping role "tool".
func TestCLIOrphanToolResultDegradesToUserText(t *testing.T) {
	msgs := cliBodyMessages(t, `{"model":"deepseek-flash","messages":[
		{"role":"user","content":"run echo"},
		{"role":"tool","tool_call_id":"call_missing","content":"orphan output"}
	]}`)
	orphan := msgAt(t, msgs, 1)
	if orphan["role"] != "user" {
		t.Fatalf("orphan tool result must degrade to user, got %v", orphan["role"])
	}
	if orphan["content"] != "orphan output" {
		t.Errorf("orphan content = %#v", orphan["content"])
	}
}

// No emitted message may use role "tool" with a non-array content: that exact
// combination is the production 400.
func TestCLINoToolMessageCarriesStringContent(t *testing.T) {
	msgs := cliBodyMessages(t, `{"model":"deepseek-flash","messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"ok","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"plain string output"},
		{"role":"user","content":[{"type":"text","text":"next"}]}
	]}`)
	for i, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("message %d is %T", i, item)
		}
		if msg["role"] != "tool" {
			continue
		}
		if _, ok := msg["content"].([]any); !ok {
			t.Fatalf("message %d: role \"tool\" content must be an array, got %T %#v",
				i, msg["content"], msg["content"])
		}
	}
}

// Plain conversations must travel unchanged: text-only turns keep the OpenAI
// content shape, so the fix cannot alter non-tool traffic.
func TestCLITextOnlyTurnsKeepContentShape(t *testing.T) {
	msgs := cliBodyMessages(t, `{"model":"deepseek-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":[{"type":"text","text":"again"}]}
	]}`)
	if got := msgAt(t, msgs, 0)["content"]; got != "hi" {
		t.Errorf("user string content changed: %#v", got)
	}
	if got := msgAt(t, msgs, 1)["content"]; got != "hello" {
		t.Errorf("assistant string content changed: %#v", got)
	}
	blocks, ok := msgAt(t, msgs, 2)["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("user block content changed: %#v", msgAt(t, msgs, 2)["content"])
	}
	if block := blocks[0].(map[string]any); block["text"] != "again" {
		t.Errorf("user block = %#v", block)
	}
}
