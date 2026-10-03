package plugin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// creditRefusalBody is the route's key-level refusal, verbatim from production:
// the request is valid, the account behind that key cannot pay for it.
const creditRefusalBody = `{"success":false,"error":{"code":"BAD_REQUEST","status":400,` +
	`"message":"You have insufficient credits to make this request. Please purchase more credits to continue using the service."}}`

// validationRefusalBody is a request-level 400: another key cannot help.
const validationRefusalBody = `{"error":{"message":"Invalid request error. HINT: Validation error: ` +
	`Invalid option: expected one of \"user\"|\"assistant\" at \"params.messages[4].role\"",` +
	`"type":"invalid_request_error"}}`

// keyedStreamHTTPClient answers per Authorization key so a pooled call can be
// driven through a drained member before reaching a healthy one.
type keyedStreamHTTPClient struct {
	mu      sync.Mutex
	seen    []string
	refused []string
	body    string
	status  int
}

func (c *keyedStreamHTTPClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, errors.New("unexpected non-streaming request")
}

func (c *keyedStreamHTTPClient) DoStream(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	key := strings.TrimPrefix(req.Headers.Get("Authorization"), "Bearer ")
	c.mu.Lock()
	c.seen = append(c.seen, key)
	c.mu.Unlock()
	for _, refused := range c.refused {
		if refused != key {
			continue
		}
		chunks := make(chan pluginapi.HTTPStreamChunk, 1)
		chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(c.body)}
		close(chunks)
		return pluginapi.HTTPStreamResponse{StatusCode: c.status, Chunks: chunks}, nil
	}
	chunks := make(chan pluginapi.HTTPStreamChunk, 2)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"text-delta","text":"OK"}` + "\n")}
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"finish","finishReason":"stop"}` + "\n")}
	close(chunks)
	return pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}, nil
}

func (c *keyedStreamHTTPClient) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// A drained account must not fail a call that a sibling member can serve. The
// refusal arrives as a 400, which the blanket 4xx rule treats as request-bad.
func TestCreditRefusalIsRetryableDespite400(t *testing.T) {
	if !retryableBody(http.StatusBadRequest, []byte(creditRefusalBody)) {
		t.Fatal("a credit refusal on 400 must fail over: another pool member may be funded")
	}
	if retryableBody(http.StatusBadRequest, []byte(validationRefusalBody)) {
		t.Fatal("a validation 400 must fail fast: retrying another key cannot help")
	}
	if retryableBody(http.StatusBadRequest, nil) {
		t.Fatal("an empty 400 body must fail fast")
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		if !retryableBody(status, nil) {
			t.Fatalf("status %d must stay retryable", status)
		}
	}
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		if retryableBody(status, []byte(creditRefusalBody)) {
			t.Fatalf("status %d must not become retryable", status)
		}
	}
}

// With one heavily weighted drained member and one funded member, every call
// must still return the funded member's stream whatever order the pool picked.
// Twenty rounds make "the pool picked the healthy key first every time"
// (p = (1/11)^20) impossible as an explanation.
func TestExecuteStreamSurvivesADrainedPoolMember(t *testing.T) {
	cfg := parseConfig([]byte("transport: cli\napi_keys:\n" +
		"  - key: drained-key\n    weight: 10\n" +
		"  - key: funded-key\n    weight: 1\n"))
	executor := NewExecutor(cfg, nil)
	client := &keyedStreamHTTPClient{
		refused: []string{"drained-key"},
		body:    creditRefusalBody,
		status:  http.StatusBadRequest,
	}
	for round := 0; round < 20; round++ {
		response, err := executor.ExecuteStream(t.Context(), pluginapi.ExecutorRequest{
			Model:      "cc-deepseek-v4.1-flash",
			Payload:    []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
			HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("round %d: the funded member was never reached: %v", round, err)
		}
		var payloads [][]byte
		for chunk := range response.Chunks {
			if chunk.Err != nil {
				t.Fatalf("round %d: stream error %v", round, chunk.Err)
			}
			payloads = append(payloads, chunk.Payload)
		}
		joined := string(bytes.Join(payloads, nil))
		if !strings.Contains(joined, "OK") {
			t.Fatalf("round %d: served payload lost the funded member's text: %q", round, joined)
		}
		if strings.Contains(joined, "insufficient credits") {
			t.Fatalf("round %d: the drained member's refusal leaked to the client", round)
		}
	}
	if client.attempts() < 20 {
		t.Fatalf("only %d upstream attempts for 20 calls, expected at least one per call", client.attempts())
	}
}

// The exception must stay narrow: a request-level 400 fails fast instead of
// burning every funded key on a payload the route will always reject.
func TestExecuteStreamDoesNotFailOverOnValidation400(t *testing.T) {
	cfg := parseConfig([]byte("transport: cli\napi_keys:\n" +
		"  - key: key-a\n    weight: 1\n" +
		"  - key: key-b\n    weight: 1\n"))
	executor := NewExecutor(cfg, nil)
	client := &keyedStreamHTTPClient{
		refused: []string{"key-a", "key-b"},
		body:    validationRefusalBody,
		status:  http.StatusBadRequest,
	}
	_, err := executor.ExecuteStream(t.Context(), pluginapi.ExecutorRequest{
		Model:      "cc-deepseek-v4.1-flash",
		Payload:    []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
		HTTPClient: client,
	})
	if err == nil {
		t.Fatal("a validation 400 must surface as an error")
	}
	if got := client.attempts(); got != 1 {
		t.Fatalf("validation 400 caused %d upstream attempts, want exactly 1", got)
	}
	if !strings.Contains(err.Error(), "Invalid option") {
		t.Fatalf("error lost the upstream reason: %v", err)
	}
}
