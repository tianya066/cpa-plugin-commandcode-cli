package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func managementPlugin(cfg *pluginConfig) *CommandCodePlugin {
	cfg.buildIndexes()
	return &CommandCodePlugin{cfg: cfg, executor: NewExecutor(cfg, nil)}
}

func settingsInputFor(cfg *pluginConfig) settingsInput {
	editable := editableConfig(cfg)
	raw, _ := json.Marshal(editable)
	var input settingsInput
	_ = json.Unmarshal(raw, &input)
	input.Revision = configRevision(cfg)
	for _, key := range configuredKeys(cfg) {
		input.Keys = append(input.Keys, settingsKeyInput{ID: credentialID(key.Key), Name: key.Name, Weight: key.normWeight(), Disabled: key.Disabled})
	}
	return input
}

func baseManagementConfig() *pluginConfig {
	return &pluginConfig{Transport: "provider", APIKeys: []APIKeyEntry{{Key: "user-secret-first", Weight: 1, Name: "first", ProxyURL: "http://alice:proxy-password@127.0.0.1:9898"}, {Key: "user-secret-second", Weight: 2, Name: "second"}}}
}

func mockSettingsAPI(t *testing.T, cfg *pluginConfig) (*httptest.Server, *atomic.Int32, func() map[string]any) {
	t.Helper()
	raw, _ := yaml.Marshal(cfg)
	var state map[string]any
	_ = yaml.Unmarshal(raw, &state)
	state["store"] = map[string]any{"opaque": "must-survive"}
	var mu sync.Mutex
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v8/management/config/plugins/configs/commandcode" {
			http.Error(w, "wrong endpoint", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer management-test" && r.Header.Get("X-Management-Key") != "management-test" {
			http.Error(w, "unauthorized", 401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPatch {
			var update map[string]any
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Errorf("patch decode: %v", err)
			}
			if _, found := update["store"]; found {
				t.Error("patch must contain only owned fields")
			}
			for key, value := range update {
				if value == nil {
					delete(state, key)
				} else {
					state[key] = value
				}
			}
			patches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	snapshot := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		raw, _ := json.Marshal(state)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	t.Cleanup(server.Close)
	return server, &patches, snapshot
}

func settingsRequest(method string, input any) pluginapi.ManagementRequest {
	body, _ := json.Marshal(input)
	return pluginapi.ManagementRequest{Method: method, Path: "/commandcode/settings", Headers: http.Header{"Authorization": []string{"Bearer management-test"}}, Body: body}
}

func TestManagementSettingsPreservesMaskedCredentialsAndUnknownFields(t *testing.T) {
	cfg := baseManagementConfig()
	server, patches, snapshot := mockSettingsAPI(t, cfg)
	cfg.ManagementBaseURL = server.URL
	p := managementPlugin(cfg)
	response, _ := p.HandleManagement(context.Background(), settingsRequest(http.MethodGet, nil))
	if response.StatusCode != 200 {
		t.Fatalf("GET: %s", response.Body)
	}
	for _, secret := range []string{"user-secret-first", "user-secret-second", "proxy-password", "must-survive"} {
		if strings.Contains(string(response.Body), secret) {
			t.Fatalf("GET leaked %s", secret)
		}
	}
	var view map[string]any
	_ = json.Unmarshal(response.Body, &view)
	if view["revision"] != view["runtime_revision"] {
		t.Fatal("persisted and runtime revisions should initially match")
	}
	input := settingsInputFor(cfg)
	input.Keys[0].Name = "renamed"
	displayedProxy := safeProxyURL(cfg.APIKeys[0].ProxyURL)
	input.Keys[0].ProxyURL = &displayedProxy
	response, _ = p.HandleManagement(context.Background(), settingsRequest(http.MethodPost, input))
	if response.StatusCode != 200 {
		t.Fatalf("POST: %s", response.Body)
	}
	if patches.Load() != 1 {
		t.Fatal("expected one patch")
	}
	state := snapshot()
	if state["store"].(map[string]any)["opaque"] != "must-survive" {
		t.Fatal("unknown store was modified")
	}
	entries := state["api_keys"].([]any)
	if entries[0].(map[string]any)["key"] != cfg.APIKeys[0].Key || entries[0].(map[string]any)["proxy_url"] != cfg.APIKeys[0].ProxyURL {
		t.Fatal("empty credential or displayed proxy overwrote a secret")
	}
	if len(entries) != 2 {
		t.Fatal("array replacement lost a key")
	}
	var saved map[string]any
	_ = json.Unmarshal(response.Body, &saved)
	after, _ := p.HandleManagement(context.Background(), settingsRequest(http.MethodGet, nil))
	_ = json.Unmarshal(after.Body, &view)
	if view["revision"] != saved["revision"] || view["runtime_revision"] == saved["revision"] {
		t.Fatal("saved revision must reflect pending runtime reload")
	}
	raw, _ := yaml.Marshal(state)
	reconfigured := managementPlugin(parseConfig(raw))
	if configRevision(reconfigured.cfg) != saved["revision"] {
		t.Fatal("reconfigure revision did not match expected save revision")
	}
	conflict, _ := p.HandleManagement(context.Background(), settingsRequest(http.MethodPost, input))
	if conflict.StatusCode != 409 || patches.Load() != 1 {
		t.Fatalf("stale revision must not patch: %d", conflict.StatusCode)
	}
}

func TestManagementSettingsRejectsInvalidInput(t *testing.T) {
	cfg := baseManagementConfig()
	cases := map[string]func(*settingsInput){
		"mask":            func(in *settingsInput) { in.Keys[0].Key = maskCredential(cfg.APIKeys[0].Key) },
		"unknown-id":      func(in *settingsInput) { in.Keys[0].ID = "unknown" },
		"duplicate-id":    func(in *settingsInput) { in.Keys[1].ID = in.Keys[0].ID },
		"duplicate-key":   func(in *settingsInput) { in.Keys[1].Key = cfg.APIKeys[0].Key },
		"new-empty":       func(in *settingsInput) { in.Keys[0].ID = "" },
		"invalid-url":     func(in *settingsInput) { in.BaseURL = "file:///etc/passwd" },
		"url-credentials": func(in *settingsInput) { in.CLIBaseURL = "https://user:secret@example.com" },
		"invalid-proxy":   func(in *settingsInput) { value := "ftp://127.0.0.1"; in.Keys[0].ProxyURL = &value },
		"disabled-all": func(in *settingsInput) {
			for i := range in.Keys {
				in.Keys[i].Disabled = true
			}
		},
		"bad-weight":       func(in *settingsInput) { in.Keys[0].Weight = -1 },
		"empty-model":      func(in *settingsInput) { in.Models[0].Name = "" },
		"duplicate-alias":  func(in *settingsInput) { in.Models[1].Alias = in.Models[0].Alias },
		"header-injection": func(in *settingsInput) { in.CLIUserAgent = "user\r\nx: y" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := settingsInputFor(cfg)
			mutate(&input)
			if _, err := validateSettings(input, cfg); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestManagementSettingsLoopbackAndMethods(t *testing.T) {
	for _, base := range []string{"http://example.com:8317", "http://127.0.0.1.evil.test:8317", "http://localhost:8317", "http://127.0.0.1:8317/other", "http://u:p@127.0.0.1:8317"} {
		p := managementPlugin(&pluginConfig{ManagementBaseURL: base})
		if _, err := p.settingsEndpoint(); err == nil {
			t.Errorf("accepted non-loopback endpoint %s", base)
		}
	}
	p := managementPlugin(&pluginConfig{})
	for _, path := range []string{"/commandcode/status", "/commandcode/settings", "/commandcode/test", "/index.html"} {
		response, _ := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodDelete, Path: path})
		if response.StatusCode != 405 {
			t.Errorf("%s allowed DELETE", path)
		}
	}
	registration, _ := p.RegisterManagement(context.Background(), pluginapi.ManagementRegistrationRequest{})
	if len(registration.Routes) != 4 {
		t.Fatal("expected explicit GET/POST route registrations")
	}
}

type accountTestClient struct {
	calls  atomic.Int32
	active atomic.Int32
	peak   atomic.Int32
	mu     sync.Mutex
	keys   map[string]int
	marker string
	fail   string
}

func (c *accountTestClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		return pluginapi.HTTPResponse{}, fmt.Errorf("missing deadline")
	}
	c.calls.Add(1)
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		old := c.peak.Load()
		if active <= old || c.peak.CompareAndSwap(old, active) {
			break
		}
	}
	time.Sleep(3 * time.Millisecond)
	key := req.Headers.Get("Authorization")
	c.mu.Lock()
	if c.keys == nil {
		c.keys = make(map[string]int)
	}
	c.keys[key]++
	c.mu.Unlock()
	if c.fail != "" && strings.Contains(req.URL, "credits") {
		return pluginapi.HTTPResponse{StatusCode: 403, Body: []byte(c.fail)}, nil
	}
	raw, _ := json.Marshal(map[string]any{"marker": c.marker})
	return pluginapi.HTTPResponse{StatusCode: 200, Body: raw}, nil
}
func (c *accountTestClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("unexpected stream")
}

func TestManagementAccountsArePerKeyBoundedCachedAndRequestScoped(t *testing.T) {
	cfg := &pluginConfig{}
	for i := 0; i < 7; i++ {
		cfg.APIKeys = append(cfg.APIKeys, APIKeyEntry{Key: fmt.Sprintf("user-account-secret-%d", i), Weight: 1, Disabled: i == 6})
	}
	p := managementPlugin(cfg)
	a := &accountTestClient{marker: "first", fail: "partial error user-account-secret-1"}
	response, _ := p.managementStatus(WithManagementHTTPClient(context.Background(), a), false)
	if response.StatusCode != 200 || a.calls.Load() != 18 || a.peak.Load() > 3 {
		t.Fatalf("calls=%d peak=%d", a.calls.Load(), a.peak.Load())
	}
	var body struct {
		Accounts []struct {
			ID      string         `json:"id"`
			Account map[string]any `json:"account"`
		} `json:"accounts"`
	}
	_ = json.Unmarshal(response.Body, &body)
	if len(body.Accounts) != 7 || body.Accounts[6].Account["disabled"] != true {
		t.Fatal("all configured keys including disabled must be visible")
	}
	if body.Accounts[1].Account["credits_error"] == nil || body.Accounts[1].Account["whoami"] == nil {
		t.Fatal("partial failures must remain local")
	}
	b := &accountTestClient{marker: "second"}
	_, _ = p.managementStatus(WithManagementHTTPClient(context.Background(), b), false)
	if b.calls.Load() != 0 {
		t.Fatal("cache missed")
	}
	response, _ = p.managementStatus(WithManagementHTTPClient(context.Background(), b), true)
	if b.calls.Load() != 18 || a.calls.Load() != 18 {
		t.Fatal("refresh reused a previous callback")
	}
	if !strings.Contains(string(response.Body), "second") {
		t.Fatal("refresh did not replace cache")
	}
	fresh := managementPlugin(cfg)
	_, _ = fresh.managementStatus(WithManagementHTTPClient(context.Background(), b), false)
	if b.calls.Load() != 36 {
		t.Fatal("plugin instances shared cache")
	}
	changed := *cfg
	changed.CLIVersion = "new-version"
	p.cfg = &changed
	_, _ = p.managementStatus(WithManagementHTTPClient(context.Background(), b), false)
	if b.calls.Load() != 54 {
		t.Fatal("configuration revision did not invalidate cache")
	}
	p.accountMu.Lock()
	for key, entry := range p.accountCache {
		entry.at = time.Now().Add(-31 * time.Second)
		p.accountCache[key] = entry
	}
	p.accountMu.Unlock()
	_, _ = p.managementStatus(WithManagementHTTPClient(context.Background(), b), false)
	if b.calls.Load() != 72 {
		t.Fatal("expired account cache was reused")
	}
}

func TestManagementAccountProxyAndNoCallbackFallback(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer user-proxy-secret" {
			t.Error("proxy received wrong account key")
		}
		if r.Header.Get("Proxy-Authorization") == "" {
			t.Error("proxy authentication missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"proxied": true})
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("alice", "proxy-secret")
	cfg := &pluginConfig{CLIBaseURL: "http://unreachable.invalid", APIKeys: []APIKeyEntry{{Key: "user-proxy-secret", ProxyURL: proxyURL.String(), Weight: 1}}}
	p := managementPlugin(cfg)
	host := &accountTestClient{}
	response, _ := p.managementStatus(WithManagementHTTPClient(context.Background(), host), true)
	if response.StatusCode != 200 || calls.Load() != 3 || host.calls.Load() != 0 {
		t.Fatalf("per-key proxy failed: %s", response.Body)
	}
	cfg.APIKeys[0].ProxyURL = ""
	response, _ = p.managementStatus(context.Background(), true)
	if !strings.Contains(string(response.Body), "current management HTTP callback is unavailable") {
		t.Fatal("missing callback must not reuse prior host client or bypass host policy")
	}
	executorType := reflect.TypeOf(*p.executor)
	if _, exists := executorType.FieldByName("hostClient"); exists {
		t.Fatal("executor retained request-scoped HTTP client")
	}
}

func TestManagementTestUsesOnlySelectedSavedKey(t *testing.T) {
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["max_tokens"] != float64(128) {
			t.Error("test token budget is not 128")
		}
		if request["messages"].([]any)[0].(map[string]any)["content"] != "Reply exactly OK" {
			t.Error("test prompt changed")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}})
	}))
	defer upstream.Close()
	saved := baseManagementConfig()
	saved.APIKeys[0].ProxyURL = ""
	saved.BaseURL = upstream.URL
	server, _, _ := mockSettingsAPI(t, saved)
	runtime := &pluginConfig{ManagementBaseURL: server.URL, APIKey: "runtime-wrong-key"}
	p := managementPlugin(runtime)
	req := settingsRequest(http.MethodPost, map[string]any{"id": credentialID(saved.APIKeys[1].Key), "model": saved.effectiveModels()[0].Alias})
	req.Path = "/commandcode/test"
	client := directHTTPClient{client: upstream.Client()}
	response, _ := p.HandleManagement(WithManagementHTTPClient(context.Background(), client), req)
	if received != "Bearer "+saved.APIKeys[1].Key || !strings.Contains(string(response.Body), `"ok":true`) || !strings.Contains(string(response.Body), `"text":"OK"`) {
		t.Fatalf("test not selected saved key: %s", response.Body)
	}
	if strings.Contains(string(response.Body), saved.APIKeys[1].Key) {
		t.Fatal("test leaked selected credential")
	}
}

type managementFailureClient struct {
	key     string
	timeout bool
	calls   atomic.Int32
}

func (c *managementFailureClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.calls.Add(1)
	if c.timeout {
		<-ctx.Done()
		return pluginapi.HTTPResponse{}, ctx.Err()
	}
	return pluginapi.HTTPResponse{StatusCode: 403, Body: []byte(`{"error":"plan refuses credential ` + c.key + `"}`)}, nil
}
func (c *managementFailureClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("unexpected stream")
}

func TestManagementTestPreservesFailureAndDeadline(t *testing.T) {
	cfg := baseManagementConfig()
	cfg.APIKeys[0].ProxyURL = ""
	server, _, _ := mockSettingsAPI(t, cfg)
	p := managementPlugin(&pluginConfig{ManagementBaseURL: server.URL})
	req := settingsRequest(http.MethodPost, map[string]any{"id": credentialID(cfg.APIKeys[0].Key), "model": cfg.effectiveModels()[0].Alias})
	req.Path = "/commandcode/test"
	client := &managementFailureClient{key: cfg.APIKeys[0].Key}
	response, _ := p.HandleManagement(WithManagementHTTPClient(context.Background(), client), req)
	if !strings.Contains(string(response.Body), `"ok":false`) || !strings.Contains(string(response.Body), `"status":403`) || strings.Contains(string(response.Body), cfg.APIKeys[0].Key) {
		t.Fatalf("lost or leaked true upstream error: %s", response.Body)
	}
	if client.calls.Load() != 1 {
		t.Fatal("single-key test failed over to another account")
	}
	client.timeout = true
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	response, _ = p.HandleManagement(WithManagementHTTPClient(ctx, client), req)
	if !strings.Contains(string(response.Body), `"status":504`) {
		t.Fatalf("deadline was not surfaced: %s", response.Body)
	}
}

func TestManagementSettingsReplaceKeyAndClearProxy(t *testing.T) {
	cfg := baseManagementConfig()
	input := settingsInputFor(cfg)
	input.Keys[0].Key = "replacement-new-secret"
	input.Keys[0].ClearProxy = true
	next, err := validateSettings(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.APIKeys[0].Key != "replacement-new-secret" || next.APIKeys[0].ProxyURL != "" {
		t.Fatal("explicit replace/clear was not applied")
	}
	if credentialID(next.APIKeys[0].Key) == credentialID(cfg.APIKeys[0].Key) {
		t.Fatal("replacement key did not get a new stable id")
	}
	if configRevision(next) == configRevision(cfg) {
		t.Fatal("credential replacement did not alter revision")
	}
	server, patches, _ := mockSettingsAPI(t, cfg)
	cfg.ManagementBaseURL = server.URL
	p := managementPlugin(cfg)
	input = settingsInputFor(cfg)
	input.Revision = ""
	response, _ := p.HandleManagement(context.Background(), settingsRequest(http.MethodPost, input))
	if response.StatusCode != 400 || patches.Load() != 0 {
		t.Fatal("missing revision should not write")
	}
}
