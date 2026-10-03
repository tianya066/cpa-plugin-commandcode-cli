package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

// Executor forwards OpenAI chat-completions payloads to commandcode and
// normalizes responses back into standard OpenAI shape (reasoning backfill).
//
// v0.2.0: requests go through the weighted multi-key pool. Members without
// proxy_url use the host HTTP client (host proxy policy + request-log
// preserved); members with proxy_url use a self-built transport (host
// request-log cannot capture those). Failover retries 429/5xx/transport
// errors on the next pool member.
type Executor struct {
	cfg        *pluginConfig
	translator *Translator
	keypool    *pool
}

func NewExecutor(cfg *pluginConfig, t *Translator) *Executor {
	if t == nil {
		t = NewTranslator(cfg)
	}
	return &Executor{cfg: cfg, translator: t, keypool: newPool()}
}

func (e *Executor) Identifier() string { return Provider }

// apiKey keeps the v0.1.x single-key resolution for translator paths and
// error messages. Live execution uses the pool (members method).
func apiKey(cfg *pluginConfig, req pluginapi.ExecutorRequest) string {
	if ms := cfg.members(req); len(ms) > 0 {
		return strings.TrimSpace(ms[0].Key)
	}
	return ""
}

const missingKeyMsg = "commandcode executor: missing api key (router path passes nil auth; set plugins.configs.commandcode.api_key or api_keys in config.yaml)"

const claudeMessagesPath = "/v1/messages"

type streamFramingPolicy uint8

const (
	streamFramingBare streamFramingPolicy = iota
	streamFramingClaude
)

func streamFramingForRequest(req pluginapi.ExecutorRequest) streamFramingPolicy {
	path, ok := req.Metadata[coreexecutor.RequestPathMetadataKey].(string)
	if ok && path == claudeMessagesPath {
		return streamFramingClaude
	}
	return streamFramingBare
}

func (p streamFramingPolicy) apply(payload []byte) []byte {
	if p != streamFramingClaude {
		return payload
	}
	framed := make([]byte, 0, len("data: ")+len(payload))
	framed = append(framed, "data: "...)
	framed = append(framed, payload...)
	return framed
}

// providerEndpoint is the Provider API chat route.
func (e *Executor) providerEndpoint() string {
	return strings.TrimSuffix(e.cfg.baseURL(), "/") + "/chat/completions"
}

// endpoint keeps the historical name used by the provider path.
func (e *Executor) endpoint() string { return e.providerEndpoint() }

// useCLITransport reports whether this request must go to the CLI route.
func (e *Executor) useCLITransport() bool {
	switch strings.ToLower(strings.TrimSpace(e.cfg.Transport)) {
	case "cli":
		return true
	default:
		return false
	}
}

// planRefusal reports whether an upstream error means "this key's plan has no
// Provider API access" — the one case worth retrying on the CLI route when
// transport is "auto".
func planRefusal(status int, body []byte) bool {
	if status != http.StatusForbidden && status != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "doesn't include api access") ||
		strings.Contains(text, "does not include api access") ||
		strings.Contains(text, "upgrade to provider") ||
		strings.Contains(text, "insufficient_quota")
}

// buildUpstreamBody runs the request translator edge openai->commandcode so
// model normalization stays in one place, then forces stream flags.
func (e *Executor) buildUpstreamBody(model string, payload []byte, stream bool) []byte {
	out, err := e.translator.TranslateRequest(context.Background(), pluginapi.RequestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "commandcode",
		Model:      model,
		Stream:     stream,
		Body:       payload,
	})
	body := payload
	if err == nil && len(out.Body) > 0 {
		body = out.Body
	}
	return setStreamFlag(body, stream)
}

func setStreamFlag(body []byte, stream bool) []byte {
	if len(body) == 0 {
		return body
	}
	updated, err := sjson.SetBytes(body, "stream", stream)
	if err != nil {
		return body
	}
	body = updated
	if stream {
		if updated, err := sjson.SetBytes(body, "stream_options.include_usage", true); err == nil {
			body = updated
		}
	}
	return body
}

func upstreamHeaders(apiKey string, stream bool) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("User-Agent", "cli-proxy-commandcode")
	if stream {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	return h
}

// Execute performs a non-streaming chat completion, failing over across
// pool members on retryable errors (transport error, 429, 5xx).
//
// The CLI route streams only, so a CLI-transport request is served by draining
// that stream and aggregating it into one chat.completion.
func (e *Executor) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	members := e.cfg.members(req)
	if len(members) == 0 {
		return pluginapi.ExecutorResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMsg}
	}
	if e.useCLITransport() {
		return e.executeCLI(ctx, req, members)
	}
	body := e.buildUpstreamBody(req.Model, req.Payload, false)
	var lastErr error
	for _, idx := range e.keypool.order(members) {
		m := members[idx]
		d, err := e.keypool.clientFor(idx, m, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		status, headers, respBody, err := d.do(ctx, e.endpoint(), upstreamHeaders(strings.TrimSpace(m.Key), false), body)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorResponse{}, err
		}
		if status < 200 || status >= 300 {
			lastErr = statusError{statusCode: status, body: respBody}
			if retryableBody(status, respBody) && ctx.Err() == nil {
				continue
			}
			// transport=auto: a plan that has no Provider API access is served
			// by the CLI route instead.
			if e.autoCLIFallback() && planRefusal(status, respBody) {
				return e.executeCLI(ctx, req, members)
			}
			return pluginapi.ExecutorResponse{}, lastErr
		}
		fixed, _ := mapReasoningBody(respBody)
		return pluginapi.ExecutorResponse{Payload: fixed, Headers: headers}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorResponse{}, lastErr
	}
	return pluginapi.ExecutorResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

// autoCLIFallback reports whether a provider-API plan refusal should be retried
// on the CLI route.
func (e *Executor) autoCLIFallback() bool {
	return strings.EqualFold(strings.TrimSpace(e.cfg.Transport), "auto")
}

// executeCLI runs one non-streaming request through the CLI route by draining
// the event stream and aggregating it.
func (e *Executor) executeCLI(ctx context.Context, req pluginapi.ExecutorRequest, members []APIKeyEntry) (pluginapi.ExecutorResponse, error) {
	// The vendor name goes upstream; the response echoes what the client asked
	// for, so a caller never sees a model it did not request.
	model := e.upstreamModelFor(req)
	echoModel := e.responseModelFor(req)
	body := e.buildCLIBody(model, req.Payload)
	var lastErr error
	for _, idx := range e.keypool.order(members) {
		m := members[idx]
		d, err := e.keypool.clientFor(idx, m, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		status, headers, chunks, err := d.doStream(ctx, e.cliEndpoint(), e.cliHeaders(strings.TrimSpace(m.Key)), body)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorResponse{}, err
		}
		if status < 200 || status >= 300 {
			errBody := readStreamErrorBody(ctx, chunks)
			lastErr = statusError{statusCode: status, body: errBody}
			if retryableBody(status, errBody) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorResponse{}, lastErr
		}
		stream, state := e.cliEventStream(ctx, chunks, echoModel, streamFramingBare)
		for chunk := range stream {
			if chunk.Err != nil {
				return pluginapi.ExecutorResponse{}, chunk.Err
			}
		}
		fixed, _ := mapReasoningBody(state.aggregate())
		return pluginapi.ExecutorResponse{Payload: fixed, Headers: headers}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorResponse{}, lastErr
	}
	return pluginapi.ExecutorResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

// ExecuteStream performs a streaming chat completion. Normalized chunks stay
func (e *Executor) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	framing := streamFramingForRequest(req)
	members := e.cfg.members(req)
	if len(members) == 0 {
		return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMsg}
	}
	if e.useCLITransport() {
		return e.executeCLIStream(ctx, req, members, framing)
	}
	body := e.buildUpstreamBody(req.Model, req.Payload, true)
	var lastErr error
	for _, idx := range e.keypool.order(members) {
		m := members[idx]
		d, err := e.keypool.clientFor(idx, m, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		status, headers, chunks, err := d.doStream(ctx, e.endpoint(), upstreamHeaders(strings.TrimSpace(m.Key), true), body)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorStreamResponse{}, err
		}
		if status < 200 || status >= 300 {
			errBody := readStreamErrorBody(ctx, chunks)
			lastErr = statusError{statusCode: status, body: errBody}
			if retryableBody(status, errBody) && ctx.Err() == nil {
				continue
			}
			if e.autoCLIFallback() && planRefusal(status, errBody) {
				return e.executeCLIStream(ctx, req, members, framing)
			}
			return pluginapi.ExecutorStreamResponse{}, lastErr
		}
		return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: convertChunks(ctx, chunks, framing)}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorStreamResponse{}, lastErr
	}
	return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

// executeCLIStream runs one streaming request through the CLI route. Failover
// applies before the first upstream byte only, matching the provider path.
func (e *Executor) executeCLIStream(ctx context.Context, req pluginapi.ExecutorRequest, members []APIKeyEntry, framing streamFramingPolicy) (pluginapi.ExecutorStreamResponse, error) {
	model := e.upstreamModelFor(req)
	echoModel := e.responseModelFor(req)
	body := e.buildCLIBody(model, req.Payload)
	var lastErr error
	for _, idx := range e.keypool.order(members) {
		m := members[idx]
		d, err := e.keypool.clientFor(idx, m, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		status, headers, chunks, err := d.doStream(ctx, e.cliEndpoint(), e.cliHeaders(strings.TrimSpace(m.Key)), body)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorStreamResponse{}, err
		}
		if status < 200 || status >= 300 {
			errBody := readStreamErrorBody(ctx, chunks)
			lastErr = statusError{statusCode: status, body: errBody}
			if retryableBody(status, errBody) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorStreamResponse{}, lastErr
		}
		stream, _ := e.cliEventStream(ctx, chunks, echoModel, framing)
		return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: stream}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorStreamResponse{}, lastErr
	}
	return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

// upstreamModelFor resolves the vendor model name for a request: the configured
// alias mapping first, then the host's already-rewritten name.
func (e *Executor) upstreamModelFor(req pluginapi.ExecutorRequest) string {
	e.cfg.ensureIndexes()
	if vendor := e.cfg.upstreamName(req.Model); vendor != "" {
		return vendor
	}
	return strings.TrimSpace(req.Model)
}

// responseModelFor resolves the model name a response must echo back.
//
// OpenAI clients read `model` to attribute the reply, so it has to be the name
// the client asked for. The vendor name belongs in the upstream request body
// only: reporting it here makes every caller see a model it never requested,
// and the host logs a model-substitution warning for each call because the two
// names disagree.
//
// The host passes the client's original spelling in Metadata under
// "requested_model"; req.Model may already be the vendor name by then. When the
// metadata is absent, fall back to reversing the configured alias mapping so a
// vendor name still maps back to the alias clients use.
func (e *Executor) responseModelFor(req pluginapi.ExecutorRequest) string {
	if requested := metadataString(req.Metadata, coreexecutor.RequestedModelMetadataKey); requested != "" {
		return requested
	}
	model := strings.TrimSpace(req.Model)
	e.cfg.ensureIndexes()
	if alias := e.cfg.aliasFor(model); alias != "" {
		return alias
	}
	return model
}

// metadataString reads one metadata value as a trimmed string.
func metadataString(metadata map[string]any, key string) string {
	if metadata == nil || key == "" {
		return ""
	}
	raw, ok := metadata[key]
	if !ok {
		return ""
	}
	if text, isText := raw.(string); isText {
		return strings.TrimSpace(text)
	}
	return ""
}

// convertChunks normalizes each upstream SSE data payload (reasoning
// backfill) and applies the route-specific executor framing policy.
//
// OpenAI chat and Responses routes stay bare because their downstream paths
// accept or add SSE framing. Claude Messages receives one data: prefix for the
// host's OpenAI-to-Claude translator. Empty lines are dropped, and upstream
// [DONE] is swallowed because the host emits its own stream tail.
//
// The host delivers arbitrary 32KB raw reads, so one SSE line can straddle
// two chunks. We buffer until a newline completes the line; only complete
// lines go through normalizeStreamLine. The tail remainder is flushed when
// the upstream closes.
func convertChunks(ctx context.Context, in <-chan pluginapi.HTTPStreamChunk, framing streamFramingPolicy) <-chan pluginapi.ExecutorStreamChunk {
	// One slot lets a terminal cancellation error be reported even when the
	// caller stops draining the stream at the same time.
	out := make(chan pluginapi.ExecutorStreamChunk, 1)
	go func() {
		defer close(out)
		var pending []byte
		emitError := func(err error) {
			terminal := pluginapi.ExecutorStreamChunk{Err: err}
			select {
			case out <- terminal:
			case <-ctx.Done():
				// If a payload already occupies the slot, cancellation must still
				// let this goroutine terminate rather than block on error delivery.
				select {
				case out <- terminal:
				default:
				}
			}
		}
		emit := func(payload []byte) bool {
			if len(bytes.TrimSpace(payload)) == 0 {
				return true
			}
			payload = framing.apply(payload)
			select {
			case <-ctx.Done():
				return false
			case out <- pluginapi.ExecutorStreamChunk{Payload: payload}:
				return true
			}
		}
		for {
			select {
			case <-ctx.Done():
				emitError(ctx.Err())
				return
			case chunk, ok := <-in:
				if !ok {
					if frame := normalizeStreamLine(pending); len(bytes.TrimSpace(frame)) > 0 {
						emit(frame)
					}
					return
				}
				if chunk.Err != nil {
					emitError(chunk.Err)
					return
				}
				pending = append(pending, chunk.Payload...)
				for {
					idx := bytes.IndexByte(pending, '\n')
					if idx < 0 {
						break
					}
					line := pending[:idx+1]
					pending = pending[idx+1:]
					if !emit(normalizeStreamLine(line)) {
						return
					}
				}
				// Guard against unbounded growth on a never-ending line.
				if len(pending) > 4<<20 {
					if !emit(normalizeStreamLine(pending)) {
						return
					}
					pending = nil
				}
			}
		}
	}()
	return out
}

// normalizeStreamLine converts one complete upstream SSE line into a bare
// JSON payload. Stacked prefixes are collapsed, reasoning_content is
// backfilled, and empty lines and [DONE] yield nil. Route-specific framing is
// applied later by convertChunks.
func normalizeStreamLine(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		// Non-SSE bytes (e.g. event: lines, comments): drop, never corrupt.
		return nil
	}
	payload := trimmed
	for bytes.HasPrefix(bytes.TrimSpace(payload), []byte("data:")) {
		p := bytes.TrimSpace(payload)
		payload = bytes.TrimSpace(p[len("data:"):])
	}
	if len(payload) == 0 || string(payload) == "[DONE]" {
		return nil
	}
	if !json.Valid(payload) {
		return nil
	}
	if fixed, ok := mapReasoningBody(payload); ok {
		return fixed
	}
	return payload
}

// normalizeStreamBytes handles one raw host-stream read (kept for unit
// tests and non-streaming helpers): same bare-JSON contract as
// normalizeStreamLine, joined back with newlines for assertion convenience.
func normalizeStreamBytes(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	lines := bytes.Split(raw, []byte("\n"))
	var out [][]byte
	for _, line := range lines {
		if frame := normalizeStreamLine(line); len(bytes.TrimSpace(frame)) > 0 {
			out = append(out, frame)
		}
	}
	if len(out) == 0 {
		return raw
	}
	return bytes.Join(out, []byte("\n"))
}

// CountTokens is a local estimate; commandcode exposes no tokenize endpoint.
func (e *Executor) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	_ = ctx
	count := int64(len(req.Payload) / 4)
	if count < 1 && len(req.Payload) > 0 {
		count = 1
	}
	usage := map[string]any{
		"prompt_tokens":     count,
		"completion_tokens": 0,
		"total_tokens":      count,
	}
	raw, _ := json.Marshal(map[string]any{
		"id":      "commandcode-count",
		"object":  "chat.completion",
		"created": 0,
		"model":   req.Model,
		"choices": []any{},
		"usage":   usage,
	})
	return pluginapi.ExecutorResponse{Payload: raw}, nil
}

// HttpRequest bridges raw executor HTTP through the pool: first member's
// transport (pool order is stable per call) with the resolved api key
// injected when the caller did not set Authorization.
func (e *Executor) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	if strings.TrimSpace(req.URL) == "" {
		return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("commandcode executor: request URL is required")
	}
	headers := req.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	members := e.cfg.members(poolReqFromHTTP(req))
	if len(members) == 0 && headers.Get("Authorization") == "" {
		return pluginapi.ExecutorHTTPResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMsg}
	}
	if headers.Get("Authorization") == "" {
		headers.Set("Authorization", "Bearer "+strings.TrimSpace(members[0].Key))
	}
	var d doer
	if len(members) > 0 {
		var err error
		d, err = e.keypool.clientFor(0, members[0], req.HTTPClient)
		if err != nil {
			return pluginapi.ExecutorHTTPResponse{}, err
		}
	} else {
		if req.HTTPClient == nil {
			return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("commandcode executor: host HTTP client is required")
		}
		d = hostDoer{client: req.HTTPClient}
	}
	status, respHeaders, respBody, err := d.do(ctx, strings.TrimSpace(req.URL), headers, req.Body)
	if err != nil {
		return pluginapi.ExecutorHTTPResponse{}, err
	}
	return pluginapi.ExecutorHTTPResponse{StatusCode: status, Headers: respHeaders, Body: respBody}, nil
}

// statusError carries an upstream HTTP status back to the host (the ABI
// error envelope preserves it as http_status for retry classification).
type statusError struct {
	statusCode int
	msg        string
	body       []byte
}

func (e statusError) Error() string {
	if strings.TrimSpace(e.msg) != "" {
		return e.msg
	}
	if len(e.body) > 0 {
		return upstreamErrorMessage(e.body)
	}
	return fmt.Sprintf("status %d", e.statusCode)
}

func (e statusError) StatusCode() int { return e.statusCode }

func upstreamErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var decoded struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		if len(decoded.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if errObj := json.Unmarshal(decoded.Error, &obj); errObj == nil && strings.TrimSpace(obj.Message) != "" {
				return strings.TrimSpace(obj.Message)
			}
			var s string
			if errStr := json.Unmarshal(decoded.Error, &s); errStr == nil && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		if strings.TrimSpace(decoded.Message) != "" {
			return strings.TrimSpace(decoded.Message)
		}
	}
	if len(trimmed) > 500 {
		return trimmed[:500]
	}
	return trimmed
}

func readStreamErrorBody(ctx context.Context, chunks <-chan pluginapi.HTTPStreamChunk) []byte {
	const maxBytes = 1 << 20
	body := make([]byte, 0)
	if chunks == nil {
		return body
	}
	for len(body) < maxBytes {
		select {
		case <-ctx.Done():
			return body
		case chunk, ok := <-chunks:
			if !ok {
				return body
			}
			if len(chunk.Payload) > 0 {
				remaining := maxBytes - len(body)
				if len(chunk.Payload) > remaining {
					return append(body, chunk.Payload[:remaining]...)
				}
				body = append(body, chunk.Payload...)
			}
			if chunk.Err != nil {
				return body
			}
		}
	}
	return body
}
