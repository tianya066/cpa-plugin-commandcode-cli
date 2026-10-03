package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// The CLI route answers with concatenated JSON objects; the splitter must not
// cut inside strings that contain braces (code blocks, JSON in text).
func TestSplitJSONObjectsHandlesBracesInStrings(t *testing.T) {
	stream := `{"type":"text-delta","text":"a {b} c"}{"type":"text-delta","text":"{\"x\":1}"}`
	var got []string
	rest := splitJSONObjects([]byte(stream), func(obj []byte) bool {
		got = append(got, string(obj))
		return true
	})
	if len(rest) != 0 {
		t.Fatalf("trailing bytes = %q, want none", rest)
	}
	if len(got) != 2 {
		t.Fatalf("objects = %d, want 2 (%v)", len(got), got)
	}
	if !strings.Contains(got[0], "a {b} c") {
		t.Fatalf("first object lost text: %s", got[0])
	}
}

func TestSplitJSONObjectsKeepsPartial(t *testing.T) {
	rest := splitJSONObjects([]byte(`{"type":"text-delta","text":"hi"}{"type":"fin`), func([]byte) bool { return true })
	if string(rest) != `{"type":"fin` {
		t.Fatalf("partial = %q", rest)
	}
}

// End-to-end conversion of a realistic CLI event sequence into OpenAI chunks,
// including the aggregation path used for non-streaming clients.
func TestCLIEventConversionAndAggregation(t *testing.T) {
	events := []string{
		`{"type":"start"}`,
		`{"type":"start-step"}`,
		`{"type":"reasoning-start"}`,
		`{"type":"reasoning-delta","text":"think 1 "}`,
		`{"type":"reasoning-delta","text":"think 2"}`,
		`{"type":"reasoning-end"}`,
		`{"type":"text-delta","text":"hello"}`,
		`{"type":"text-delta","text":" world"}`,
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":100,"outputTokens":7,"inputTokenDetails":{"cacheReadTokens":40},"outputTokenDetails":{"reasoningTokens":3}}}`,
	}
	state := &cliStreamState{id: "chatcmpl-test", model: "deepseek/deepseek-v4.1-flash", created: 1}
	var chunks [][]byte
	for _, raw := range events {
		chunks = append(chunks, state.convert([]byte(raw))...)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks emitted")
	}
	for i, c := range chunks {
		var obj map[string]any
		if err := json.Unmarshal(c, &obj); err != nil {
			t.Fatalf("chunk %d not JSON: %v (%s)", i, err, c)
		}
		if obj["object"] != "chat.completion.chunk" {
			t.Fatalf("chunk %d object = %v", i, obj["object"])
		}
	}
	joined := string(joinBytes(chunks))
	if !strings.Contains(joined, `"reasoning_content":"think 1 "`) {
		t.Fatalf("reasoning_content missing in chunks: %s", joined[:minInt(400, len(joined))])
	}
	if !strings.Contains(joined, `"content":"hello"`) {
		t.Fatalf("text delta missing")
	}
	agg := state.aggregate()
	var out map[string]any
	if err := json.Unmarshal(agg, &out); err != nil {
		t.Fatalf("aggregate not JSON: %v", err)
	}
	if out["object"] != "chat.completion" {
		t.Fatalf("aggregate object = %v", out["object"])
	}
	choice := out["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != "hello world" {
		t.Fatalf("aggregate content = %v", msg["content"])
	}
	if msg["reasoning_content"] != "think 1 think 2" {
		t.Fatalf("aggregate reasoning = %v", msg["reasoning_content"])
	}
	usage := out["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 100 || usage["completion_tokens"].(float64) != 7 {
		t.Fatalf("usage = %v", usage)
	}
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
}

// The CLI envelope must carry the documented shape: folded system section,
// upstream model name, streaming forced on.
func TestBuildCLIBodyShape(t *testing.T) {
	cfg := parseConfig([]byte("transport: cli\nmodels:\n  - alias: deepseek-flash\n    name: deepseek/deepseek-v4.1-flash\n"))
	e := NewExecutor(cfg, nil)
	payload := []byte(`{"model":"deepseek-flash","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"max_tokens":64,"temperature":0.5}`)
	body := e.buildCLIBody("deepseek/deepseek-v4.1-flash", payload)
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	params := env["params"].(map[string]any)
	if params["model"] != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("model = %v", params["model"])
	}
	if params["stream"] != true {
		t.Fatalf("stream must be forced true, got %v", params["stream"])
	}
	if params["max_tokens"].(float64) != 64 {
		t.Fatalf("max_tokens = %v", params["max_tokens"])
	}
	if _, ok := env["threadId"].(string); !ok {
		t.Fatal("threadId missing")
	}
	if env["permissionMode"] != "standard" {
		t.Fatalf("permissionMode = %v", env["permissionMode"])
	}
	sys, ok := params["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system must be a one-block array, got %#v", params["system"])
	}
	block := sys[0].(map[string]any)
	if block["text"] != "be brief" || block["type"] != "text" {
		t.Fatalf("system block = %#v", block)
	}
	msgs := params["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("system message must be folded out of messages, got %d", len(msgs))
	}
}

// The CLI header set must match the reference implementation's "cli" branch:
// transport basics plus the route's discriminator headers.
func TestCLIHeadersMatchReferenceSet(t *testing.T) {
	cfg := parseConfig([]byte("transport: cli\ncli_version: \"1.73.0\"\ncli_working_dir: /tmp\napi_keys:\n  - key: user_test\n"))
	e := NewExecutor(cfg, nil)
	h := e.cliHeaders("user_test")
	want := map[string]string{
		"Content-Type":           "application/json",
		"Authorization":          "Bearer user_test",
		"Accept":                 "text/event-stream",
		"accept-encoding":        "identity",
		"x-command-code-version": "1.73.0",
		"x-cli-environment":      "production",
		"x-project-slug":         "tmp",
		"x-taste-learning":       "false",
		"x-co-flag":              "false",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Fatalf("header %s = %q, want %q", k, got, v)
		}
	}
	ua := h.Get("User-Agent")
	if !strings.Contains(ua, "cli-proxy-commandcode/") {
		t.Fatalf("User-Agent must identify the calling client, got %q", ua)
	}
	// The UA reaches a third-party service on every request; it must not carry
	// the operator's repository or any other provenance URL.
	if strings.Contains(ua, "http") || strings.Contains(ua, "github.com") || strings.Contains(ua, "(") {
		t.Fatalf("User-Agent must not leak a repository URL, got %q", ua)
	}
}

// projectSlug mirrors the reference implementation's projectSlugFromPath.
func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/tmp":                      "tmp",
		"C:\\Users\\Me\\My Project": "users-me-my-project",
		"/home/me/work_area/v2":     "home-me-work-area-v2",
		"":                          "project",
		"///":                       "project",
		"/a--b___c":                 "a-b-c",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Fatalf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func joinBytes(bs [][]byte) []byte {
	var out []byte
	for _, b := range bs {
		out = append(out, b...)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
