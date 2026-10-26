package llmlayer

import "errors"

// ErrLLMNotConfigured is the sentinel every layer resolver returns for a
// project that has never been configured for per-layer models — one whose
// llm_config_version is below ConfigVersion.
//
// It exists so the dozen entry points that resolve a layer (discovery, Ask,
// pack generation, file ingestion, the executive summary, report style, the
// knowledge-source summary) share ONE check and ONE user-facing message: go
// to project settings and choose the models. Callers match it with errors.Is
// and map it to their surface's equivalent of "409, and here is where to go".
var ErrLLMNotConfigured = errors.New("this project's LLM models have not been configured — open project settings and choose a provider and model for each layer")

// ErrNoProviderConfigured is returned when a project has no LLM provider
// entry at all (not even a legacy llm.provider), so there is nothing to
// resolve. Distinct from ErrLLMNotConfigured, which means "configured for an
// older vocabulary, or never reviewed".
var ErrNoProviderConfigured = errors.New("no LLM provider configured")

// ErrUnknownProviderEntry is returned when a layer names a provider entry the
// project does not have. Resolution falls back to the default entry rather
// than failing the run, so this is reported by validation and by the support
// bundle, not raised mid-run.
var ErrUnknownProviderEntry = errors.New("layer names an LLM provider entry that does not exist on this project")

// ErrModelMissing is returned when a resolved layer has no model and its
// config names no endpoint (an endpoint-backed entry legitimately serves its
// own model, so it may run with none).
var ErrModelMissing = errors.New("no model configured for this layer")

// IsNotConfigured reports whether err is the not-configured sentinel,
// including wrapped. A convenience so handlers do not each import errors.
func IsNotConfigured(err error) bool {
	return errors.Is(err, ErrLLMNotConfigured)
}
