package verifier

import (
	"os"
	"strconv"
	"strings"
)

// Env-var keys the agent honours. Documented in
// docs/reference/configuration.md and listed in plan §"Cost envelope
// and budgets".
const (
	EnvVerifierRounds   = "VALIDATION_VERIFIER_MAX_ROUNDS"
	EnvVerifierTokenCap = "VALIDATION_VERIFIER_TOKEN_CAP"
	EnvVerifierOutput   = "VALIDATION_VERIFIER_MAX_OUTPUT"
	EnvRefuterRounds    = "VALIDATION_REFUTER_MAX_ROUNDS"
	EnvRefuterTokenCap  = "VALIDATION_REFUTER_TOKEN_CAP"
	EnvRefuterOutput    = "VALIDATION_REFUTER_MAX_OUTPUT"
	EnvRefuterEnabled   = "VALIDATION_REFUTER_ENABLED"
	EnvBundleSample     = "VALIDATION_BUNDLE_SAMPLE_ROWS"
	EnvBundleCellCap    = "VALIDATION_BUNDLE_CELL_CHAR_CAP"
	EnvRecStepBudget    = "VALIDATION_REC_STEPS_TOKEN_BUDGET"
	EnvEstimateRatio    = "VALIDATION_ESTIMATE_TOKEN_RATIO"

	EnvMaxInsightsPerRun   = "VALIDATION_MAX_INSIGHTS_PER_RUN"
	EnvMaxRecsPerRun       = "VALIDATION_MAX_RECOMMENDATIONS_PER_RUN"
	EnvMaxReadStepRowsCall = "VALIDATION_MAX_READ_STEP_ROWS"
	// EnvNumericTolerance is the relative tolerance for comparing a
	// claim's quantitative figure against row evidence. Default 0.20
	// (±20%). The verifier + refuter prompts render the value as a
	// percentage; the rule prevents rounding-noise from triggering
	// `rejected` (e.g. "27% spike" vs evidence 26.5% stays
	// supported — the relative diff is 1.85%, well inside ±20%).
	EnvNumericTolerance = "VALIDATION_NUMERIC_TOLERANCE"
	// EnvMinSampleSize is the minimum row-population the refuter
	// must observe before it can use a row as counter-evidence for
	// a market-wide superlative claim. Default 30 (the classical
	// "large enough sample" threshold). Below this, a contradicting
	// row is treated as a small-sample outlier rather than a
	// refutation — UNLESS the original claim's own sample is
	// similarly small (in which case the comparison is apples-to-
	// apples and the threshold doesn't apply).
	EnvMinSampleSize = "VALIDATION_MIN_SAMPLE_SIZE"
)

// RunCaps are the run-level caps the orchestrator applies AROUND the
// per-doc verifier loop. Distinct from the per-mode budget caps the
// agent enforces internally.
type RunCaps struct {
	MaxInsightsPerRun        int
	MaxRecommendationsPerRun int
	// MaxReadStepRowsCall caps the per-call `limit` parameter the
	// agent may pass to `read_step_rows`. Without this cap the model
	// can request the entire step result, blowing prompt + memory +
	// cost before history truncation can help. Default 200.
	MaxReadStepRowsCall int
}

// LoadConfigFromEnv reads every documented validation knob from env
// vars, falling back to plan defaults for any that aren't set.
// Returns the per-agent Config and the run-level caps as separate
// values so the orchestrator can route them to the right places.
func LoadConfigFromEnv() (Config, RunCaps) {
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = getEnvInt(EnvVerifierRounds, cfg.Verifier.MaxRounds)
	cfg.Verifier.TokenCap = getEnvInt(EnvVerifierTokenCap, cfg.Verifier.TokenCap)
	cfg.Verifier.MaxOutputTokens = getEnvInt(EnvVerifierOutput, cfg.Verifier.MaxOutputTokens)
	cfg.Refuter.MaxRounds = getEnvInt(EnvRefuterRounds, cfg.Refuter.MaxRounds)
	cfg.Refuter.TokenCap = getEnvInt(EnvRefuterTokenCap, cfg.Refuter.TokenCap)
	cfg.Refuter.MaxOutputTokens = getEnvInt(EnvRefuterOutput, cfg.Refuter.MaxOutputTokens)
	cfg.Bundle.SampleRows = getEnvInt(EnvBundleSample, cfg.Bundle.SampleRows)
	cfg.Bundle.CellCharCap = getEnvInt(EnvBundleCellCap, cfg.Bundle.CellCharCap)
	cfg.Bundle.RecStepsTokenCap = getEnvInt(EnvRecStepBudget, cfg.Bundle.RecStepsTokenCap)
	cfg.Bundle.EstimateRatio = getEnvFloat(EnvEstimateRatio, cfg.Bundle.EstimateRatio)
	cfg.RefuterEnabled = getEnvBool(EnvRefuterEnabled, cfg.RefuterEnabled)
	cfg.NumericTolerance = getEnvFloat(EnvNumericTolerance, cfg.NumericTolerance)
	cfg.MinSampleSize = getEnvInt(EnvMinSampleSize, cfg.MinSampleSize)

	caps := RunCaps{
		MaxInsightsPerRun:        getEnvInt(EnvMaxInsightsPerRun, 30),
		MaxRecommendationsPerRun: getEnvInt(EnvMaxRecsPerRun, 15),
		MaxReadStepRowsCall:      getEnvInt(EnvMaxReadStepRowsCall, 200),
	}
	return cfg, caps
}

func getEnvInt(k string, fallback int) int {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvFloat(k string, fallback float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(k string, fallback bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return fallback
}
