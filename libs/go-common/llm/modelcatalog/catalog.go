package modelcatalog

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// The seed catalog. Every model shipped as officially supported is listed
// here with its wire, output-token ceiling, and list price. Each row is the
// authoritative source for what the dashboard shows and how the providers
// dispatch — a new model requires exactly one Register call.
//
// Exclusions:
//   - Deprecated models (legacy Claude 3.x, legacy GPT-3.x) are omitted; a
//     user on a legacy model can still point at it via wire_override.
//   - Models without a clear published token ceiling are omitted to avoid
//     silent under-allocation.
//   - Ollama is not in the catalog: it is a local runtime with a user-
//     populated model list, orthogonal to the cloud-routing problem this
//     catalog solves.
//
// Constants used across rows to keep the seed readable.
const (
	claudeOpus4x6Max  = 128000
	claudeSonnet4xMax = 64000
	claudeHaiku4xMax  = 64000
	claudeOpus4xMax   = 32000
	geminiMax         = 65536
	qwenMax           = 32768
	deepseekMax       = 32768
	mistralLargeMax   = 4096
	mixtralMax        = 8192
	llamaMax          = 8192
	gpt5Max           = 16384
	gpt4oMax          = 16384
	gpt41Max          = 32768
)

// Claude pricing ($ / 1M tokens) — list price, same across every cloud
// (Anthropic partner pricing matches anthropic.com).
const (
	claudeOpusIn   = 15.0
	claudeOpusOut  = 75.0
	claudeSonIn    = 3.0
	claudeSonOut   = 15.0
	claudeHaikuIn  = 0.80
	claudeHaikuOut = 4.0
)

func init() {
	seedBedrock()
	seedVertex()
	seedAzure()
	seedOpenAI()
	seedClaude()

	// Make the catalog the source of truth for MaxOutputTokens when the
	// agent asks (llm.GetMaxOutputTokens). Registered here rather than in
	// registry.go so the seed is populated before any lookup fires.
	gollm.SetMaxTokensCatalogLookup(func(cloud, model string) (int, bool) {
		e, ok := Lookup(cloud, model)
		if !ok {
			return 0, false
		}
		return e.MaxOutputTokens, true
	})

	// Lazy lookup from the registry into the catalog so the
	// /api/v1/providers/llm endpoint carries each provider's model list
	// for the dashboard combobox. We wire the lookup here (rather than
	// pre-snapshotting) because provider init() and catalog init() can
	// run in either order — reading at meta-fetch time avoids the race.
	gollm.SetProviderModelsLookup(func(provider string) []gollm.ModelInfo {
		entries := ListByCloud(provider)
		if len(entries) == 0 {
			return nil
		}
		models := make([]gollm.ModelInfo, 0, len(entries))
		for _, e := range entries {
			models = append(models, gollm.ModelInfo{
				ID:                    e.ID,
				DisplayName:           e.DisplayName,
				Wire:                  string(e.Wire),
				MaxOutputTokens:       e.MaxOutputTokens,
				InputPricePerMillion:  e.InputPricePerMillion,
				OutputPricePerMillion: e.OutputPricePerMillion,
			})
		}
		return models
	})
}

func seedBedrock() {
	// Anthropic wire — Claude on Bedrock.
	Register(Entry{Cloud: "bedrock", ID: "anthropic.claude-opus-4-6-v1:0",
		Wire: Anthropic, DisplayName: "Claude Opus 4.6 (Bedrock)",
		MaxOutputTokens:      claudeOpus4x6Max,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "bedrock", ID: "global.anthropic.claude-opus-4-6-v1",
		Wire: Anthropic, DisplayName: "Claude Opus 4.6 global (Bedrock)",
		MaxOutputTokens:      claudeOpus4x6Max,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "bedrock", ID: "anthropic.claude-sonnet-4-6-v1:0",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4.6 (Bedrock)",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})
	Register(Entry{Cloud: "bedrock", ID: "anthropic.claude-haiku-4-5-v1:0",
		Wire: Anthropic, DisplayName: "Claude Haiku 4.5 (Bedrock)",
		MaxOutputTokens:      claudeHaiku4xMax,
		InputPricePerMillion: claudeHaikuIn, OutputPricePerMillion: claudeHaikuOut})
	Register(Entry{Cloud: "bedrock", ID: "anthropic.claude-opus-4-20250514-v1:0",
		Wire: Anthropic, DisplayName: "Claude Opus 4 (Bedrock)",
		MaxOutputTokens:      claudeOpus4xMax,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "bedrock", ID: "anthropic.claude-sonnet-4-20250514-v1:0",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4 (Bedrock)",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})

	// OpenAI-compat wire — Qwen / DeepSeek / Mistral / Llama on Bedrock.
	Register(Entry{Cloud: "bedrock", ID: "qwen.qwen3-next-80b-a3b",
		Wire: OpenAICompat, DisplayName: "Qwen3-next 80B A3B (Bedrock)",
		MaxOutputTokens:      qwenMax,
		InputPricePerMillion: 0.22, OutputPricePerMillion: 0.88})
	Register(Entry{Cloud: "bedrock", ID: "qwen.qwen3-coder-30b-a3b-v1:0",
		Wire: OpenAICompat, DisplayName: "Qwen3 Coder 30B A3B (Bedrock)",
		MaxOutputTokens:      qwenMax,
		InputPricePerMillion: 0.18, OutputPricePerMillion: 0.72})
	Register(Entry{Cloud: "bedrock", ID: "qwen.qwen3-32b-v1:0",
		Wire: OpenAICompat, DisplayName: "Qwen3 32B (Bedrock)",
		MaxOutputTokens:      qwenMax,
		InputPricePerMillion: 0.18, OutputPricePerMillion: 0.72})
	Register(Entry{Cloud: "bedrock", ID: "deepseek.r1-v1:0",
		Wire: OpenAICompat, DisplayName: "DeepSeek R1 (Bedrock)",
		MaxOutputTokens:      deepseekMax,
		InputPricePerMillion: 1.35, OutputPricePerMillion: 5.40})
	Register(Entry{Cloud: "bedrock", ID: "mistral.mixtral-8x22b-v1:0",
		Wire: OpenAICompat, DisplayName: "Mixtral 8x22B (Bedrock)",
		MaxOutputTokens:      mixtralMax,
		InputPricePerMillion: 0.60, OutputPricePerMillion: 1.80})
	Register(Entry{Cloud: "bedrock", ID: "mistral.mistral-large-2407-v1:0",
		Wire: OpenAICompat, DisplayName: "Mistral Large 2407 (Bedrock)",
		MaxOutputTokens:      mistralLargeMax,
		InputPricePerMillion: 3.0, OutputPricePerMillion: 9.0})
	Register(Entry{Cloud: "bedrock", ID: "meta.llama3-3-70b-instruct-v1:0",
		Wire: OpenAICompat, DisplayName: "Llama 3.3 70B Instruct (Bedrock)",
		MaxOutputTokens:      llamaMax,
		InputPricePerMillion: 0.72, OutputPricePerMillion: 0.72})
	Register(Entry{Cloud: "bedrock", ID: "meta.llama4-maverick-17b-instruct-v1:0",
		Wire: OpenAICompat, DisplayName: "Llama 4 Maverick 17B (Bedrock)",
		MaxOutputTokens:      llamaMax,
		InputPricePerMillion: 0.35, OutputPricePerMillion: 1.40})
}

func seedVertex() {
	// Google-native wire — Gemini.
	Register(Entry{Cloud: "vertex-ai", ID: "gemini-2.5-pro",
		Wire: GoogleNative, DisplayName: "Gemini 2.5 Pro (Vertex)",
		MaxOutputTokens:      geminiMax,
		InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0})
	Register(Entry{Cloud: "vertex-ai", ID: "gemini-2.5-flash",
		Wire: GoogleNative, DisplayName: "Gemini 2.5 Flash (Vertex)",
		MaxOutputTokens:      geminiMax,
		InputPricePerMillion: 0.15, OutputPricePerMillion: 0.60})
	Register(Entry{Cloud: "vertex-ai", ID: "gemini-2.0-flash",
		Wire: GoogleNative, DisplayName: "Gemini 2.0 Flash (Vertex)",
		MaxOutputTokens:      geminiMax,
		InputPricePerMillion: 0.10, OutputPricePerMillion: 0.40})
	Register(Entry{Cloud: "vertex-ai", ID: "gemini-1.5-pro",
		Wire: GoogleNative, DisplayName: "Gemini 1.5 Pro (Vertex)",
		MaxOutputTokens:      geminiMax,
		InputPricePerMillion: 1.25, OutputPricePerMillion: 5.0})
	Register(Entry{Cloud: "vertex-ai", ID: "gemini-1.5-flash",
		Wire: GoogleNative, DisplayName: "Gemini 1.5 Flash (Vertex)",
		MaxOutputTokens:      geminiMax,
		InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30})

	// Anthropic wire — Claude on Vertex.
	Register(Entry{Cloud: "vertex-ai", ID: "claude-opus-4-6@20251101",
		Wire: Anthropic, DisplayName: "Claude Opus 4.6 (Vertex)",
		MaxOutputTokens:      claudeOpus4x6Max,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "vertex-ai", ID: "claude-sonnet-4-6@20251101",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4.6 (Vertex)",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})
	Register(Entry{Cloud: "vertex-ai", ID: "claude-haiku-4-5@20251001",
		Wire: Anthropic, DisplayName: "Claude Haiku 4.5 (Vertex)",
		MaxOutputTokens:      claudeHaiku4xMax,
		InputPricePerMillion: claudeHaikuIn, OutputPricePerMillion: claudeHaikuOut})
	Register(Entry{Cloud: "vertex-ai", ID: "claude-opus-4@20250514",
		Wire: Anthropic, DisplayName: "Claude Opus 4 (Vertex)",
		MaxOutputTokens:      claudeOpus4xMax,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "vertex-ai", ID: "claude-sonnet-4@20250514",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4 (Vertex)",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})

	// OpenAI-compat wire — Vertex Model Garden MaaS.
	Register(Entry{Cloud: "vertex-ai", ID: "meta/llama-3.3-70b-instruct-maas",
		Wire: OpenAICompat, DisplayName: "Llama 3.3 70B Instruct (Vertex MaaS)",
		MaxOutputTokens:      llamaMax,
		InputPricePerMillion: 0.72, OutputPricePerMillion: 0.72})
	Register(Entry{Cloud: "vertex-ai", ID: "meta/llama-4-maverick-17b-instruct-maas",
		Wire: OpenAICompat, DisplayName: "Llama 4 Maverick 17B (Vertex MaaS)",
		MaxOutputTokens:      llamaMax,
		InputPricePerMillion: 0.35, OutputPricePerMillion: 1.40})
	Register(Entry{Cloud: "vertex-ai", ID: "qwen/qwen3-coder-480b-a35b-instruct-maas",
		Wire: OpenAICompat, DisplayName: "Qwen3 Coder 480B A35B (Vertex MaaS)",
		MaxOutputTokens:      qwenMax,
		InputPricePerMillion: 2.0, OutputPricePerMillion: 8.0})
	Register(Entry{Cloud: "vertex-ai", ID: "mistral-ai/mistral-large-2411-001",
		Wire: OpenAICompat, DisplayName: "Mistral Large 2411 (Vertex MaaS)",
		MaxOutputTokens:      mistralLargeMax,
		InputPricePerMillion: 3.0, OutputPricePerMillion: 9.0})
	Register(Entry{Cloud: "vertex-ai", ID: "deepseek-ai/deepseek-r1",
		Wire: OpenAICompat, DisplayName: "DeepSeek R1 (Vertex MaaS)",
		MaxOutputTokens:      deepseekMax,
		InputPricePerMillion: 1.35, OutputPricePerMillion: 5.40})
}

func seedAzure() {
	// OpenAI-compat wire — GPT on Azure Foundry.
	Register(Entry{Cloud: "azure-foundry", ID: "gpt-5",
		Wire: OpenAICompat, DisplayName: "GPT-5 (Azure)",
		MaxOutputTokens:      gpt5Max,
		InputPricePerMillion: 5.0, OutputPricePerMillion: 15.0})
	Register(Entry{Cloud: "azure-foundry", ID: "gpt-5-mini",
		Wire: OpenAICompat, DisplayName: "GPT-5 Mini (Azure)",
		MaxOutputTokens:      gpt5Max,
		InputPricePerMillion: 0.30, OutputPricePerMillion: 1.20})
	Register(Entry{Cloud: "azure-foundry", ID: "gpt-4.1",
		Wire: OpenAICompat, DisplayName: "GPT-4.1 (Azure)",
		MaxOutputTokens:      gpt41Max,
		InputPricePerMillion: 2.0, OutputPricePerMillion: 8.0})
	Register(Entry{Cloud: "azure-foundry", ID: "gpt-4o",
		Wire: OpenAICompat, DisplayName: "GPT-4o (Azure)",
		MaxOutputTokens:      gpt4oMax,
		InputPricePerMillion: 2.50, OutputPricePerMillion: 10.0})
	Register(Entry{Cloud: "azure-foundry", ID: "gpt-4o-mini",
		Wire: OpenAICompat, DisplayName: "GPT-4o Mini (Azure)",
		MaxOutputTokens:      gpt4oMax,
		InputPricePerMillion: 0.15, OutputPricePerMillion: 0.60})

	// OpenAI-compat wire — other vendors on Azure Foundry.
	Register(Entry{Cloud: "azure-foundry", ID: "mistral-large-2411",
		Wire: OpenAICompat, DisplayName: "Mistral Large 2411 (Azure)",
		MaxOutputTokens:      mistralLargeMax,
		InputPricePerMillion: 3.0, OutputPricePerMillion: 9.0})

	// Anthropic wire — Claude on Azure Foundry.
	Register(Entry{Cloud: "azure-foundry", ID: "claude-opus-4-6",
		Wire: Anthropic, DisplayName: "Claude Opus 4.6 (Azure)",
		MaxOutputTokens:      claudeOpus4x6Max,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "azure-foundry", ID: "claude-sonnet-4-6",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4.6 (Azure)",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})
	Register(Entry{Cloud: "azure-foundry", ID: "claude-haiku-4-5",
		Wire: Anthropic, DisplayName: "Claude Haiku 4.5 (Azure)",
		MaxOutputTokens:      claudeHaiku4xMax,
		InputPricePerMillion: claudeHaikuIn, OutputPricePerMillion: claudeHaikuOut})
}

func seedOpenAI() {
	// OpenAI direct — always OpenAICompat wire.
	Register(Entry{Cloud: "openai", ID: "gpt-5",
		Wire: OpenAICompat, DisplayName: "GPT-5",
		MaxOutputTokens:      gpt5Max,
		InputPricePerMillion: 5.0, OutputPricePerMillion: 15.0})
	Register(Entry{Cloud: "openai", ID: "gpt-5-mini",
		Wire: OpenAICompat, DisplayName: "GPT-5 Mini",
		MaxOutputTokens:      gpt5Max,
		InputPricePerMillion: 0.30, OutputPricePerMillion: 1.20})
	Register(Entry{Cloud: "openai", ID: "gpt-4.1",
		Wire: OpenAICompat, DisplayName: "GPT-4.1",
		MaxOutputTokens:      gpt41Max,
		InputPricePerMillion: 2.0, OutputPricePerMillion: 8.0})
	Register(Entry{Cloud: "openai", ID: "gpt-4.1-mini",
		Wire: OpenAICompat, DisplayName: "GPT-4.1 Mini",
		MaxOutputTokens:      gpt41Max,
		InputPricePerMillion: 0.40, OutputPricePerMillion: 1.60})
	Register(Entry{Cloud: "openai", ID: "gpt-4o",
		Wire: OpenAICompat, DisplayName: "GPT-4o",
		MaxOutputTokens:      gpt4oMax,
		InputPricePerMillion: 2.50, OutputPricePerMillion: 10.0})
	Register(Entry{Cloud: "openai", ID: "gpt-4o-mini",
		Wire: OpenAICompat, DisplayName: "GPT-4o Mini",
		MaxOutputTokens:      gpt4oMax,
		InputPricePerMillion: 0.15, OutputPricePerMillion: 0.60})
	Register(Entry{Cloud: "openai", ID: "o3",
		Wire: OpenAICompat, DisplayName: "o3",
		MaxOutputTokens:      100000,
		InputPricePerMillion: 2.0, OutputPricePerMillion: 8.0})
	Register(Entry{Cloud: "openai", ID: "o4-mini",
		Wire: OpenAICompat, DisplayName: "o4-mini",
		MaxOutputTokens:      100000,
		InputPricePerMillion: 1.10, OutputPricePerMillion: 4.40})
}

func seedClaude() {
	// Anthropic direct — always Anthropic wire.
	Register(Entry{Cloud: "claude", ID: "claude-opus-4-6",
		Wire: Anthropic, DisplayName: "Claude Opus 4.6",
		MaxOutputTokens:      claudeOpus4x6Max,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "claude", ID: "claude-sonnet-4-6",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4.6",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})
	Register(Entry{Cloud: "claude", ID: "claude-haiku-4-5",
		Wire: Anthropic, DisplayName: "Claude Haiku 4.5",
		MaxOutputTokens:      claudeHaiku4xMax,
		InputPricePerMillion: claudeHaikuIn, OutputPricePerMillion: claudeHaikuOut})
	Register(Entry{Cloud: "claude", ID: "claude-opus-4-20250514",
		Wire: Anthropic, DisplayName: "Claude Opus 4",
		MaxOutputTokens:      claudeOpus4xMax,
		InputPricePerMillion: claudeOpusIn, OutputPricePerMillion: claudeOpusOut})
	Register(Entry{Cloud: "claude", ID: "claude-sonnet-4-20250514",
		Wire: Anthropic, DisplayName: "Claude Sonnet 4",
		MaxOutputTokens:      claudeSonnet4xMax,
		InputPricePerMillion: claudeSonIn, OutputPricePerMillion: claudeSonOut})
}
