package plugin

// Management panel support.
//
// The host exposes plugin-owned Management API routes under /v0/management/ and
// browser-navigable resources under /v0/resource/plugins/<id>/. Registering a GET
// route with a Menu label makes the host list it in the Management Center's menu;
// the page itself is a small self-contained HTML document that fetches the JSON
// routes below.
//
// Two routes are served:
//
//	GET <base>/commandcode/status   → JSON: transport, model map, key pool, plan
//	GET <base>/commandcode/index.html → the panel page (resource, menu entry)
//
// The plan/quota section reads CommandCode's own account surface
// (/alpha/whoami, /alpha/billing/credits, /alpha/billing/subscriptions), which is
// available on every plan tier, so the panel shows the account even when the
// chat route is refused.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// managementBasePath is the plugin's Management API prefix.
const managementBasePath = "/commandcode"

// statusCacheTTL keeps the panel from hammering the account endpoints: the host
// calls the route on every page load, and the values only move slowly.
const statusCacheTTL = 30 * time.Second

var statusCache = struct {
	sync.Mutex
	at     time.Time
	body   []byte
	status int
}{}

// registerManagementRoutes declares the plugin's Management API routes and its
func (p *CommandCodePlugin) registerManagementRoutes(_ context.Context, req pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	base := strings.TrimSuffix(strings.TrimSpace(req.BasePath), "/")
	if base == "" {
		base = managementBasePath
	}
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: base + "/status", Handler: p},
		},
		Resources: []pluginapi.ResourceRoute{
			{Path: "/index.html", Menu: "CommandCode", Description: "CommandCode 渠道状态与套餐额度", Handler: p},
		},
	}, nil
}

// handleManagementRequest serves the plugin's own routes.
func (p *CommandCodePlugin) handleManagementRequest(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := strings.TrimSpace(req.Path)
	switch {
	case strings.HasSuffix(path, "/status"):
		return p.managementStatus(ctx)
	case strings.HasSuffix(path, "/index.html"), path == "" || strings.HasSuffix(path, "/"):
		return managementPage(), nil
	default:
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Body: []byte(`{"error":"not found"}`)}, nil
	}
}

// managementStatus returns the plugin's runtime state plus the live account view.
func (p *CommandCodePlugin) managementStatus(ctx context.Context) (pluginapi.ManagementResponse, error) {
	statusCache.Lock()
	if len(statusCache.body) > 0 && time.Since(statusCache.at) < statusCacheTTL {
		body, code := statusCache.body, statusCache.status
		statusCache.Unlock()
		return pluginapi.ManagementResponse{StatusCode: code, Headers: jsonHeaders(), Body: body}, nil
	}
	statusCache.Unlock()

	payload := map[string]any{
		"plugin":       "commandcode",
		"version":      pluginVersion,
		"transport":    p.cfg.transportMode(),
		"base_url":     p.cfg.baseURL(),
		"cli_base":     p.cfg.cliBaseURL(),
		"cli_version":  p.cfg.cliVersion(),
		"generated_at": time.Now().Format(time.RFC3339),
	}

	entries := p.cfg.effectiveModels()
	models := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		models = append(models, map[string]any{
			"alias":     entry.Alias,
			"upstream":  entry.Name,
			"namespace": Provider + "/" + firstNonEmpty(entry.Name, entry.Alias),
			"label":     entry.label(),
		})
	}
	payload["models"] = models

	payload["keys"] = p.keyPoolView()
	payload["account"] = p.accountView(ctx)

	body, err := json.Marshal(payload)
	if err != nil {
		return pluginapi.ManagementResponse{StatusCode: http.StatusInternalServerError, Body: []byte(`{"error":"marshal failed"}`)}, nil
	}
	statusCache.Lock()
	statusCache.at, statusCache.body, statusCache.status = time.Now(), body, http.StatusOK
	statusCache.Unlock()
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: jsonHeaders(), Body: body}, nil
}

// keyPoolView lists the configured credentials without exposing them.
func (p *CommandCodePlugin) keyPoolView() []map[string]any {
	out := make([]map[string]any, 0, len(p.cfg.APIKeys))
	for _, entry := range p.cfg.APIKeys {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		row := map[string]any{
			"label":    maskCredential(key),
			"weight":   entry.normWeight(),
			"disabled": entry.Disabled,
			"proxy":    strings.TrimSpace(entry.ProxyURL) != "",
		}
		out = append(out, row)
	}
	if len(out) == 0 && strings.TrimSpace(p.cfg.APIKey) != "" {
		out = append(out, map[string]any{
			"label": maskCredential(strings.TrimSpace(p.cfg.APIKey)), "weight": 1, "disabled": false, "proxy": false,
		})
	}
	return out
}

// accountView reads CommandCode's account surface. Every field is optional: a
// failure is reported inside the payload rather than failing the page.
func (p *CommandCodePlugin) accountView(ctx context.Context) map[string]any {
	out := map[string]any{}
	key := ""
	if len(p.cfg.APIKeys) > 0 {
		for _, entry := range p.cfg.APIKeys {
			if !entry.Disabled && strings.TrimSpace(entry.Key) != "" {
				key = strings.TrimSpace(entry.Key)
				break
			}
		}
	}
	if key == "" {
		key = strings.TrimSpace(p.cfg.APIKey)
	}
	if key == "" {
		out["error"] = "no api key configured"
		return out
	}
	client := p.accountHTTPClient()
	if client == nil {
		out["error"] = "host http client unavailable"
		return out
	}

	if who, err := p.accountGet(ctx, client, key, "/alpha/whoami?limits=1"); err == nil {
		out["whoami"] = who
	} else {
		out["whoami_error"] = err.Error()
	}
	if credits, err := p.accountGet(ctx, client, key, "/alpha/billing/credits"); err == nil {
		out["credits"] = credits
	} else {
		out["credits_error"] = err.Error()
	}
	if sub, err := p.accountGet(ctx, client, key, "/alpha/billing/subscriptions"); err == nil {
		out["subscription"] = sub
	} else {
		out["subscription_error"] = err.Error()
	}
	return out
}

// accountHTTPClient returns the client used for account reads. The host client
// (captured from the last executor request) keeps the host's proxy policy and
// request log; before any request has been seen — a panel opened on a fresh
// process — a plain client is used so the page still renders account data.
func (p *CommandCodePlugin) accountHTTPClient() pluginapi.HostHTTPClient {
	if p.executor != nil {
		if c := p.executor.lastHostClient(); c != nil {
			return c
		}
	}
	return directHTTPClient{}
}

// directHTTPClient is the fallback host-client implementation for panel reads.
type directHTTPClient struct{}

func (directHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	httpReq.Header = req.Headers.Clone()
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header}, errRead
	}
	return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header, Body: body}, nil
}

func (directHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	httpReq.Header = req.Headers.Clone()
	resp, err := http.DefaultClient.Do(httpReq)
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

func (p *CommandCodePlugin) accountGet(ctx context.Context, client pluginapi.HostHTTPClient, key, path string) (map[string]any, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Accept", "application/json")
	headers.Set("accept-encoding", "identity")
	headers.Set("x-command-code-version", p.cfg.cliVersion())
	headers.Set("x-cli-environment", "production")
	headers.Set("User-Agent", p.cfg.cliUserAgent())
	resp, err := client.Do(reqCtx, pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     p.cfg.cliBaseURL() + path,
		Headers: headers,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForPanel(string(resp.Body), 160))
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp.Body, &decoded); err != nil {
		return nil, fmt.Errorf("invalid json")
	}
	return decoded, nil
}

func jsonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return h
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateForPanel(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// maskCredential keeps a short tail so operators can tell keys apart.
func maskCredential(key string) string {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) <= 8 {
		return "…"
	}
	return trimmed[:3] + "…" + trimmed[len(trimmed)-4:]
}

// transportMode reports the configured transport with its default applied.
func (c *pluginConfig) transportMode() string {
	if c == nil {
		return "provider"
	}
	switch strings.ToLower(strings.TrimSpace(c.Transport)) {
	case "cli":
		return "cli"
	case "auto":
		return "auto"
	default:
		return "provider"
	}
}
