package main

// Live probe: drives the plugin's own CLI transport against the real upstream.
// It supplies a minimal HostHTTPClient so the shipped code path runs unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	plug "github.com/ahoo/cpa-plugin-commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// probeClient is a stand-in for the host HTTP client (same contract).
type probeClient struct{ c *http.Client }

func (p probeClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(string(req.Body)))
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	httpReq.Header = req.Headers.Clone()
	resp, err := p.c.Do(httpReq)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header, Body: body}, nil
}

func (p probeClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(string(req.Body)))
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	httpReq.Header = req.Headers.Clone()
	resp, err := p.c.Do(httpReq)
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	ch := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		buf := make([]byte, 32*1024)
		for {
			n, errRead := resp.Body.Read(buf)
			if n > 0 {
				select {
				case <-ctx.Done():
					return
				case ch <- pluginapi.HTTPStreamChunk{Payload: append([]byte(nil), buf[:n]...)}:
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					select {
					case <-ctx.Done():
					case ch <- pluginapi.HTTPStreamChunk{Err: errRead}:
					}
				}
				return
			}
		}
	}()
	return pluginapi.HTTPStreamResponse{StatusCode: resp.StatusCode, Headers: resp.Header, Chunks: ch}, nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: liveprobe <api-key> <upstream-model>")
		os.Exit(2)
	}
	key, model := os.Args[1], os.Args[2]

	cfgYAML := []byte("transport: cli\napi_keys:\n  - key: " + key + "\n    weight: 10\nmodels:\n  - alias: deepseek-flash\n    name: " + model + "\n")
	_, handler := plug.Build(cfgYAML)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	client := probeClient{c: &http.Client{Timeout: 0}}
	payload := []byte(`{"model":"deepseek-flash","messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"用一句话说明你是什么模型"}],"max_tokens":300}`)
	req := pluginapi.ExecutorRequest{Model: "deepseek-flash", Payload: payload, HTTPClient: client}

	fmt.Println("=== 1) streaming path (ExecuteStream) ===")
	stream, err := handler.ExecuteStream(ctx, req)
	if err != nil {
		fmt.Println("  ERROR:", err)
	} else {
		n := 0
		var text, reasoning, finish string
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				fmt.Println("  chunk error:", chunk.Err)
				break
			}
			n++
			s := string(chunk.Payload)
			if n <= 5 {
				fmt.Printf("  chunk %d (%d bytes): %s\n", n, len(s), head(s, 220))
			}
			text += field(s, "content")
			reasoning += field(s, "reasoning_content")
			if f := field(s, "finish_reason"); f != "" && f != "null" {
				finish = f
			}
		}
		fmt.Printf("  total chunks=%d finish=%q\n", n, finish)
		fmt.Printf("  text      = %q\n", head(text, 140))
		fmt.Printf("  reasoning = %q\n", head(reasoning, 200))
	}

	fmt.Println("=== 2) non-streaming path (Execute → aggregate) ===")
	resp, err := handler.Execute(ctx, req)
	if err != nil {
		fmt.Println("  ERROR:", err)
	} else {
		var out map[string]any
		if err := json.Unmarshal(resp.Payload, &out); err != nil {
			fmt.Println("  payload not JSON:", head(string(resp.Payload), 300))
		} else {
			choices, _ := out["choices"].([]any)
			var msg map[string]any
			if len(choices) > 0 {
				msg, _ = choices[0].(map[string]any)["message"].(map[string]any)
			}
			fmt.Printf("  object=%v model=%v finish=%v\n", out["object"], out["model"], firstFinish(choices))
			if msg != nil {
				fmt.Printf("  content   = %q\n", head(fmt.Sprint(msg["content"]), 140))
				fmt.Printf("  reasoning = %q\n", head(fmt.Sprint(msg["reasoning_content"]), 200))
			}
			fmt.Printf("  usage     = %v\n", out["usage"])
		}
	}
}

func firstFinish(choices []any) any {
	if len(choices) == 0 {
		return nil
	}
	m, _ := choices[0].(map[string]any)
	return m["finish_reason"]
}

// field extracts a top-level JSON string field value from a chunk payload.
func field(s, name string) string {
	needle := `"` + name + `":"`
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	rest := s[i+len(needle):]
	var b strings.Builder
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c == '\\' && j+1 < len(rest) {
			b.WriteByte(rest[j])
			b.WriteByte(rest[j+1])
			j++
			continue
		}
		if c == '"' {
			break
		}
		b.WriteByte(c)
	}
	return b.String()
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
