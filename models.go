package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ModelProvider contributes the commandcode model list to the host registry.
// The ABI only offers static + per-auth discovery (no live /v1/models crawl:
// StaticModels has no HTTPClient), so the list is derived from the same
// configuration that drives routing, keeping the two in step automatically.
type ModelProvider struct {
	cfg *pluginConfig
}

func NewModelProvider(cfg *pluginConfig) *ModelProvider { return &ModelProvider{cfg: cfg} }

// modelDef is the registry-facing shape of one claimed model.
type modelDef struct {
	id          string
	displayName string
}

// registryModels builds the advertised list from configuration.
//
// Namespaced upstream IDs keep an executor registration anchor when a native
// provider owns a matching client alias. Bare aliases are advertised as well,
// so clients can discover the configured names through /v1/models.
func (p *ModelProvider) registryModels() []modelDef {
	entries := p.cfg.effectiveModels()
	defs := make([]modelDef, 0, 2*len(entries))
	seen := make(map[string]bool, 2*len(entries))
	add := func(id, label string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		defs = append(defs, modelDef{id: id, displayName: label + " via CommandCode"})
	}
	for _, entry := range entries {
		// Prefer the upstream name for the ID: it is the name the vendor
		// actually serves, so the advertised model stays meaningful across
		// alias changes. Fall back to the alias when no upstream is declared.
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Alias)
		}
		if name == "" {
			continue
		}
		add(Provider+"/"+name, entry.label())
	}
	for _, entry := range entries {
		add(strings.TrimSpace(entry.Alias), entry.label())
	}
	return defs
}

func (p *ModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: Provider, Models: p.models()}, nil
}

func (p *ModelProvider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: Provider, Models: p.models()}, nil
}

func (p *ModelProvider) models() []pluginapi.ModelInfo {
	defs := p.registryModels()
	models := make([]pluginapi.ModelInfo, 0, len(defs))
	for _, def := range defs {
		models = append(models, pluginapi.ModelInfo{
			ID:                         def.id,
			Object:                     "model",
			OwnedBy:                    "commandcode",
			Type:                       "chat",
			DisplayName:                def.displayName,
			Name:                       def.id,
			Description:                def.displayName,
			SupportedGenerationMethods: []string{"chatCompletions"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "top_p", "max_tokens", "stop", "tools", "reasoning_effort"},
			Thinking: &pluginapi.ThinkingSupport{
				DynamicAllowed: true,
				Levels:         []string{"none", "auto", "low", "medium", "high", "max"},
			},
		})
	}
	return models
}
