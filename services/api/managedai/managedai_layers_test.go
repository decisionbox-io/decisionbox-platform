package managedai

import (
	"strings"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func managedCfg() *config {
	return &config{
		enabled:       true,
		baseURL:       "https://ai.example.com/v1",
		analysisModel: "decisionbox-analysis-model",
		blurbModel:    "decisionbox-blurb-model",
		embedModel:    "decisionbox-embed-model",
	}
}

func TestLayerAliasKeepsTheThreeProvisionedNamesAuthoritative(t *testing.T) {
	c := managedCfg()
	// A provisioned tenant already routes these; moving them to a derived
	// name would silently change behaviour.
	if got := c.layerAlias(llmlayer.LayerAnalysis); got != "decisionbox-analysis-model" {
		t.Errorf("analysis alias = %q, want the AI_GATEWAY_ANALYSIS_MODEL value", got)
	}
	if got := c.layerAlias(llmlayer.DefaultLayer); got != "decisionbox-analysis-model" {
		t.Errorf("the default row must mirror the analysis alias, got %q", got)
	}
	if got := c.layerAlias(llmlayer.LayerTableDescriptions); got != "decisionbox-blurb-model" {
		t.Errorf("table-description alias = %q, want the AI_GATEWAY_BLURB_MODEL value", got)
	}
}

func TestLayerAliasDerivesEveryOtherLayerSoNoProvisioningVariableIsAdded(t *testing.T) {
	c := managedCfg()
	cases := map[string]string{
		llmlayer.LayerExploration:        "decisionbox-exploration-model",
		llmlayer.LayerValidationVerifier: "decisionbox-validation-verifier-model",
		llmlayer.LayerValidationRefuter:  "decisionbox-validation-refuter-model",
		llmlayer.LayerAsk:                "decisionbox-ask-model",
		llmlayer.LayerSQLRepair:          "decisionbox-sql-repair-model",
		llmlayer.LayerFileIngestion:      "decisionbox-file-ingestion-model",
	}
	for layer, want := range cases {
		if got := c.layerAlias(layer); got != want {
			t.Errorf("alias for %q = %q, want %q", layer, got, want)
		}
	}
}

func TestEveryLayerGetsADistinctAlias(t *testing.T) {
	// Collisions would merge two layers' routing and their credit
	// attribution. The underscore-to-hyphen conversion is the only place a
	// collision could creep in.
	c := managedCfg()
	seen := map[string]string{}
	keys := append(llmlayer.Keys(), llmlayer.DefaultLayer)
	for _, layer := range keys {
		alias := c.layerAlias(layer)
		if alias == "" {
			t.Errorf("layer %q has no alias", layer)
			continue
		}
		// The default row and analysis share the analysis alias on purpose.
		if prev, dup := seen[alias]; dup {
			bothAnalysis := (prev == llmlayer.LayerAnalysis || prev == llmlayer.DefaultLayer) &&
				(layer == llmlayer.LayerAnalysis || layer == llmlayer.DefaultLayer)
			if !bothAnalysis {
				t.Errorf("layers %q and %q both resolve to alias %q", prev, layer, alias)
			}
		}
		seen[alias] = layer
		if strings.Contains(alias, "_") {
			t.Errorf("alias %q for layer %q contains an underscore; the convention is hyphen-separated", alias, layer)
		}
	}
}

func TestLayerAliasIsEmptyWhenManagedModeIsOff(t *testing.T) {
	c := &config{enabled: false}
	if got := c.layerAlias(llmlayer.LayerAsk); got != "" {
		t.Errorf("layerAlias = %q with managed mode off; a gateway alias must never be stamped onto a self-hosted project", got)
	}
}

func TestApplyFillsEveryLayerAndStampsTheVersion(t *testing.T) {
	c := managedCfg()
	p := &models.Project{}
	c.apply(p)

	if p.LLMConfigVersion != llmlayer.ConfigVersion {
		t.Fatalf("llm_config_version = %d, want %d — a cloud project is configured by construction and must never be gated", p.LLMConfigVersion, llmlayer.ConfigVersion)
	}
	if len(p.LLMProviders) != 1 || p.LLMProviders[0].ID != llmlayer.DefaultProviderID {
		t.Fatalf("provider entries = %+v, want exactly the reserved default gateway entry", p.LLMProviders)
	}
	if p.LLMProviders[0].Config["base_url"] != c.baseURL {
		t.Errorf("entry base_url = %q, want the gateway", p.LLMProviders[0].Config["base_url"])
	}
	// Every layer gets its own row, so the gateway routes and METERS each
	// layer separately — per-layer credit attribution comes for free.
	if _, ok := p.LayerModels[llmlayer.DefaultLayer]; !ok {
		t.Error("the default row is missing")
	}
	for _, layer := range llmlayer.Keys() {
		lm, ok := p.LayerModels[layer]
		if !ok {
			t.Errorf("layer %q has no row", layer)
			continue
		}
		if lm.Model != c.layerAlias(layer) {
			t.Errorf("layer %q model = %q, want %q", layer, lm.Model, c.layerAlias(layer))
		}
		if lm.ProviderID != llmlayer.DefaultProviderID {
			t.Errorf("layer %q names entry %q, want the single gateway entry", layer, lm.ProviderID)
		}
	}
	// The legacy fields are still preset, so an older image keeps working.
	if p.LLM.Provider != managedProvider || p.LLM.Model != c.analysisModel {
		t.Errorf("legacy llm = %+v", p.LLM)
	}
	if p.BlurbLLM == nil || p.BlurbLLM.Model != c.blurbModel {
		t.Errorf("legacy blurb_llm = %+v", p.BlurbLLM)
	}
}

func TestApplyReplacesACraftedPerLayerConfigurationWholesale(t *testing.T) {
	c := managedCfg()
	p := &models.Project{
		LLMProviders: []llmlayer.ProviderEntry{
			{ID: llmlayer.DefaultProviderID, Provider: "openai", Config: map[string]string{"base_url": "https://attacker.example/v1", "credentials_json": "sk-leak"}},
			{ID: "exfil", Provider: "openai", Config: map[string]string{"base_url": "https://attacker.example/v1"}},
		},
		LayerModels: map[string]llmlayer.LayerModel{
			llmlayer.DefaultLayer: {ProviderID: "exfil", Model: "gpt-4o"},
			llmlayer.LayerAsk:     {ProviderID: "exfil", Model: "gpt-4o"},
		},
	}
	c.apply(p)

	if len(p.LLMProviders) != 1 {
		t.Fatalf("provider entries = %+v, want the crafted set replaced wholesale", p.LLMProviders)
	}
	if _, leaked := p.LLMProviders[0].Config["credentials_json"]; leaked {
		t.Error("a crafted credentials_json survived Apply")
	}
	if p.LLMProviders[0].Config["base_url"] != c.baseURL {
		t.Errorf("base_url = %q, want the gateway", p.LLMProviders[0].Config["base_url"])
	}
	for layer, lm := range p.LayerModels {
		if lm.ProviderID != llmlayer.DefaultProviderID {
			t.Errorf("layer %q still names %q", layer, lm.ProviderID)
		}
	}
	if p.LayerModels[llmlayer.LayerAsk].Model != c.layerAlias(llmlayer.LayerAsk) {
		t.Errorf("the ask layer kept a crafted model: %q", p.LayerModels[llmlayer.LayerAsk].Model)
	}
}

func TestIsManagedSecretKeyCoversSecondaryProviderEntries(t *testing.T) {
	c := managedCfg()
	// A crafted write to a secondary entry's key would store a per-project
	// credential that shadows the gateway key for whichever layer names it.
	for _, id := range []string{"exfil", "a_b", "x"} {
		key := llmlayer.CredentialsKey(id)
		if !c.isManagedSecretKey(key) {
			t.Errorf("isManagedSecretKey(%q) = false; the whole llm-credentials- prefix must be refused in managed mode", key)
		}
	}
	for _, key := range []string{"llm-credentials", "blurb-llm-credentials", "embedding-credentials"} {
		if !c.isManagedSecretKey(key) {
			t.Errorf("isManagedSecretKey(%q) = false", key)
		}
	}
	if c.isManagedSecretKey("warehouse-credentials") {
		t.Error("a warehouse credential is not an AI credential and must stay writable in managed mode")
	}

	off := &config{enabled: false}
	if off.isManagedSecretKey(llmlayer.CredentialsKey("exfil")) {
		t.Error("nothing is a managed secret key when managed mode is off")
	}
}

func TestApplyIsANoOpWhenManagedModeIsOff(t *testing.T) {
	c := &config{enabled: false}
	p := &models.Project{
		LLMProviders:     []llmlayer.ProviderEntry{{ID: "mine", Provider: "bedrock"}},
		LayerModels:      map[string]llmlayer.LayerModel{llmlayer.DefaultLayer: {ProviderID: "mine", Model: "opus"}},
		LLMConfigVersion: llmlayer.ConfigVersion,
	}
	c.apply(p)
	if len(p.LLMProviders) != 1 || p.LLMProviders[0].ID != "mine" {
		t.Errorf("a self-hosted project's own configuration was touched: %+v", p.LLMProviders)
	}
}
