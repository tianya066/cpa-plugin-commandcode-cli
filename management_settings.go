package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// The editor owns only these fields. Host PATCH preserves all other plugin data.
type settingsInput struct {
	Revision      string             `json:"revision"`
	Transport     string             `json:"transport"`
	CLIVersion    string             `json:"cli_version"`
	CLIBaseURL    string             `json:"cli_base_url"`
	CLIWorkingDir string             `json:"cli_working_dir"`
	CLIUserAgent  string             `json:"cli_user_agent"`
	BaseURL       string             `json:"base_url"`
	Priority      int                `json:"priority"`
	Keys          []settingsKeyInput `json:"keys"`
	Models        []ModelEntry       `json:"models"`
}

type settingsKeyInput struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Weight     int     `json:"weight"`
	Disabled   bool    `json:"disabled"`
	Key        string  `json:"key,omitempty"`
	ProxyURL   *string `json:"proxy_url,omitempty"`
	ClearProxy bool    `json:"clear_proxy,omitempty"`
}

func editableConfig(cfg *pluginConfig) map[string]any {
	keys := make([]map[string]any, 0)
	for _, entry := range configuredKeys(cfg) {
		keys = append(keys, map[string]any{"key": strings.TrimSpace(entry.Key), "name": strings.TrimSpace(entry.Name), "weight": entry.normWeight(), "disabled": entry.Disabled, "proxy_url": strings.TrimSpace(entry.ProxyURL)})
	}
	models := make([]ModelEntry, 0)
	for _, entry := range cfg.effectiveModels() {
		models = append(models, ModelEntry{Alias: strings.TrimSpace(entry.Alias), Name: firstNonEmpty(strings.TrimSpace(entry.Name), strings.TrimSpace(entry.Alias)), DisplayName: strings.TrimSpace(entry.DisplayName)})
	}
	return map[string]any{
		"transport": cfg.transportMode(), "cli_version": cfg.cliVersion(), "cli_base_url": cfg.cliBaseURL(),
		"cli_working_dir": cfg.cliWorkingDir(), "cli_user_agent": cfg.cliUserAgent(), "base_url": cfg.baseURL(), "priority": cfg.Priority,
		"api_keys": keys, "models": models,
	}
}

func configRevision(cfg *pluginConfig) string {
	data, _ := json.Marshal(editableConfig(cfg))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func safeProxyURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[configured]"
	}
	parsed.User = nil
	return parsed.String()
}

func (p *CommandCodePlugin) managementSettings(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	_, cfg, err := p.readSavedSettings(ctx, req.Headers)
	if err != nil {
		return settingsFailure(err), nil
	}
	out := editableConfig(cfg)
	delete(out, "api_keys")
	keys := make([]map[string]any, 0)
	for _, entry := range configuredKeys(cfg) {
		row := keyView(entry)
		row["proxy_url"] = safeProxyURL(entry.ProxyURL)
		keys = append(keys, row)
	}
	out["keys"] = keys
	out["revision"] = configRevision(cfg)
	out["runtime_revision"] = configRevision(p.cfg)
	return managementJSON(http.StatusOK, out), nil
}

func (p *CommandCodePlugin) saveManagementSettings(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	var input settingsInput
	if len(req.Body) > 1<<20 {
		return managementError(400, "settings request is too large"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return managementError(400, "invalid settings JSON"), nil
	}
	if decoder.Decode(new(any)) != io.EOF {
		return managementError(400, "invalid settings JSON"), nil
	}
	// Serializes the read/revision check/PATCH sequence for this plugin instance.
	p.settingsMu.Lock()
	defer p.settingsMu.Unlock()
	_, current, err := p.readSavedSettings(ctx, req.Headers)
	if err != nil {
		return settingsFailure(err), nil
	}
	if input.Revision == "" {
		return managementError(400, "revision is required"), nil
	}
	if input.Revision != configRevision(current) {
		return managementError(409, "settings changed; reload before saving"), nil
	}
	next, err := validateSettings(input, current)
	if err != nil {
		return managementError(400, err.Error()), nil
	}
	patch := editableConfig(next)
	patch["api_key"] = nil // migrate the legacy single-key form into the managed pool
	data, _ := json.Marshal(patch)
	if _, err := p.hostSettingsRequest(ctx, req.Headers, http.MethodPatch, data); err != nil {
		return settingsFailure(err), nil
	}
	// Host reload is asynchronous. Waiting for it inside management.handle deadlocks.
	return managementJSON(http.StatusOK, map[string]any{"saved": true, "revision": configRevision(next)}), nil
}

func validateSettings(input settingsInput, current *pluginConfig) (*pluginConfig, error) {
	transport := strings.ToLower(strings.TrimSpace(input.Transport))
	if transport != "provider" && transport != "cli" && transport != "auto" {
		return nil, fmt.Errorf("transport must be provider, cli or auto")
	}
	for _, field := range []struct{ name, value string }{{"base_url", input.BaseURL}, {"cli_base_url", input.CLIBaseURL}} {
		if strings.TrimSpace(field.value) != "" && !validEndpointURL(field.value, false) {
			return nil, fmt.Errorf("invalid %s", field.name)
		}
	}
	if strings.ContainsAny(input.CLIVersion+input.CLIUserAgent, "\r\n") {
		return nil, fmt.Errorf("invalid CLI header value")
	}
	if len(input.Keys) == 0 || len(input.Keys) > 100 {
		return nil, fmt.Errorf("configure between 1 and 100 keys")
	}
	old := make(map[string]APIKeyEntry)
	for _, entry := range configuredKeys(current) {
		old[credentialID(entry.Key)] = entry
	}
	seenKeys, seenIDs := map[string]bool{}, map[string]bool{}
	keys := make([]APIKeyEntry, 0, len(input.Keys))
	enabled := 0
	for index, row := range input.Keys {
		var entry APIKeyEntry
		if row.ID != "" {
			var found bool
			entry, found = old[row.ID]
			if !found {
				return nil, fmt.Errorf("key %d has an unknown id", index+1)
			}
			if seenIDs[row.ID] {
				return nil, fmt.Errorf("key %d has a duplicate id", index+1)
			}
			seenIDs[row.ID] = true
		}
		if key := strings.TrimSpace(row.Key); key != "" {
			if strings.ContainsAny(key, "*…") || strings.Contains(key, "...") || strings.Contains(strings.ToLower(key), "redacted") || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
				return nil, fmt.Errorf("key %d must be a full credential, not a mask", index+1)
			}
			entry.Key = key
		}
		if strings.TrimSpace(entry.Key) == "" {
			return nil, fmt.Errorf("key %d requires a credential", index+1)
		}
		if seenKeys[entry.Key] {
			return nil, fmt.Errorf("duplicate credential")
		}
		seenKeys[entry.Key] = true
		if row.Weight < 1 || row.Weight > 1000000 {
			return nil, fmt.Errorf("key %d weight must be between 1 and 1000000", index+1)
		}
		entry.Name, entry.Weight, entry.Disabled = strings.TrimSpace(row.Name), row.Weight, row.Disabled
		if row.ClearProxy {
			if row.ProxyURL != nil && strings.TrimSpace(*row.ProxyURL) != "" {
				return nil, fmt.Errorf("key %d cannot set and clear proxy together", index+1)
			}
			entry.ProxyURL = ""
		} else if row.ProxyURL != nil && strings.TrimSpace(*row.ProxyURL) != "" {
			value := strings.TrimSpace(*row.ProxyURL)
			// A displayed credential-free proxy URL round-trips without erasing its password.
			if value != safeProxyURL(entry.ProxyURL) || entry.ProxyURL == "" {
				if !validEndpointURL(value, true) {
					return nil, fmt.Errorf("key %d has an invalid proxy URL", index+1)
				}
				entry.ProxyURL = value
			}
		}
		keys = append(keys, entry)
		if !entry.Disabled {
			enabled++
		}
	}
	if enabled == 0 {
		return nil, fmt.Errorf("at least one key must be enabled")
	}
	if len(input.Models) == 0 || len(input.Models) > 300 {
		return nil, fmt.Errorf("configure between 1 and 300 models")
	}
	seenModels := map[string]bool{}
	models := make([]ModelEntry, 0, len(input.Models))
	for _, entry := range input.Models {
		entry.Alias, entry.Name, entry.DisplayName = strings.TrimSpace(entry.Alias), strings.TrimSpace(entry.Name), strings.TrimSpace(entry.DisplayName)
		if entry.Alias == "" || entry.Name == "" {
			return nil, fmt.Errorf("each model requires alias and name")
		}
		alias := normalizeModel(entry.Alias)
		if alias == "" || seenModels[alias] {
			return nil, fmt.Errorf("model aliases must be unique")
		}
		seenModels[alias] = true
		models = append(models, entry)
	}
	next := *current
	next.APIKey, next.APIKeys = "", keys
	next.Models, next.Transport, next.Priority = models, transport, input.Priority
	next.CLIVersion, next.CLIUserAgent = strings.TrimSpace(input.CLIVersion), strings.TrimSpace(input.CLIUserAgent)
	next.BaseURL, next.CLIBaseURL, next.CLIWorkingDir = strings.TrimSpace(input.BaseURL), strings.TrimSpace(input.CLIBaseURL), strings.TrimSpace(input.CLIWorkingDir)
	next.buildIndexes()
	return &next, nil
}

func validEndpointURL(raw string, proxy bool) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if strings.ContainsAny(u.Host, " \t\r\n") {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return false
		}
	}
	if proxy {
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			return false
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
			return true
		default:
			return false
		}
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == ""
}

func (p *CommandCodePlugin) settingsEndpoint() (string, error) {
	base := strings.TrimSpace(p.cfg.ManagementBaseURL)
	if base == "" {
		base = "http://127.0.0.1:8317"
	}
	parsed, err := url.Parse(base)
	if err != nil || !validEndpointURL(base, false) {
		return "", fmt.Errorf("management_base_url must be a loopback HTTP URL")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("management_base_url must use a loopback IP address")
	}
	return strings.TrimRight(base, "/") + "/v8/management/config/plugins/configs/commandcode", nil
}

type settingsHTTPError struct{ status int }

func (e settingsHTTPError) Error() string {
	return fmt.Sprintf("CPA settings API returned HTTP %d", e.status)
}

func settingsFailure(err error) pluginapi.ManagementResponse {
	status := http.StatusBadGateway
	var upstream settingsHTTPError
	if errors.As(err, &upstream) && (upstream.status == 401 || upstream.status == 403) {
		status = upstream.status
	}
	return managementError(status, err.Error())
}

func (p *CommandCodePlugin) hostSettingsRequest(ctx context.Context, headers http.Header, method string, body []byte) ([]byte, error) {
	endpoint, err := p.settingsEndpoint()
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot create settings request")
	}
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "X-Management-Key") {
			req.Header[key] = append([]string(nil), values...)
		}
	}
	if req.Header.Get("Authorization") == "" && req.Header.Get("X-Management-Key") == "" {
		// Header maps coming from an ABI need not be canonicalized.
		found := false
		for key, values := range req.Header {
			if (strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "X-Management-Key")) && len(values) > 0 && values[0] != "" {
				found = true
			}
		}
		if !found {
			return nil, settingsHTTPError{status: 401}
		}
	}
	req.Header.Set("Content-Type", "application/json")
	// Never follow a redirect carrying management credentials or use a proxy here.
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CPA settings API is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, settingsHTTPError{status: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("cannot read CPA settings response")
	}
	return data, nil
}

func (p *CommandCodePlugin) readSavedSettings(ctx context.Context, headers http.Header) (map[string]any, *pluginConfig, error) {
	data, err := p.hostSettingsRequest(ctx, headers, http.MethodGet, nil)
	if err != nil {
		return nil, nil, err
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, nil, fmt.Errorf("CPA settings API returned an invalid object")
	}
	raw, err := yaml.Marshal(object)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot decode saved settings")
	}
	return object, parseConfig(raw), nil
}

func (p *CommandCodePlugin) managementTest(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	var input struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if json.Unmarshal(req.Body, &input) != nil || input.ID == "" {
		return managementError(400, "id and a valid JSON body are required"), nil
	}
	_, saved, err := p.readSavedSettings(ctx, req.Headers)
	if err != nil {
		return settingsFailure(err), nil
	}
	var selected *APIKeyEntry
	for _, entry := range configuredKeys(saved) {
		if credentialID(entry.Key) == input.ID {
			value := entry
			selected = &value
			break
		}
	}
	if selected == nil {
		return managementError(400, "unknown key id"), nil
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = saved.effectiveModels()[0].Alias
	}
	found := false
	for _, entry := range saved.effectiveModels() {
		if model == entry.Alias {
			found = true
			break
		}
	}
	if !found {
		return managementError(400, "unknown model alias"), nil
	}
	selected.Disabled = false
	saved.APIKey, saved.APIKeys = "", []APIKeyEntry{*selected}
	executor := NewExecutor(saved, nil)
	payload, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply exactly OK"}}, "max_tokens": 128, "stream": false})
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	response, testErr := executor.Execute(testCtx, pluginapi.ExecutorRequest{Model: model, Payload: payload, HTTPClient: managementHTTPClient(ctx)})
	if testCtx.Err() != nil {
		testErr = testCtx.Err()
	}
	status := http.StatusOK
	var text string
	if testErr == nil {
		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(response.Payload, &completion) != nil || len(completion.Choices) == 0 {
			testErr = fmt.Errorf("upstream returned an invalid completion")
		} else {
			text = completion.Choices[0].Message.Content
		}
		if testErr == nil && strings.TrimSpace(text) == "" {
			testErr = fmt.Errorf("upstream returned no text")
		}
	}
	message := ""
	if testErr != nil {
		status = http.StatusBadGateway
		if provider, ok := testErr.(interface{ StatusCode() int }); ok && provider.StatusCode() > 0 {
			status = provider.StatusCode()
		}
		if errors.Is(testErr, context.DeadlineExceeded) || errors.Is(testCtx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		message = truncateForPanel(redactForPanel(testErr.Error(), configuredKeys(saved)...), 1500)
	}
	return managementJSON(http.StatusOK, map[string]any{"ok": testErr == nil, "status": status, "model": model, "seconds": time.Since(start).Seconds(), "text": truncateForPanel(redactForPanel(text, *selected), 1000), "error": message}), nil
}
