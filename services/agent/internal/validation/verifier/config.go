package verifier

import (
	"os"
	"strconv"
	"strings"
)

// Env-var keys the agent honours. Documented in
// docs/reference/configuration.md and listed in plan §"Cost envelope
// and budgets".
// These const names trigger gosec G101 ("Potential hardcoded
// credentials") because of the `_TOKEN_` substring. They are env-var
// names, not credentials — silence the false positive at the block
// level rather than per-line.
//
//nolint:gosec // env var names, not credentials
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

	// EnvConcurrency bounds how many documents are validated at once.
	// Default 1 (one at a time). Each document runs its verifier then its
	// refuter, both issuing LLM calls and warehouse queries, so raising it
	// multiplies the load on the LLM provider's rate limit and on the
	// warehouse; validation is otherwise the longest phase of a run.
	EnvConcurrency = "VALIDATION_CONCURRENCY"
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
	// Concurrency is how many documents are validated at once
	// (VALIDATION_CONCURRENCY). Values below 1 mean 1.
	Concurrency int
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
		Concurrency:              getEnvInt(EnvConcurrency, 1),
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

// Fallback per-round output ceilings, used only when the model's own output
// cap is unknown (an uncatalogued model with no live metadata). These are the
// values the validation loop shipped with; they are kept as the floor of last
// resort rather than as the default, because as a default they silently
// truncated the verdicts of any model that needs more.
const (
	fallbackVerifierOutputTokens = 4000
	fallbackRefuterOutputTokens  = 3000
)

// validationOutputShare bounds the per-round output ceiling to a fraction of
// the mode's token envelope.
//
// The loop reserves `forced-final prompt + MaxOutputTokens` out of TokenCap
// before each round and stops early when that reserve does not fit. An output
// ceiling taken straight from a 128K-output model would make the reserve
// exceed the whole envelope, sending every document to its forced final round
// with no evidence gathered — trading a truncated verdict for an uninformed
// one. A quarter leaves the bundle and the tool history three quarters.
const validationOutputShare = 4

// ApplyModelOutputCaps sizes each mode's per-round output ceiling from the
// resolved output cap of the model that mode actually runs on (the verifier
// and the refuter are separate layers and may differ).
//
// Precedence per mode, matching how the other bounded phases resolve theirs:
//
//  1. an explicit operator value (VALIDATION_*_MAX_OUTPUT, already loaded
//     into MaxOutputTokens by LoadConfigFromEnv);
//  2. the model's own output cap;
//  3. the documented fallback.
//
// The result is then bounded by validationOutputShare. Pass 0 for a cap that
// could not be resolved.
func (c *Config) ApplyModelOutputCaps(verifierModelCap, refuterModelCap int) {
	// Captured before the resolution overwrites the field, so the binding
	// constraint can be told apart afterwards.
	operatorVerifier, operatorRefuter := c.Verifier.MaxOutputTokens, c.Refuter.MaxOutputTokens
	c.Verifier.MaxOutputTokens = resolveModeOutputTokens(c.Verifier, verifierModelCap, fallbackVerifierOutputTokens)
	c.Refuter.MaxOutputTokens = resolveModeOutputTokens(c.Refuter, refuterModelCap, fallbackRefuterOutputTokens)
	// The reservation floor is the per-mode budget the loop shipped with: it
	// is what a verdict has always fitted in, so reserving it keeps the round
	// budgeting exactly as it was while the ceiling above grows with the
	// model. Never above the ceiling — a model that accepts less than the
	// floor must not be asked for the floor.
	c.Verifier.MinOutputTokens = minInt(fallbackVerifierOutputTokens, c.Verifier.MaxOutputTokens)
	c.Refuter.MinOutputTokens = minInt(fallbackRefuterOutputTokens, c.Refuter.MaxOutputTokens)
	c.Verifier.OutputBound = outputConstraintFor(c.Verifier, verifierModelCap, operatorVerifier)
	c.Refuter.OutputBound = outputConstraintFor(c.Refuter, refuterModelCap, operatorRefuter)
	c.outputResolved = true
}

// outputConstraintFor reports which of the three candidates set the mode's
// ceiling, so a truncation message names something that can actually move it.
//
// Several candidates can sit on the same number, and then the order matters:
// raising an easier one is clamped straight back by a harder one, which is the
// message that costs an operator a deploy and teaches them to distrust the
// diagnosis. So ties resolve toward the constraint that is hardest to move —
// the model first (no validation setting raises it), then the envelope (only
// TokenCap), then the operator's own value.
func outputConstraintFor(m ModeConfig, modelCap, operatorValue int) OutputConstraint {
	switch {
	case modelCap > 0 && m.MaxOutputTokens == modelCap:
		return OutputBoundByModel
	case m.TokenCap/validationOutputShare > 0 && m.MaxOutputTokens == m.TokenCap/validationOutputShare:
		return OutputBoundByEnvelope
	default:
		// The operator's value, or — when neither they nor the model could
		// answer — the documented fallback, which the output knob replaces.
		return OutputBoundByOperator
	}
}

// resolveModeOutputTokens implements ApplyModelOutputCaps for one mode.
func resolveModeOutputTokens(m ModeConfig, modelCap, fallback int) int {
	out := m.MaxOutputTokens
	switch {
	case out <= 0:
		out = modelCap
	case modelCap > 0 && out > modelCap:
		// An operator value above what the model accepts is rejected by the
		// provider and would fail every validation call on the project, so
		// the model's cap wins — the clamp-only-lowers rule the window side
		// follows too.
		out = modelCap
	}
	if out <= 0 {
		out = fallback
	}
	if share := m.TokenCap / validationOutputShare; share > 0 && out > share {
		out = share
	}
	return out
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
