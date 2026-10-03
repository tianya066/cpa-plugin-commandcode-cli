package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementBasePath = "/commandcode"
const statusCacheTTL = 30 * time.Second

type managementClientKey struct{}

// WithManagementHTTPClient binds the current ABI callback to this request only.
// Never retain the client: the host closes its callback when the request ends.
func WithManagementHTTPClient(ctx context.Context, client pluginapi.HostHTTPClient) context.Context {
	return context.WithValue(ctx, managementClientKey{}, client)
}

func managementHTTPClient(ctx context.Context) pluginapi.HostHTTPClient {
	client, _ := ctx.Value(managementClientKey{}).(pluginapi.HostHTTPClient)
	return client
}

type accountCacheEntry struct {
	at      time.Time
	account map[string]any
}

func (p *CommandCodePlugin) registerManagementRoutes(_ context.Context, req pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	base := strings.TrimSuffix(strings.TrimSpace(req.BasePath), "/")
	if base == "" {
		base = managementBasePath
	}
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: base + "/status", Handler: p},
			{Method: http.MethodGet, Path: base + "/settings", Handler: p},
			{Method: http.MethodPost, Path: base + "/settings", Handler: p},
			{Method: http.MethodPost, Path: base + "/test", Handler: p},
		},
		Resources: []pluginapi.ResourceRoute{{Path: "/index.html", Menu: "CommandCode", Description: "CommandCode 账号、模型与运行设置", Handler: p}},
	}, nil
}

func (p *CommandCodePlugin) handleManagementRequest(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := strings.TrimSpace(req.Path)
	method := strings.ToUpper(req.Method)
	var allowed string
	switch {
	case strings.HasSuffix(path, "/status"):
		allowed = http.MethodGet
		if method == allowed {
			return p.managementStatus(ctx, req.Query.Get("refresh") == "1")
		}
	case strings.HasSuffix(path, "/settings"):
		allowed = "GET, POST"
		if method == http.MethodGet {
			return p.managementSettings(ctx, req)
		}
		if method == http.MethodPost {
			return p.saveManagementSettings(ctx, req)
		}
	case strings.HasSuffix(path, "/test"):
		allowed = http.MethodPost
		if method == allowed {
			return p.managementTest(ctx, req)
		}
	case strings.HasSuffix(path, "/index.html"), path == "" || strings.HasSuffix(path, "/"):
		allowed = http.MethodGet
		if method == allowed {
			return managementPage(), nil
		}
	default:
		return managementError(http.StatusNotFound, "not found"), nil
	}
	resp := managementError(http.StatusMethodNotAllowed, "method not allowed")
	resp.Headers.Set("Allow", allowed)
	return resp, nil
}

func (p *CommandCodePlugin) managementStatus(ctx context.Context, refresh bool) (pluginapi.ManagementResponse, error) {
	models := make([]map[string]any, 0)
	for _, entry := range p.cfg.effectiveModels() {
		models = append(models, map[string]any{"alias": entry.Alias, "upstream": entry.Name, "namespace": Provider + "/" + firstNonEmpty(entry.Name, entry.Alias), "label": entry.label()})
	}
	entries := configuredKeys(p.cfg)
	accounts := make([]map[string]any, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(i int, entry APIKeyEntry) {
			defer wg.Done()
			row := keyView(entry)
			row["account"] = p.accountView(ctx, entry, refresh)
			accounts[i] = row
		}(i, entry)
	}
	wg.Wait()
	var first any = map[string]any{"error": "no api key configured"}
	if len(accounts) > 0 {
		first = accounts[0]["account"]
	}
	return managementJSON(http.StatusOK, map[string]any{
		"plugin": "commandcode", "version": pluginVersion,
		"revision": configRevision(p.cfg), "transport": p.cfg.transportMode(),
		"base_url": p.cfg.baseURL(), "cli_base": p.cfg.cliBaseURL(), "cli_version": p.cfg.cliVersion(),
		"generated_at": time.Now().Format(time.RFC3339), "models": models,
		"keys": p.keyPoolView(), "accounts": accounts, "account": first,
	}), nil
}

func configuredKeys(cfg *pluginConfig) []APIKeyEntry {
	out := make([]APIKeyEntry, 0, len(cfg.APIKeys))
	for _, entry := range cfg.APIKeys {
		entry.Key = strings.TrimSpace(entry.Key)
		if entry.Key != "" {
			out = append(out, entry)
		}
	}
	if len(out) == 0 && strings.TrimSpace(cfg.APIKey) != "" {
		out = append(out, APIKeyEntry{Key: strings.TrimSpace(cfg.APIKey), Weight: 1})
	}
	return out
}

func credentialID(key string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(hash[:16])
}

func keyView(entry APIKeyEntry) map[string]any {
	return map[string]any{"id": credentialID(entry.Key), "label": maskCredential(entry.Key), "name": entry.Name, "weight": entry.normWeight(), "disabled": entry.Disabled, "proxy": strings.TrimSpace(entry.ProxyURL) != ""}
}

func (p *CommandCodePlugin) keyPoolView() []map[string]any {
	out := make([]map[string]any, 0)
	for _, entry := range configuredKeys(p.cfg) {
		out = append(out, keyView(entry))
	}
	return out
}

func (p *CommandCodePlugin) accountView(ctx context.Context, entry APIKeyEntry, refresh bool) map[string]any {
	if entry.Disabled {
		return map[string]any{"disabled": true}
	}
	cacheKey := configRevision(p.cfg) + ":" + credentialID(entry.Key)
	p.accountMu.Lock()
	if p.accountSlots == nil {
		p.accountSlots = make(chan struct{}, 3)
	}
	slots := p.accountSlots
	if cached, ok := p.accountCache[cacheKey]; !refresh && ok && time.Since(cached.at) < statusCacheTTL {
		p.accountMu.Unlock()
		return cached.account
	}
	p.accountMu.Unlock()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return map[string]any{"error": ctx.Err().Error()}
	}
	// Recheck after waiting for another account request to finish.
	p.accountMu.Lock()
	if cached, ok := p.accountCache[cacheKey]; !refresh && ok && time.Since(cached.at) < statusCacheTTL {
		p.accountMu.Unlock()
		return cached.account
	}
	p.accountMu.Unlock()
	client := managementHTTPClient(ctx)
	if strings.TrimSpace(entry.ProxyURL) != "" {
		transport, err := proxyTransport(entry.ProxyURL)
		if err != nil {
			return map[string]any{"error": redactForPanel(err.Error(), entry)}
		}
		if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
			defer closer.CloseIdleConnections()
		}
		client = directHTTPClient{client: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
	}
	if client == nil {
		return map[string]any{"error": "current management HTTP callback is unavailable"}
	}
	out := map[string]any{}
	for _, endpoint := range []struct{ field, path string }{{"whoami", "/alpha/whoami?limits=1"}, {"credits", "/alpha/billing/credits"}, {"subscription", "/alpha/billing/subscriptions"}} {
		if data, err := p.accountGet(ctx, client, entry.Key, endpoint.path); err == nil {
			out[endpoint.field] = data
		} else {
			out[endpoint.field+"_error"] = redactForPanel(err.Error(), entry)
		}
	}
	p.accountMu.Lock()
	if p.accountCache == nil {
		p.accountCache = make(map[string]accountCacheEntry)
	}
	p.accountCache[cacheKey] = accountCacheEntry{at: time.Now(), account: out}
	p.accountMu.Unlock()
	return out
}

// directHTTPClient is used only for explicitly configured per-key proxy routes.
type directHTTPClient struct{ client *http.Client }

func (c directHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	httpReq.Header = req.Headers.Clone()
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header, Body: body}, err
}

func (c directHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("account client does not support streams")
}

func (p *CommandCodePlugin) accountGet(ctx context.Context, client pluginapi.HostHTTPClient, key, path string) (map[string]any, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Accept", "application/json")
	headers.Set("Accept-Encoding", "identity")
	headers.Set("x-command-code-version", p.cfg.cliVersion())
	headers.Set("x-cli-environment", "production")
	headers.Set("User-Agent", p.cfg.cliUserAgent())
	resp, err := client.Do(reqCtx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: p.cfg.cliBaseURL() + path, Headers: headers})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForPanel(string(resp.Body), 160))
	}
	var decoded map[string]any
	if json.Unmarshal(resp.Body, &decoded) != nil {
		return nil, fmt.Errorf("invalid json")
	}
	// Account endpoints should never echo credentials, but do not trust their payloads.
	safe, _ := json.Marshal(decoded)
	safe = bytes.ReplaceAll(safe, []byte(key), []byte("[redacted]"))
	_ = json.Unmarshal(safe, &decoded)
	return decoded, nil
}

func redactForPanel(text string, entries ...APIKeyEntry) string {
	for _, entry := range entries {
		if entry.Key != "" {
			text = strings.ReplaceAll(text, entry.Key, "[redacted]")
		}
		if entry.ProxyURL != "" {
			text = strings.ReplaceAll(text, entry.ProxyURL, safeProxyURL(entry.ProxyURL))
			if parsed, err := url.Parse(entry.ProxyURL); err == nil && parsed.User != nil {
				if password, ok := parsed.User.Password(); ok && password != "" {
					text = strings.ReplaceAll(text, password, "[redacted]")
				}
			}
		}
	}
	return text
}

func managementJSON(status int, payload any) pluginapi.ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		return pluginapi.ManagementResponse{StatusCode: 500, Headers: jsonHeaders(), Body: []byte(`{"error":"marshal failed"}`)}
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: jsonHeaders(), Body: body}
}

func managementError(status int, message string) pluginapi.ManagementResponse {
	return managementJSON(status, map[string]any{"error": message})
}

func jsonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return h
}

// ManagementEnvelope encodes a management response for the ABI wire.
//
// The host decodes the envelope result into pluginapi.ManagementResponse, which
// declares no JSON tags, so the payload must carry the Go field names
// (StatusCode/Headers/Body). A hand-written snake_case mirror is silently
// rejected by encoding/json and turns every 4xx/5xx into HTTP 200.
func ManagementEnvelope(response pluginapi.ManagementResponse) ([]byte, error) {
	status := response.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	result, err := json.Marshal(pluginapi.ManagementResponse{StatusCode: status, Headers: response.Headers, Body: response.Body})
	if err != nil {
		return nil, err
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
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

func maskCredential(key string) string {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) <= 8 {
		return "…"
	}
	return trimmed[:3] + "…" + trimmed[len(trimmed)-4:]
}

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
