# Generating Domain Packs

> **Version**: 0.4.0 (unreleased)
> **Availability**: Pack generation is implemented in the enterprise plugin. The stock community build exposes the API hooks but returns `404` until a generator provider is registered.

DecisionBox can synthesize a complete `DomainPack` for you from a few inputs:

- One or more **knowledge sources**: website URL(s), DOCX/XLSX/CSV/MD/TXT files, or free-text notes about your business.
- A connected **warehouse**.
- Configured **LLM** + **embedding** providers.

The agent reads everything, runs schema indexing, and emits the categories, JSON Schema profile, analysis areas, and prompt templates a pack needs. You review the draft, optionally regenerate any section with feedback, then click **Start discovery**.

## When to use this

Use generation when you want to skip authoring a pack by hand for a new domain. Built-in packs (gaming, ecommerce, social, system-test) cover common cases — generation is for everything else, especially when you have written knowledge that already explains the business.

## Lifecycle

A generated project moves through four states (see [Discovery lifecycle](../concepts/discovery-lifecycle.md#project-state-machine)):

```
pack_generation_pending  → user fills the wizard
   ↓
pack_generation          → agent runs --mode=pack-gen
   ↓
pack_generation_done     → user reviews / regenerates sections
   ↓
ready                    → discovery unlocked
```

## Wizard flow

1. **/projects/new** → choose **Generate one for me** → enter a project name, pack name, slug, and optional description → **Create draft and continue**.
2. **/projects/{id}/generate** (3-step wizard):
   - **Knowledge sources** — upload files / URLs / free text.
   - **Warehouse + providers** — same panels you'd use in project settings, with "Save and continue" buttons per panel.
   - **Generate** — review the summary and launch.
3. The project page then shows the live agent status, then the draft pack with per-section regenerate boxes.
4. **Start discovery** accepts the pack and enables normal discovery runs.

## API

See the [Pack generation](../reference/api.md#pack-generation) section in the API reference for the underlying endpoints.

## Limits and cost

Pack generation issues a small number of LLM calls (one for the full synthesis plus one per regenerate-section action). For deployments running on DecisionBox Cloud, the per-month cap is set by your plan. Self-hosted enterprise deployments may set `PACK_GEN_PER_MONTH` directly.

## Caveats

- The stock community build does not include the generator. The dashboard wizard is reachable but the launch button returns "not available on this deployment" until an enterprise plugin is loaded.
- Pack generation never overwrites a built-in pack; the user-supplied slug must be unique.
- Sample warehouse rows are sent to your configured LLM provider. The wizard surfaces this disclosure before launch.
