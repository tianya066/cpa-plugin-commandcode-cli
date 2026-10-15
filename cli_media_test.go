package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// Multimodal/tool-choice coverage for the CLI envelope. The route's part union
// accepts only AI-SDK/Anthropic shapes; every inbound OpenAI part must be
// converted (verified against the live route) or visibly marked.

func buildCLIBodyParams(t *testing.T, payload string) map[string]any {
	t.Helper()
	cfg := parseConfig([]byte("transport: cli\nmodels:\n  - alias: m\n    name: v/m\n"))
	e := NewExecutor(cfg, nil)
	body := e.buildCLIBody("v/m", []byte(payload))
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	params, ok := env["params"].(map[string]any)
	if !ok {
		t.Fatal("params missing")
	}
	return params
}

func lastUserMessage(t *testing.T, params map[string]any) map[string]any {
	t.Helper()
	msgs, ok := params["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatal("messages missing")
	}
	return msgs[len(msgs)-1].(map[string]any)
}

func TestUserImageDataURLConvertsToWireImage(t *testing.T) {
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"what color?"},
		{"type":"image_url","image_url":{"url":"`+dataURL+`"}}]}]}`)
	content, ok := lastUserMessage(t, params)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	img := content[1].(map[string]any)
	if img["type"] != "image" || img["image"] != dataURL || img["mimeType"] != "image/png" {
		t.Fatalf("image part = %#v", img)
	}
}

func TestUserImageRemoteURLConvertsToWireImage(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}`)
	content := lastUserMessage(t, params)["content"].([]any)
	img := content[0].(map[string]any)
	if img["type"] != "image" || img["image"] != "https://example.com/x.png" {
		t.Fatalf("image part = %#v", img)
	}
}

func TestAnthropicImageSourceConverts(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"QUJD"}}]}]}`)
	img := lastUserMessage(t, params)["content"].([]any)[0].(map[string]any)
	if img["type"] != "image" || img["image"] != "data:image/jpeg;base64,QUJD" || img["mimeType"] != "image/jpeg" {
		t.Fatalf("image part = %#v", img)
	}
}

func TestInputImageBareStringConverts(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"input_image","image_url":"data:image/webp;base64,UklGRg=="}]}]}`)
	img := lastUserMessage(t, params)["content"].([]any)[0].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != "image/webp" {
		t.Fatalf("image part = %#v", img)
	}
}

func TestOpenAIFilePartBecomesMarker(t *testing.T) {
	// The route's document{source} union variant passes zod but is rejected by
	// the downstream ModelMessage conversion, so files must degrade to text.
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"file","file":{"filename":"x.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]}`)
	part := lastUserMessage(t, params)["content"].([]any)[0].(map[string]any)
	if part["type"] != "text" {
		t.Fatalf("part = %#v", part)
	}
	text := part["text"].(string)
	for _, want := range []string{"file", "x.pdf", "application/pdf"} {
		if !strings.Contains(text, want) {
			t.Fatalf("marker %q missing %q", text, want)
		}
	}
}

func TestAudioAndUnknownPartsBecomeMarkers(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"input_audio","input_audio":{"data":"AA==","format":"wav"}},
		{"type":"mystery_part","mystery_part":{}}]}]}`)
	content := lastUserMessage(t, params)["content"].([]any)
	for i, want := range []string{"audio", "mystery_part"} {
		part := content[i].(map[string]any)
		if part["type"] != "text" || !strings.Contains(part["text"].(string), want) {
			t.Fatalf("part %d = %#v, want text marker mentioning %q", i, part, want)
		}
	}
}

func TestToolResultCarriesImageAsMedia(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[
		{"role":"user","content":"look"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_image","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":[
			{"type":"text","text":"see below"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},
		{"role":"user","content":"what color?"}]}`)
	msgs := params["messages"].([]any)
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Fatalf("tool message role = %v", toolMsg["role"])
	}
	result := toolMsg["content"].([]any)[0].(map[string]any)
	out := result["output"].(map[string]any)
	if out["type"] != "content" {
		t.Fatalf("output = %#v", out)
	}
	value := out["value"].([]any)
	media := value[1].(map[string]any)
	if media["type"] != "media" || media["data"] != "QUJD" || media["mediaType"] != "image/png" {
		t.Fatalf("media part = %#v", media)
	}
}

func TestToolResultTextOnlyStaysText(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"sunny"}]}`)
	msgs := params["messages"].([]any)
	out := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)["output"].(map[string]any)
	if out["type"] != "text" || out["value"] != "sunny" {
		t.Fatalf("output = %#v", out)
	}
}

func TestAssistantReasoningContentBecomesReasoningPart(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[
		{"role":"user","content":"pick"},
		{"role":"assistant","content":"seven","reasoning_content":"thinking about it"},
		{"role":"user","content":"add one"}]}`)
	assistant := params["messages"].([]any)[1].(map[string]any)
	parts := assistant["content"].([]any)
	if parts[0].(map[string]any)["type"] != "reasoning" ||
		parts[0].(map[string]any)["text"] != "thinking about it" {
		t.Fatalf("assistant parts = %#v", parts)
	}
	if parts[1].(map[string]any)["text"] != "seven" {
		t.Fatalf("assistant text = %#v", parts[1])
	}
}

func TestAssistantReasoningPrecedesToolCalls(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","reasoning_content":"let me check","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"sunny"}]}`)
	parts := params["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if parts[0].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("first part = %#v", parts[0])
	}
	last := parts[len(parts)-1].(map[string]any)
	if last["type"] != "tool-call" || last["toolCallId"] != "c1" {
		t.Fatalf("tool-call part = %#v", last)
	}
}

func TestToolChoiceConversion(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		wantType    string
		wantName    string
		wantNoTools bool
	}{
		{"required", `"required"`, "any", "", false},
		{"auto string", `"auto"`, "", "", false},
		{"function", `{"type":"function","function":{"name":"get_weather"}}`, "tool", "get_weather", false},
		{"anthropic tool", `{"type":"tool","name":"get_weather"}`, "tool", "get_weather", false},
		{"none", `"none"`, "", "", true},
		{"anthropic none", `{"type":"none"}`, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"get_weather","description":"x","parameters":{"type":"object"}}}],
				"tool_choice":`+tc.payload+`}`)
			if tc.wantNoTools {
				if tools := params["tools"].([]any); len(tools) != 0 {
					t.Fatalf("tools must be emptied for none, got %#v", tools)
				}
				if _, ok := params["tool_choice"]; ok {
					t.Fatalf("tool_choice must be omitted for none, got %#v", params["tool_choice"])
				}
				return
			}
			got, ok := params["tool_choice"].(map[string]any)
			if tc.wantType == "" {
				if ok {
					t.Fatalf("tool_choice must be omitted, got %#v", got)
				}
				return
			}
			if !ok || got["type"] != tc.wantType {
				t.Fatalf("tool_choice = %#v, want type %q", params["tool_choice"], tc.wantType)
			}
			if tc.wantName != "" && got["name"] != tc.wantName {
				t.Fatalf("tool_choice name = %#v", got)
			}
		})
	}
}

func TestParallelToolCallsFalseEmitsDisableFlag(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"f","description":"x","parameters":{"type":"object"}}}],
		"tool_choice":"required","parallel_tool_calls":false}`)
	tc := params["tool_choice"].(map[string]any)
	if tc["type"] != "any" || tc["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %#v", tc)
	}
}

func TestTemperatureOmittedUnlessProvided(t *testing.T) {
	params := buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if _, ok := params["temperature"]; ok {
		t.Fatalf("temperature must be omitted when unset, got %v", params["temperature"])
	}
	params = buildCLIBodyParams(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)
	if params["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", params["temperature"])
	}
}

func TestUsageCarriesCacheWriteTokens(t *testing.T) {
	usage := convertCLIUsage(map[string]any{
		"inputTokens": 1000, "outputTokens": 5,
		"inputTokenDetails": map[string]any{"cacheReadTokens": 600, "cacheWriteTokens": 300},
	})
	if usage == nil || usage.PromptDetails == nil {
		t.Fatal("prompt details missing")
	}
	if usage.PromptDetails.CacheWriteTokens != 300 || usage.PromptDetails.CachedTokens != 600 {
		t.Fatalf("details = %#v", usage.PromptDetails)
	}
	raw, _ := json.Marshal(usage)
	if !strings.Contains(string(raw), `"cache_write_tokens":300`) {
		t.Fatalf("usage JSON = %s", raw)
	}
}

func TestUsageReadsTopLevelCacheBucketsWithoutDetails(t *testing.T) {
	usage := convertCLIUsage(map[string]any{
		"inputTokens": float64(1000), "outputTokens": float64(5),
		"cacheReadTokens": float64(400), "cacheWriteTokens": float64(250),
	})
	if usage == nil || usage.PromptDetails == nil {
		t.Fatal("prompt details missing without inputTokenDetails")
	}
	if usage.PromptDetails.CacheWriteTokens != 250 || usage.PromptDetails.CachedTokens != 400 {
		t.Fatalf("details = %#v", usage.PromptDetails)
	}
}

func TestAssistantReasoningPrefersDetailsOverPlainString(t *testing.T) {
	msg := map[string]any{
		"reasoning":         "stale",
		"reasoning_details": []any{map[string]any{"text": "author"}, map[string]any{"text": "itative"}},
	}
	if got := assistantReasoning(msg); got != "authoritative" {
		t.Fatalf("assistantReasoning = %q, want details text", got)
	}
	msg["reasoning_content"] = "canonical"
	if got := assistantReasoning(msg); got != "canonical" {
		t.Fatalf("assistantReasoning = %q, want reasoning_content first", got)
	}
	delete(msg, "reasoning_content")
	msg["reasoning_details"] = []any{}
	if got := assistantReasoning(msg); got != "stale" {
		t.Fatalf("assistantReasoning = %q, want plain-string fallback", got)
	}
}
