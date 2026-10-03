//go:build cgo

// This file exercises the c-shared entrypoint, which only builds with cgo
// enabled. Without the tag the package would compile on CGO_ENABLED=0 while
// abi.go (which imports "C") is excluded, and every reference here would fail.
package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	plug "github.com/ahoo/cpa-plugin-commandcode"
)

// The host decodes the management.handle envelope result into
// pluginapi.ManagementResponse, which declares no JSON tags. A hand-written
// snake_case mirror is silently accepted by encoding/json and every 4xx/5xx
// then reaches clients as HTTP 200, breaking the panel's 401/409 handling.
// This exercises the call site's encoder, not just the library helper.
func TestManagementHandleEnvelopeCarriesStatusCode(t *testing.T) {
	raw, err := plug.ManagementEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusConflict,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       []byte(`{"error":"settings changed; reload before saving"}`),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %s", raw)
	}
	var decoded pluginapi.ManagementResponse
	if err := json.Unmarshal(envelope.Result, &decoded); err != nil {
		t.Fatalf("decode into the host's type: %v", err)
	}
	if decoded.StatusCode != http.StatusConflict {
		t.Fatalf("host would answer HTTP %d instead of 409; wire form: %s", decoded.StatusCode, envelope.Result)
	}
	if string(decoded.Body) != `{"error":"settings changed; reload before saving"}` {
		t.Fatalf("body round-tripped as %q", decoded.Body)
	}
	if decoded.Headers.Get("Content-Type") == "" {
		t.Fatalf("headers lost: %v", decoded.Headers)
	}
}

// schema_version < 6 makes the v8 host HTML-entity-escape every management
// response string, which mangles upstream error bodies and appends another
// escaping level to user strings on each save/load cycle.
func TestRegistrationReportsRawManagementSchemaVersion(t *testing.T) {
	if abiSchemaVersion < 6 {
		t.Fatalf("abiSchemaVersion = %d; the host escapes management JSON below 6", abiSchemaVersion)
	}
	if abiSchemaVersion > pluginabi.SchemaVersion+2 {
		t.Fatalf("abiSchemaVersion = %d looks accidental; the host rejects versions above its own", abiSchemaVersion)
	}
}

// The host only wires a capability when the corresponding wire flag arrives,
// so a capability implemented in the library but not exported here is dead code.
func TestRegistrationExportsDeclaredCapabilities(t *testing.T) {
	built, p := plug.Build(nil)
	if p == nil {
		t.Fatal("Build returned no plugin")
	}
	registration := abiRegistration{
		SchemaVersion: abiSchemaVersion,
		Metadata:      built.Metadata,
		Capabilities: abiCapabilities{
			ModelProvider:      built.Capabilities.ModelProvider != nil,
			ModelRouter:        built.Capabilities.ModelRouter != nil,
			Executor:           built.Capabilities.Executor != nil,
			RequestTranslator:  built.Capabilities.RequestTranslator != nil,
			ResponseTranslator: built.Capabilities.ResponseTranslator != nil,
			UsagePlugin:        built.Capabilities.UsagePlugin != nil,
			ManagementAPI:      built.Capabilities.ManagementAPI != nil,
		},
	}
	raw, err := json.Marshal(registration)
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}
	var wire struct {
		SchemaVersion uint32 `json:"schema_version"`
		Capabilities  map[string]any
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal registration: %v", err)
	}
	if wire.SchemaVersion != abiSchemaVersion {
		t.Fatalf("wire schema_version = %d, want %d", wire.SchemaVersion, abiSchemaVersion)
	}

	for _, capability := range []string{"model_provider", "model_router", "executor", "request_translator", "response_translator", "usage_plugin", "management_api"} {
		if enabled, _ := wire.Capabilities[capability].(bool); !enabled {
			t.Fatalf("capability %q is implemented but not exported on the wire: %s", capability, raw)
		}
	}
}

// Every method the host may call for an exported capability must be dispatched;
// otherwise the plugin answers "unknown method" and the capability is dead.
func TestExportedCapabilitiesHaveDispatchCases(t *testing.T) {
	// handleABIMethod resolves the plugin instance first, so register before
	// probing dispatch; otherwise every case answers "not registered".
	if _, err := handleABIMethod(t.Context(), pluginabi.MethodPluginRegister, []byte(`{}`)); err != nil {
		t.Fatalf("plugin.register: %v", err)
	}
	if _, err := handleABIMethod(t.Context(), pluginabi.MethodUsageHandle, []byte(`{"Model":"m"}`)); err != nil {
		t.Fatalf("usage.handle is exported but not dispatched: %v", err)
	}
	for _, method := range []string{pluginabi.MethodManagementRegister, pluginabi.MethodManagementHandle} {
		if _, err := handleABIMethod(t.Context(), method, []byte(`{}`)); err != nil {
			t.Fatalf("%s dispatch failed: %v", method, err)
		}
	}
}
