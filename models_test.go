package plugin

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelDiscoveryAdvertisesAliasesAndNamespacedAnchors(t *testing.T) {
	cfg := parseConfig([]byte(`models:
  - alias: cc-fast
    name: vendor/fast-v2
    display_name: Fast
  - alias: cc-fast-backup
    name: vendor/fast-v2
  - alias: cc-reason
    name: vendor/reason-v1
`))
	provider := NewModelProvider(cfg)
	static, err := provider.StaticModels(t.Context(), pluginapi.StaticModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := provider.ModelsForAuth(t.Context(), pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(static, auth) {
		t.Fatal("static and per-auth discovery must agree")
	}
	var ids []string
	for _, model := range static.Models {
		ids = append(ids, model.ID)
		if model.ID != model.Name || model.OwnedBy != Provider || model.Thinking == nil {
			t.Fatalf("incomplete advertised model: %+v", model)
		}
		// Every advertised spelling must still be routable without changing
		// the established alias/upstream routing rules.
		route, routeErr := NewRouter(cfg).RouteModel(t.Context(), requestWithModel(model.ID))
		if routeErr != nil || !route.Handled {
			t.Fatalf("advertised model %q cannot route", model.ID)
		}
	}
	want := []string{"commandcode/vendor/fast-v2", "commandcode/vendor/reason-v1", "cc-fast", "cc-fast-backup", "cc-reason"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("advertised IDs = %v, want %v", ids, want)
	}
	if static.Models[0].DisplayName != "Fast via CommandCode" || static.Models[2].DisplayName != "Fast via CommandCode" {
		t.Fatal("configured display name missing")
	}
}

func TestModelDiscoveryDeduplicatesAliasAnchorAndLegacyEntries(t *testing.T) {
	cfg := &pluginConfig{Models: []ModelEntry{
		{Alias: "legacy"},
		{Alias: "legacy", Name: "legacy"},
		{Alias: "commandcode/legacy", Name: "legacy"},
		{Alias: "  ", Name: "  "},
	}}
	response, err := NewModelProvider(cfg).StaticModels(t.Context(), pluginapi.StaticModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, model := range response.Models {
		ids = append(ids, model.ID)
	}
	if !reflect.DeepEqual(ids, []string{"commandcode/legacy", "legacy"}) {
		t.Fatalf("duplicate or empty public model IDs: %v", ids)
	}
}
