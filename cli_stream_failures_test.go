package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Captured verbatim from the CLI route while the model's tool input was
// truncated by the token cap. The route reports one failure as a pair.
const invalidToolCallEvent = `{"type": "tool-call", "toolCallId": "call_00_KNuEX7Xjj4HcNP5rRRmC2124", "toolName": "Bash", "input": "{\"command\"", "dynamic": true, "invalid": true, "error": {"name": "AI_InvalidToolInputError", "cause": {"name": "AI_JSONParseError", "cause": {}, "text": "{\"command\""}, "toolInput": "{\"command\"", "toolName": "Bash"}}`

const toolErrorEvent = `{"type": "tool-error", "toolCallId": "call_00_KNuEX7Xjj4HcNP5rRRmC2124", "toolName": "Bash", "input": "{\"command\"", "error": "Invalid input for tool Bash: JSON parsing failed: Text: {\"command\".\nError message: Expected ':' after property name in JSON at position 10 (line 1 column 11)", "dynamic": true}`

// Captured verbatim: an in-stream failure the route reports after a request it
// refuses to serve.
const upstreamErrorEvent = `{"type": "error", "error": {"type": "server_error", "message": "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"}}`

const validToolCallEvent = `{"type": "tool-call", "toolCallId": "call_00_valid", "toolName": "Bash", "input": {"command": "echo hi"}}`

func newState() *cliStreamState {
	return &cliStreamState{id: "chatcmpl-test", model: "cc-deepseek-v4.1-flash"}
}

func convertOne(t *testing.T, s *cliStreamState, event string) [][]byte {
	t.Helper()
	return s.convert([]byte(event))
}

func chunkText(chunks [][]byte) string {
	parts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, "\n")
}

// A refused call must not reach the client as a callable tool: its arguments are
// the model's raw, unparsable text.
func TestCLIRefusedToolCallIsNotForwarded(t *testing.T) {
	s := newState()
	if chunks := convertOne(t, s, invalidToolCallEvent); len(chunks) != 0 {
		t.Fatalf("invalid tool call must not be emitted, got %s", chunkText(chunks))
	}
	if len(s.toolCalls) != 0 {
		t.Fatalf("invalid tool call must not be collected, got %#v", s.toolCalls)
	}
	if s.streamErr != nil {
		t.Fatalf("nothing is reported before the paired tool-error, got %v", s.streamErr)
	}
}

// The paired tool-error carries the route's own sentence; it is what the client
// sees, once.
func TestCLIToolErrorReportsTheRouteReasonOnce(t *testing.T) {
	s := newState()
	convertOne(t, s, invalidToolCallEvent)
	chunks := convertOne(t, s, toolErrorEvent)
	if len(chunks) != 1 {
		t.Fatalf("want exactly one error chunk, got %d: %s", len(chunks), chunkText(chunks))
	}
	payload := string(chunks[0])
	if !strings.Contains(payload, "upstream_error") {
		t.Fatalf("payload is not an error chunk: %s", payload)
	}
	if !strings.Contains(payload, "Invalid input for tool Bash") ||
		!strings.Contains(payload, "Expected ':' after property name") {
		t.Fatalf("route reason was dropped: %s", payload)
	}
	if !strings.Contains(payload, "tool call Bash was rejected") {
		t.Fatalf("payload does not name the tool: %s", payload)
	}
	if s.streamErr == nil {
		t.Fatal("a refused tool call must be remembered as a stream failure")
	}
	if again := convertOne(t, s, toolErrorEvent); len(again) != 0 {
		t.Fatalf("one failure must be reported once, got %s", chunkText(again))
	}
}

// If the route never sends the paired tool-error, the refusal is still reported
// rather than disappearing.
func TestCLIRefusedToolCallIsFlushedAtStreamEnd(t *testing.T) {
	s := newState()
	convertOne(t, s, invalidToolCallEvent)
	tail := s.closeChunks()
	if len(tail) < 2 {
		t.Fatalf("want an error chunk plus the terminal chunk, got %s", chunkText(tail))
	}
	first := string(tail[0])
	if !strings.Contains(first, "AI_JSONParseError") || !strings.Contains(first, "was rejected") {
		t.Fatalf("flushed report lost the reason: %s", first)
	}
	if !strings.Contains(string(tail[len(tail)-1]), "chat.completion.chunk") {
		t.Fatalf("last chunk must remain the terminal chunk: %s", string(tail[len(tail)-1]))
	}
	if s.streamErr == nil {
		t.Fatal("a flushed refusal must be remembered as a stream failure")
	}
	if extra := s.closeChunks(); len(extra) != 1 {
		t.Fatalf("flush must happen once, second close returned %d chunks", len(extra))
	}
}

// The route nests the reason; the old reader looked for a flat field and fell
// back to a generic sentence, losing it.
func TestCLIUpstreamErrorKeepsTheNestedReason(t *testing.T) {
	s := newState()
	chunks := convertOne(t, s, upstreamErrorEvent)
	if len(chunks) != 1 {
		t.Fatalf("want one error chunk, got %d", len(chunks))
	}
	payload := string(chunks[0])
	if !strings.Contains(payload, "must be a response to a preceding message") {
		t.Fatalf("nested reason was dropped: %s", payload)
	}
	if strings.Contains(payload, "commandcode cli stream error") {
		t.Fatalf("payload fell back to the generic message: %s", payload)
	}
	if s.streamErr == nil {
		t.Fatal("an upstream error event must be remembered as a stream failure")
	}
}

// A valid call must be unaffected: full input, complete arguments, and the
// tool-call finish reason.
func TestCLIValidToolCallIsUnaffected(t *testing.T) {
	s := newState()
	chunks := convertOne(t, s, validToolCallEvent)
	if len(chunks) != 1 {
		t.Fatalf("want one tool-call chunk, got %d: %s", len(chunks), chunkText(chunks))
	}
	if !strings.Contains(string(chunks[0]), `"arguments":"{\"command\":\"echo hi\"}"`) {
		t.Fatalf("tool call arguments changed: %s", string(chunks[0]))
	}
	if len(s.toolCalls) != 1 || s.toolCalls[0].Name != "Bash" {
		t.Fatalf("valid tool call was not collected: %#v", s.toolCalls)
	}
	convertOne(t, s, `{"type":"finish","finishReason":"tool-calls"}`)
	tail := s.closeChunks()
	if !strings.Contains(string(tail[len(tail)-1]), `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish reason = %s", string(tail[len(tail)-1]))
	}
	if s.streamErr != nil {
		t.Fatalf("a valid call must not fail the request: %v", s.streamErr)
	}
}

// The aggregation path (non-streaming client) must not answer 200 with an empty
// message when the route reported a failure mid-stream.
func TestCLIExecuteFailsInsteadOfReturningAnEmptyCompletion(t *testing.T) {
	cases := []struct {
		name    string
		events  []string
		wantMsg string
	}{
		{
			name:    "upstream error event",
			events:  []string{upstreamErrorEvent, `{"type":"finish","finishReason":"stop"}`},
			wantMsg: "must be a response to a preceding message",
		},
		{
			name:    "refused tool call",
			events:  []string{invalidToolCallEvent, toolErrorEvent, `{"type":"finish","finishReason":"length"}`},
			wantMsg: "tool call Bash was rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payloads := make([][]byte, 0, len(tc.events))
			for _, event := range tc.events {
				payloads = append(payloads, []byte(event+"\n"))
			}
			executor := NewExecutor(parseConfig([]byte("transport: cli\napi_key: test-key\n")), nil)
			response, err := executor.Execute(t.Context(), pluginapi.ExecutorRequest{
				Model:      "cc-deepseek-v4.1-flash",
				Payload:    []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
				HTTPClient: staticStreamHTTPClient{payloads: payloads},
			})
			if err == nil {
				t.Fatalf("expected an error, got payload %s", response.Payload)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error lost the reason: %v", err)
			}
			if len(response.Payload) != 0 {
				t.Fatalf("a failed stream must not produce a payload: %s", response.Payload)
			}
		})
	}
}

// The aggregation path still builds a tool call when the route produced a valid
// one, and reports the model the client asked for.
func TestCLIExecuteAggregatesAValidToolCall(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte("transport: cli\napi_key: test-key\n")), nil)
	response, err := executor.Execute(t.Context(), pluginapi.ExecutorRequest{
		Model:   "cc-deepseek-v4.1-flash",
		Payload: []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
		HTTPClient: staticStreamHTTPClient{payloads: [][]byte{
			[]byte(validToolCallEvent + "\n"),
			[]byte(`{"type":"finish","finishReason":"tool-calls"}` + "\n"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Payload, &body); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if body["model"] != "cc-deepseek-v4.1-flash" {
		t.Fatalf("model = %v", body["model"])
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", message["tool_calls"])
	}
	call := calls[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "Bash" || call["arguments"] != `{"command":"echo hi"}` {
		t.Fatalf("tool call = %#v", call)
	}
}

// The streaming client learns about a refused call from the stream itself.
func TestCLIStreamSurfacesARefusedToolCall(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte("transport: cli\napi_key: test-key\n")), nil)
	response, err := executor.ExecuteStream(t.Context(), pluginapi.ExecutorRequest{
		Model:   "cc-deepseek-v4.1-flash",
		Payload: []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
		HTTPClient: staticStreamHTTPClient{payloads: [][]byte{
			[]byte(invalidToolCallEvent + "\n"),
			[]byte(toolErrorEvent + "\n"),
			[]byte(`{"type":"finish","finishReason":"length"}` + "\n"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payloads [][]byte
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		payloads = append(payloads, chunk.Payload)
	}
	joined := string(bytes.Join(payloads, nil))
	if !strings.Contains(joined, `"upstream_error"`) || !strings.Contains(joined, "tool call Bash was rejected") {
		t.Fatalf("stream did not surface the refusal: %s", joined)
	}
	if strings.Contains(joined, `"tool_calls"`) {
		t.Fatalf("refused call leaked to the client as callable: %s", joined)
	}
}
