package plugin

// CLI transport: the Go/GOAT/Pro/Max plan's own chat route.
//
// CommandCode exposes two chat surfaces:
//
//	/provider/v1/chat/completions  — the Provider API (Provider plan and above)
//	/alpha/generate                — the route the official CLI itself uses
//
// A Go-plan key is refused on the Provider API with
// `403 Your Go plan doesn't include API access`, but the CLI route serves the
// same catalog. This file implements that transport: it builds the CLI request
// envelope from the OpenAI payload the host hands us, reads the newline/concatenated
// JSON event stream it answers with, and converts every event into a standard
// OpenAI chat-completion chunk so the rest of the plugin (reasoning backfill,
// route-specific framing, host translators) stays unchanged.
//
// The CLI route streams only: `stream:false` is answered with
// `400 Proxy use detected. This endpoint only serves CLI.` Non-streaming client
// requests are therefore served by aggregating this stream into one response.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// cliGeneratePath is the CLI route, relative to the CLI base URL.
const cliGeneratePath = "/alpha/generate"

// defaultCLIBaseURL is where the CLI route lives.
const defaultCLIBaseURL = "https://api.commandcode.ai"

// defaultCLIVersion is the client version the route's compatibility gate wants.
// The route answers `403 upgrade_required` (minVersion 0.18.10) when the header
// is absent, and serves normally once it is present.
const defaultCLIVersion = "1.73.0"

// cliStreamState accumulates what the aggregation path (non-streaming client)
// needs while the same converter also emits per-event chunks.
type cliStreamState struct {
	id        string
	model     string
	created   int64
	text      strings.Builder
	reasoning strings.Builder
	toolCalls []cliToolCall
	usage     *openAIUsage
	finish    string
	roleSent  bool
}

type cliToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// openAIUsage is the usage block emitted on the wire.
type openAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

func newOpenAIUsage() *openAIUsage { return &openAIUsage{} }

// uuidV4 returns a random RFC 4122 v4 UUID, used for the CLI thread id.
func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// cliBaseURL resolves the CLI route root. An explicit cli_base_url wins; a
// provider base URL has its /provider/v1 suffix trimmed so both surfaces are
// driven from one setting when the operator only configures base_url.
func (c *pluginConfig) cliBaseURL() string {
	if c != nil {
		if raw := strings.TrimSpace(c.CLIBaseURL); raw != "" {
			return strings.TrimSuffix(raw, "/")
		}
		if raw := strings.TrimSpace(c.BaseURL); raw != "" {
			trimmed := strings.TrimSuffix(raw, "/")
			trimmed = strings.TrimSuffix(trimmed, "/provider/v1")
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return defaultCLIBaseURL
}

func (c *pluginConfig) cliVersion() string {
	if c != nil {
		if v := strings.TrimSpace(c.CLIVersion); v != "" {
			return v
		}
	}
	return defaultCLIVersion
}

func (c *pluginConfig) cliWorkingDir() string {
	if c != nil {
		if d := strings.TrimSpace(c.CLIWorkingDir); d != "" {
			return d
		}
	}
	return "/tmp"
}

// cliEndpoint is the absolute URL of the CLI route.
func (e *Executor) cliEndpoint() string {
	return e.cfg.cliBaseURL() + cliGeneratePath
}

// cliHeaders builds the CLI request headers, mirroring the header set the
// reference implementation (Mars-Sea/dsh-commandcode-provider, src/adapter.ts
// "cli" protocol branch) sends on this route:
//
//	Content-Type / Authorization / Accept      transport basics
//	accept-encoding: identity                  plain bodies, no undecoded gzip
//	user-agent                                 honest self-identification
//	x-command-code-version                     the route's compatibility gate
//	x-cli-environment: production              CLI environment discriminator
//	x-project-slug                             slug of the working directory
//	x-taste-learning: false                    CLI feature flags
//	x-co-flag: false
//
// The version header is the route's own client contract: without it the service
// refuses the call with `403 upgrade_required` (minVersion 0.18.10).
func (e *Executor) cliHeaders(apiKey string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("Accept", "text/event-stream")
	h.Set("accept-encoding", "identity")
	h.Set("User-Agent", e.cfg.cliUserAgent())
	h.Set("x-command-code-version", e.cfg.cliVersion())
	h.Set("x-cli-environment", "production")
	h.Set("x-project-slug", projectSlug(e.cfg.cliWorkingDir()))
	h.Set("x-taste-learning", "false")
	h.Set("x-co-flag", "false")
	return h
}

// cliUserAgent is the identity this plugin sends upstream. It is a truthful
// self-identification ("product/version (+url)"), not a copy of the official
// CLI's user agent: the CLI route gates on x-command-code-version, while the UA
// tells the service which client is actually calling.
func (c *pluginConfig) cliUserAgent() string {
	if c != nil {
		if ua := strings.TrimSpace(c.CLIUserAgent); ua != "" {
			return ua
		}
	}
	return "cli-proxy-commandcode/" + pluginVersion + " (+https://github.com/tianya066/cpa-plugin-commandcode-cli)"
}

// projectSlug renders a working directory the way the reference implementation
// does (projectSlugFromPath): lower-case, non-alphanumerics collapsed to single
// dashes, drive prefix dropped, leading/trailing dashes trimmed.
func projectSlug(pathName string) string {
	trimmed := strings.TrimSpace(pathName)
	if trimmed == "" {
		return "project"
	}
	lowered := strings.ToLower(trimmed)
	if len(lowered) >= 2 && lowered[1] == ':' {
		lowered = lowered[2:]
	}
	var b strings.Builder
	lastDash := false
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

// buildCLIBody converts the OpenAI chat-completions payload into the CLI
// envelope. The host hands us OpenAI JSON for every client protocol, so this is
// the single place the CLI shape is produced.
func (e *Executor) buildCLIBody(model string, payload []byte) []byte {
	var src map[string]any
	if err := json.Unmarshal(payload, &src); err != nil {
		src = map[string]any{}
	}

	msgs, system := cliMessages(src["messages"])
	params := map[string]any{
		"model":       model,
		"messages":    msgs,
		"tools":       cliTools(src["tools"]),
		"system":      system,
		"max_tokens":  cliMaxTokens(src),
		"temperature": cliFloat(src["temperature"], 0.3),
		"stream":      true,
	}
	if effort := strings.TrimSpace(gjsonGetString(payload, "reasoning_effort")); effort != "" && effort != "none" && effort != "auto" {
		params["reasoning_effort"] = effort
	}

	body := map[string]any{
		"config": map[string]any{
			"workingDir":    e.cfg.cliWorkingDir(),
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   "linux-arm64, Node.js v22.14.0",
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory":         nil,
		"taste":          nil,
		"skills":         nil,
		"permissionMode": "standard",
		"params":         params,
		"threadId":       uuidV4(),
	}
	out, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return out
}

// cliMessages splits the OpenAI message list into CLI messages and the folded
// system section. System content travels as its own cacheable section, which is
// how the route expects stable instructions to arrive.
func cliMessages(raw any) ([]any, any) {
	list, ok := raw.([]any)
	if !ok {
		return []any{}, ""
	}
	out := make([]any, 0, len(list))
	var systemParts []string
	for _, item := range list {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if strings.EqualFold(role, "system") || strings.EqualFold(role, "developer") {
			if text := cliTextContent(msg["content"]); strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": cliContentBlocks(msg["content"]),
		})
	}
	if len(systemParts) == 0 {
		return out, ""
	}
	// One cacheable section carrying the folded instruction text.
	return out, []any{map[string]any{
		"type":          "text",
		"text":          strings.Join(systemParts, "\n\n"),
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
}

// cliContentBlocks keeps the OpenAI content shape (string or block array).
func cliContentBlocks(raw any) any {
	if text, ok := raw.(string); ok {
		return text
	}
	if list, ok := raw.([]any); ok {
		out := make([]any, 0, len(list))
		for _, item := range list {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := block["type"].(string)
			switch kind {
			case "text":
				out = append(out, map[string]any{"type": "text", "text": cliTextContent(block["text"])})
			case "image_url", "image":
				out = append(out, block)
			default:
				out = append(out, block)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return ""
}

func cliTextContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if t, ok := block["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

// cliTools converts OpenAI tool declarations into the CLI's flat form.
func cliTools(raw any) []any {
	list, ok := raw.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		schema := fn["parameters"]
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":         "function",
			"name":         name,
			"description":  desc,
			"input_schema": schema,
		})
	}
	return out
}

func cliMaxTokens(src map[string]any) int64 {
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		switch v := src[key].(type) {
		case float64:
			if v > 0 {
				return int64(v)
			}
		case int64:
			if v > 0 {
				return v
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n > 0 {
				return n
			}
		}
	}
	return 32000
}

func cliFloat(raw any, def float64) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return def
}

// cliEventStream converts the CLI event stream into bare OpenAI chunk payloads.
//
// The CLI answers with concatenated JSON objects (not `data:`-framed SSE), so
// the splitter tracks brace depth and hands one complete object at a time to the
// converter. Every emitted payload is a standard chat.completion.chunk, which is
// what the rest of the pipeline (reasoning backfill, framing, host translators)
// already expects.
func (e *Executor) cliEventStream(ctx context.Context, in <-chan pluginapi.HTTPStreamChunk, model string, framing streamFramingPolicy) (<-chan pluginapi.ExecutorStreamChunk, *cliStreamState) {
	state := &cliStreamState{id: "chatcmpl-" + uuidV4()[:24], model: model, created: time.Now().Unix()}
	out := make(chan pluginapi.ExecutorStreamChunk, 1)
	go func() {
		defer close(out)
		emit := func(payload []byte) bool {
			if len(payload) == 0 {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case out <- pluginapi.ExecutorStreamChunk{Payload: framing.apply(payload)}:
				return true
			}
		}
		emitErr := func(err error) {
			select {
			case out <- pluginapi.ExecutorStreamChunk{Err: err}:
			case <-ctx.Done():
				select {
				case out <- pluginapi.ExecutorStreamChunk{Err: err}:
				default:
				}
			}
		}
		var pending []byte
		handle := func(obj []byte) bool {
			for _, chunk := range state.convert(obj) {
				if !emit(chunk) {
					return false
				}
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				emitErr(ctx.Err())
				return
			case chunk, ok := <-in:
				if !ok {
					if len(pending) > 0 {
						if !handle(pending) {
							return
						}
					}
					if tail := state.closeChunks(); len(tail) > 0 {
						for _, c := range tail {
							if !emit(c) {
								return
							}
						}
					}
					return
				}
				if chunk.Err != nil {
					emitErr(chunk.Err)
					return
				}
				pending = append(pending, chunk.Payload...)
				pending = splitJSONObjects(pending, func(obj []byte) bool {
					if !handle(obj) {
						return false
					}
					return true
				})
				if len(pending) > 8<<20 {
					pending = nil
				}
			}
		}
	}()
	return out, state
}

// splitJSONObjects consumes every complete top-level JSON object in buf and
// returns the trailing partial object (if any). Strings and escapes are tracked
// so braces inside text never split an object early.
func splitJSONObjects(buf []byte, emit func([]byte) bool) []byte {
	depth, start := 0, -1
	inStr, esc := false, false
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					if !emit(buf[start : i+1]) {
						return nil
					}
					start = -1
				}
			}
		}
	}
	if start >= 0 {
		return append([]byte(nil), buf[start:]...)
	}
	return nil
}

// convert maps one CLI event onto zero or more OpenAI chunks and folds it into
// the aggregation state.
func (s *cliStreamState) convert(obj []byte) [][]byte {
	var ev map[string]any
	if err := json.Unmarshal(obj, &ev); err != nil {
		return nil
	}
	kind, _ := ev["type"].(string)
	switch kind {
	case "text-delta":
		text, _ := ev["text"].(string)
		if text == "" {
			return nil
		}
		s.text.WriteString(text)
		return [][]byte{s.chunk(map[string]any{"content": text}, nil)}
	case "reasoning-delta":
		text, _ := ev["text"].(string)
		if text == "" {
			return nil
		}
		s.reasoning.WriteString(text)
		return [][]byte{s.chunk(map[string]any{"reasoning_content": text}, nil)}
	case "tool-call":
		id := stringField(ev, "toolCallId")
		if id == "" {
			id = "call_" + uuidV4()[:12]
		}
		name := stringField(ev, "toolName")
		args := jsonField(ev, "input", "args", "arguments")
		s.toolCalls = append(s.toolCalls, cliToolCall{ID: id, Name: name, Arguments: args})
		idx := len(s.toolCalls) - 1
		delta := map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    idx,
				"id":       id,
				"type":     "function",
				"function": map[string]any{"name": name, "arguments": args},
			}},
		}
		return [][]byte{s.chunk(delta, nil)}
	case "finish":
		if reason := stringField(ev, "finishReason"); reason != "" {
			s.finish = mapFinishReason(reason)
		}
		if usage, ok := ev["totalUsage"].(map[string]any); ok {
			s.usage = convertCLIUsage(usage)
		}
		return nil
	case "error":
		msg := stringField(ev, "message")
		if msg == "" {
			msg = stringField(ev, "error")
		}
		if msg == "" {
			msg = "commandcode cli stream error"
		}
		return [][]byte{[]byte(`{"error":{"message":` + strconv.Quote(msg) + `,"type":"upstream_error"}}`)}
	default:
		// start / start-step / text-start / text-end / reasoning-start /
		// reasoning-end / finish-step / provider-metadata / cache-write-tokens
		// carry no client-visible delta.
		return nil
	}
}

// chunk builds one chat.completion.chunk payload, including the leading role
// delta that OpenAI clients expect.
func (s *cliStreamState) chunk(delta map[string]any, finish *string) []byte {
	if !s.roleSent {
		s.roleSent = true
		if _, ok := delta["role"]; !ok {
			merged := map[string]any{"role": "assistant"}
			for k, v := range delta {
				merged[k] = v
			}
			delta = merged
		}
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != nil {
		choice["finish_reason"] = *finish
	} else {
		choice["finish_reason"] = nil
	}
	payload := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{choice},
	}
	if s.usage != nil {
		payload["usage"] = s.usage
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return raw
}

// closeChunks emits the terminal chunk (finish_reason + usage) so both the
// streaming and aggregation paths see a complete response.
func (s *cliStreamState) closeChunks() [][]byte {
	finish := s.finish
	if finish == "" {
		finish = "stop"
	}
	if s.usage == nil {
		s.usage = newOpenAIUsage()
	}
	choice := map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}
	payload := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{choice},
		"usage":   s.usage,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return [][]byte{raw}
}

// aggregate builds the non-streaming chat.completion response from the state
// collected while draining the CLI stream.
func (s *cliStreamState) aggregate() []byte {
	message := map[string]any{"role": "assistant", "content": s.text.String()}
	if s.reasoning.Len() > 0 {
		message["reasoning_content"] = s.reasoning.String()
	}
	if len(s.toolCalls) > 0 {
		calls := make([]any, 0, len(s.toolCalls))
		for _, call := range s.toolCalls {
			calls = append(calls, map[string]any{
				"id":       call.ID,
				"type":     "function",
				"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
			})
		}
		message["tool_calls"] = calls
	}
	finish := s.finish
	if finish == "" {
		finish = "stop"
	}
	if s.usage == nil {
		s.usage = newOpenAIUsage()
	}
	payload := map[string]any{
		"id":      s.id,
		"object":  "chat.completion",
		"created": s.created,
		"model":   s.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   s.usage,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"error":{"message":"commandcode cli: aggregation failed"}}`)
	}
	return raw
}

// convertCLIUsage maps the CLI's token report onto OpenAI usage fields. The
// CLI reports disjoint input buckets (uncached + cache read + cache write).
func convertCLIUsage(raw map[string]any) *openAIUsage {
	usage := newOpenAIUsage()
	usage.PromptTokens = intField(raw, "inputTokens")
	usage.CompletionTokens = intField(raw, "outputTokens")
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return nil
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if details, ok := raw["inputTokenDetails"].(map[string]any); ok {
		cached := intField(details, "cacheReadTokens")
		if cached > 0 {
			usage.PromptDetails = &struct {
				CachedTokens int64 `json:"cached_tokens"`
			}{CachedTokens: cached}
		}
	}
	if details, ok := raw["outputTokenDetails"].(map[string]any); ok {
		if reasoning := intField(details, "reasoningTokens"); reasoning > 0 {
			usage.CompletionDetails = &struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			}{ReasoningTokens: reasoning}
		}
	}
	return usage
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// jsonField returns the first present field as raw JSON text (objects/arrays are
// re-encoded, scalars are quoted).
func jsonField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch typed := v.(type) {
		case string:
			return typed
		default:
			if raw, err := json.Marshal(typed); err == nil {
				return string(raw)
			}
		}
	}
	return "{}"
}

func intField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return 0
}

// mapFinishReason normalizes the CLI's finish vocabulary to OpenAI's.
func mapFinishReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool-calls", "tool_calls", "tooluse", "tool_use":
		return "tool_calls"
	case "length", "max-tokens", "max_tokens":
		return "length"
	case "content-filter", "content_filter":
		return "content_filter"
	case "":
		return ""
	default:
		return "stop"
	}
}
