package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const aliasMappingConfig = `
api_key: test-key
transport: cli
cli_base_url: https://cli.example.invalid
models:
  - alias: deepseek-v4.1-flash
    name: deepseek/deepseek-v4.1-flash
  - alias: cc-deepseek-v4.1-flash
    name: deepseek/deepseek-v4.1-flash-fast
    display_name: cc-deepseek-v4.1-flash
`

// The vendor name must go upstream while the response echoes the name the client
// asked for. Reporting the vendor name makes every caller see a model it never
// requested and makes the host log a model-substitution warning per call.
func TestResponseModelEchoesRequestedAlias(t *testing.T) {
	tests := []struct {
		name      string
		reqModel  string
		metadata  map[string]any
		wantEcho  string
		transport string
	}{
		{
			name:     "alias request",
			reqModel: "cc-deepseek-v4.1-flash",
			wantEcho: "cc-deepseek-v4.1-flash",
		},
		{
			name:     "vendor name in req.Model maps back to the alias",
			reqModel: "deepseek/deepseek-v4.1-flash-fast",
			wantEcho: "cc-deepseek-v4.1-flash",
		},
		{
			name:     "requested_model metadata wins over req.Model",
			reqModel: "deepseek/deepseek-v4.1-flash-fast",
			metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "cc-deepseek-v4.1-flash"},
			wantEcho: "cc-deepseek-v4.1-flash",
		},
		{
			name:     "unconfigured model is echoed unchanged",
			reqModel: "some-other-model",
			wantEcho: "some-other-model",
		},
		{
			name:     "non-string metadata is ignored",
			reqModel: "deepseek/deepseek-v4.1-flash-fast",
			metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: 42},
			wantEcho: "cc-deepseek-v4.1-flash",
		},
		{
			name:     "blank metadata falls back to the alias mapping",
			reqModel: "deepseek/deepseek-v4.1-flash-fast",
			metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "   "},
			wantEcho: "cc-deepseek-v4.1-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewExecutor(parseConfig([]byte(aliasMappingConfig)), nil)
			got := executor.responseModelFor(pluginapi.ExecutorRequest{Model: tt.reqModel, Metadata: tt.metadata})
			if got != tt.wantEcho {
				t.Fatalf("responseModelFor(%q) = %q, want %q", tt.reqModel, got, tt.wantEcho)
			}
		})
	}
}

// The upstream request body must still carry the vendor name: the CLI route
// rejects a bare alias.
func TestUpstreamBodyKeepsVendorName(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte(aliasMappingConfig)), nil)
	req := pluginapi.ExecutorRequest{
		Model:   "cc-deepseek-v4.1-flash",
		Payload: []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[]}`),
	}
	vendor := executor.upstreamModelFor(req)
	if vendor != "deepseek/deepseek-v4.1-flash-fast" {
		t.Fatalf("upstreamModelFor = %q, want the vendor name", vendor)
	}
	if echo := executor.responseModelFor(req); echo != "cc-deepseek-v4.1-flash" {
		t.Fatalf("responseModelFor = %q, want the requested alias", echo)
	}
}

// End to end through the streaming executor: the response chunk's model field
// must be the requested alias, not the vendor name.
func TestStreamingResponseCarriesRequestedAlias(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte(aliasMappingConfig)), nil)
	client := staticStreamHTTPClient{payloads: [][]byte{
		[]byte(`{"type":"text-delta","text":"OK"}` + "\n"),
		[]byte(`{"type":"finish","finishReason":"stop"}` + "\n"),
	}}
	response, err := executor.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
		Model:      "cc-deepseek-v4.1-flash",
		Payload:    []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[]}`),
		HTTPClient: client,
		Metadata:   map[string]any{coreexecutor.RequestedModelMetadataKey: "cc-deepseek-v4.1-flash"},
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var seen []string
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		var decoded struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(chunk.Payload, &decoded); err != nil {
			continue
		}
		if decoded.Model != "" {
			seen = append(seen, decoded.Model)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no chunk carried a model field")
	}
	for _, model := range seen {
		if model != "cc-deepseek-v4.1-flash" {
			t.Fatalf("chunk reported model %q, want the requested alias; saw %v", model, seen)
		}
		if strings.Contains(model, "/") {
			t.Fatalf("chunk leaked a vendor name: %q", model)
		}
	}
}

// Non-streaming aggregation must echo the alias too.
func TestAggregatedResponseCarriesRequestedAlias(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte(aliasMappingConfig)), nil)
	client := staticStreamHTTPClient{payloads: [][]byte{
		[]byte(`{"type":"text-delta","text":"OK"}` + "\n"),
		[]byte(`{"type":"finish","finishReason":"stop"}` + "\n"),
	}}
	response, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		Model:      "cc-deepseek-v4.1-flash",
		Payload:    []byte(`{"model":"cc-deepseek-v4.1-flash","messages":[]}`),
		HTTPClient: client,
		Metadata:   map[string]any{coreexecutor.RequestedModelMetadataKey: "cc-deepseek-v4.1-flash"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.Payload, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Model != "cc-deepseek-v4.1-flash" {
		t.Fatalf("aggregated model = %q, want the requested alias", decoded.Model)
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) != "OK" {
		t.Fatalf("unexpected aggregated body: %s", response.Payload)
	}
	_ = http.StatusOK
}
