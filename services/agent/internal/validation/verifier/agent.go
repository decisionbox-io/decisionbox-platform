package verifier

import (
	"context"
	"fmt"
	"strings"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// LLMClient is the subset of services/agent/internal/ai.Client the
// verifier needs. Declared as an interface so unit tests stub it.
// Production callers pass an *ai.Client; it satisfies this surface.
type LLMClient interface {
	Chat(ctx context.Context, userPrompt, systemPrompt string, maxTokens int) (*ai.ChatResult, error)
}

// ModeConfig is the per-mode budget envelope. Plan §"Cost envelope".
type ModeConfig struct {
	MaxRounds int
	TokenCap  int
	// MaxOutputTokens is the per-round output CEILING — the most this mode
	// will ever ask for in one call.
	//
	// Zero means "resolve from the model's own output cap" — see
	// Config.ApplyModelOutputCaps. It must not stay zero by the time the loop
	// runs: a fixed ceiling is invisible to the caller and silently truncates
	// the verdict of whichever model outgrows it, which is exactly how a
	// reasoning model's verdicts were lost (the response was cut off
	// mid-JSON, failed to parse, and the document came back unverifiable).
	MaxOutputTokens int
	// MinOutputTokens is what the loop RESERVES for the forced-final verdict
	// out of TokenCap, and the least it will ask for in any one call.
	//
	// Deliberately separate from the ceiling. The loop charges its output
	// budget twice — once for the round it is about to run and once for the
	// forced final it must still be able to afford — so reserving the ceiling
	// would make a bigger ceiling buy FEWER rounds: raising it from 3,000 to
	// 5,000 takes 4,000 off a 20,000 envelope and costs a mid-sized bundle
	// its only evidence-gathering round. Reserving the floor and requesting
	// the ceiling keeps both: rounds get the room they always had, and the
	// verdict gets the room the model allows when it is there.
	MinOutputTokens int
	// OutputBoundByEnvelope records that TokenCap, not the model and not the
	// operator's output value, is what set MaxOutputTokens.
	//
	// It exists for the truncation message. Telling an operator to raise
	// VALIDATION_*_MAX_OUTPUT when the envelope share is the binding
	// constraint sends them to a knob that cannot move the number — the
	// resolution would just re-apply the share — so the message has to name
	// VALIDATION_*_TOKEN_CAP instead.
	OutputBoundByEnvelope bool
}

// Config bundles verifier + refuter knobs and the shared bundle
// truncation settings. Defaults are exposed via DefaultConfig so
// callers can override only the env-bound knobs.
type Config struct {
	Verifier       ModeConfig
	Refuter        ModeConfig
	Bundle         BundleConfig
	RefuterEnabled bool
	// NumericTolerance is the relative tolerance (e.g. 0.20 = ±20%)
	// the verifier + refuter apply when comparing a claim's
	// quantitative figure against row evidence. Default 0.20.
	// Substituted into the system prompts as {{NUMERIC_TOLERANCE_PCT}}.
	NumericTolerance float64
	// MinSampleSize is the minimum row population (e.g. 30) the
	// refuter must observe before treating a row as valid
	// counter-evidence for a market-wide superlative claim.
	// Substituted into the system prompt as {{MIN_SAMPLE_SIZE}}.
	MinSampleSize int
}

// DefaultConfig returns the plan §"Cost envelope" defaults.
func DefaultConfig() Config {
	return Config{
		Verifier:         ModeConfig{MaxRounds: 8, TokenCap: 30000},
		Refuter:          ModeConfig{MaxRounds: 6, TokenCap: 20000},
		Bundle:           DefaultBundleConfig(),
		RefuterEnabled:   true,
		NumericTolerance: 0.20,
		MinSampleSize:    30,
	}
}

// Tracer is an optional hook for per-round visibility. Production
// wires it to the agent's debug logger; tests stub it. nil disables
// tracing.
type Tracer interface {
	OnRound(mode valmodels.AgentMode, round int, action ActionKind, info string)
}

// Agent owns the LLM client, system prompt frames, and config. It is
// reused across Verify/Refute calls; per-call state lives in runState
// (plan §"Agent loop — per-call spend state").
type Agent struct {
	llm LLMClient
	// llmByMode is the per-mode client set (per-layer models, #407): the
	// verifier and the refuter are separate layers, so a project can run the
	// two halves of validation on different models. Empty / a missing entry
	// falls back to llm, which is what NewAgent gives.
	//
	// Keyed by mode because that is the shape this file already has — cfgFor
	// and the system-prompt map are mode-keyed, and the client was the only
	// thing that was not.
	llmByMode map[valmodels.AgentMode]LLMClient
	cfg       Config
	system    map[valmodels.AgentMode]string
	tracer    Tracer
}

// NewAgent constructs an Agent with loaded system prompts, running both modes
// on one client. Returns an error only if the embedded prompt files cannot be
// read (which should be impossible in a built binary).
func NewAgent(llm LLMClient, cfg Config) (*Agent, error) {
	system, err := loadSystemPrompts()
	if err != nil {
		return nil, err
	}
	// A caller that never resolved the model's cap (unit tests, a standalone
	// embedder) must not end up asking for zero output tokens, which some
	// providers read as "give me everything you have". Resolving with an
	// unknown model cap falls through to the documented fallbacks.
	cfg.ApplyModelOutputCaps(0, 0)
	return &Agent{llm: llm, cfg: cfg, system: system}, nil
}

// NewAgentPerMode constructs an Agent whose verifier and refuter run on their
// own clients.
//
// fallback is used for a mode with no entry, so a caller that only overrides
// one half does not have to supply both. This is the layer pair where the
// model choice matters most: verdicts depend heavily on the model, and
// swapping it changes what users see as verified.
func NewAgentPerMode(byMode map[valmodels.AgentMode]LLMClient, fallback LLMClient, cfg Config) (*Agent, error) {
	a, err := NewAgent(fallback, cfg)
	if err != nil {
		return nil, err
	}
	a.llmByMode = byMode
	return a, nil
}

// llmFor returns the client a mode's calls go through — the mode-keyed
// counterpart of cfgFor.
func (a *Agent) llmFor(mode valmodels.AgentMode) LLMClient {
	if c, ok := a.llmByMode[mode]; ok && c != nil {
		return c
	}
	return a.llm
}

// WithTracer attaches a Tracer. Returns the same Agent for chaining.
func (a *Agent) WithTracer(t Tracer) *Agent { a.tracer = t; return a }

// runState holds per-call mutable state. Two consecutive Verify()
// calls on the same Agent never share tokens — each gets a fresh
// runState. Plan §"Agent loop — per-call spend state".
type runState struct {
	mode          valmodels.AgentMode
	bundle        Bundle
	executor      Executor
	history       []toolStep
	tokensIn      int
	tokensOut     int
	queriesIssued int
	lookupsUsed   int
	stepReadsUsed int
	startedAt     time.Time
}

// Verify runs the verifier mode. The returned StructuredVerdict's
// Overall is one of the seven plan statuses; coverage failures are
// downgraded to partial by the finaliser.
func (a *Agent) Verify(ctx context.Context, b Bundle, executor Executor) (valmodels.StructuredVerdict, error) {
	return a.run(ctx, &runState{mode: valmodels.ModeVerifier, bundle: b, executor: executor, startedAt: time.Now()})
}

// Refute runs the refuter mode. Same loop, different system frame.
func (a *Agent) Refute(ctx context.Context, b Bundle, executor Executor) (valmodels.StructuredVerdict, error) {
	return a.run(ctx, &runState{mode: valmodels.ModeRefuter, bundle: b, executor: executor, startedAt: time.Now()})
}

func (a *Agent) cfgFor(mode valmodels.AgentMode) ModeConfig {
	if mode == valmodels.ModeRefuter {
		return a.cfg.Refuter
	}
	return a.cfg.Verifier
}

// run is the agent loop. Plan §"Agent loop":
//  1. Per-round reserve recompute (forced-final prompt size + max
//     output tokens) so a runaway history can't crowd out the
//     forced submission.
//  2. Truncate history's tool-result rows when soft cap would be
//     breached.
//  3. Reject refuter `submit_verdict` payloads when no
//     evidence-gathering tool has been called (refuter discipline).
//  4. On the forced-final round, accept any submit_verdict but
//     downgrade refuter tool-less verdicts to partial.
func (a *Agent) run(ctx context.Context, s *runState) (valmodels.StructuredVerdict, error) {
	mc := a.cfgFor(s.mode)
	allowed := []ActionKind{ActionLookupSchema, ActionQueryWarehouse, ActionReadStepRows, ActionSubmitVerdict}
	systemTemplate := a.system[s.mode]
	system := renderSystemPrompt(systemTemplate, s.mode, s.bundle, a.cfg.NumericTolerance, a.cfg.MinSampleSize)

	for round := 0; round < mc.MaxRounds; round++ {
		reservePrompt := a.buildPrompt(s, []ActionKind{ActionSubmitVerdict}, true)
		// Reserve the FLOOR for the forced final, not the ceiling: the forced
		// final must always be affordable, but reserving the most it could
		// ever want would cost us rounds.
		reserveEst := estimateTokens(reservePrompt, a.cfg.Bundle.EstimateRatio) + mc.MinOutputTokens
		softCap := mc.TokenCap - reserveEst
		if softCap <= 0 {
			break
		}

		prompt := a.buildPrompt(s, allowed, false)
		promptEst := estimateTokens(prompt, a.cfg.Bundle.EstimateRatio)
		// What this round can afford once the forced final is set aside. A
		// round that cannot afford even the floor drops history and retries;
		// with nothing left to drop the loop ends and the forced final runs.
		afford := softCap - (s.tokensIn + s.tokensOut + promptEst)
		if afford < mc.MinOutputTokens {
			if !a.truncateHistory(s) {
				break
			}
			continue
		}
		roundOutput := mc.MaxOutputTokens
		if afford < roundOutput {
			roundOutput = afford
		}

		chat, err := a.llmFor(s.mode).Chat(ctx, prompt, system, roundOutput)
		if err != nil {
			return a.unverifiable(s, "chat failed: "+err.Error()), nil
		}
		s.tokensIn += chat.TokensIn
		s.tokensOut += chat.TokensOut

		action, perr := ParseAction(chat.Content, allowed)
		if perr != nil {
			detail := parseFailureDetail(chat, roundOutput, perr)
			a.trace(s.mode, round, "", "parse-error: "+detail)
			s.history = append(s.history, toolStep{Error: "parse: " + detail})
			continue
		}
		a.trace(s.mode, round, action.Kind, "")

		if action.Kind == ActionSubmitVerdict {
			// Refuter discipline: reject tool-less verdicts on
			// EVERY normal round. If the loop runs out of rounds
			// without an evidence-backed submission, the forced-
			// final block (outside this loop) accepts the verdict
			// but downgrades the Overall to partial.
			if s.mode == valmodels.ModeRefuter && s.queriesIssued+s.stepReadsUsed == 0 {
				s.history = append(s.history, toolStep{
					Error: "refuter discipline: cannot submit_verdict before running at least one query_warehouse or read_step_rows; gather evidence first",
				})
				continue
			}
			return a.finalise(*action.Verdict, s), nil
		}

		res, terr := a.execTool(ctx, s, action)
		if terr != nil {
			s.history = append(s.history, toolStep{Kind: action.Kind, Error: terr.Error()})
			continue
		}
		s.history = append(s.history, toolStep{Kind: action.Kind, Result: res})

		switch action.Kind {
		case ActionQueryWarehouse:
			s.queriesIssued++
		case ActionLookupSchema:
			s.lookupsUsed++
		case ActionReadStepRows:
			s.stepReadsUsed++
		}
	}

	// Forced final round — submit_verdict only.
	a.aggressiveTruncate(s)
	forcedPrompt := a.buildPrompt(s, []ActionKind{ActionSubmitVerdict}, true)
	// Nothing follows this call, so it may spend everything left in the
	// envelope up to the ceiling — and never less than the floor, which is
	// what every round reserved for exactly this.
	forcedOutput := mc.MaxOutputTokens
	if afford := mc.TokenCap - (s.tokensIn + s.tokensOut + estimateTokens(forcedPrompt, a.cfg.Bundle.EstimateRatio)); afford < forcedOutput {
		forcedOutput = afford
	}
	if forcedOutput < mc.MinOutputTokens {
		forcedOutput = mc.MinOutputTokens
	}
	chat, err := a.llmFor(s.mode).Chat(ctx, forcedPrompt, system, forcedOutput)
	if err != nil {
		return a.unverifiable(s, "forced chat failed: "+err.Error()), nil
	}
	s.tokensIn += chat.TokensIn
	s.tokensOut += chat.TokensOut

	action, perr := ParseAction(chat.Content, []ActionKind{ActionSubmitVerdict})
	if perr != nil || action.Kind != ActionSubmitVerdict {
		reason := "forced final round produced no verdict"
		if outputCapHit(chat, forcedOutput) {
			// Name the knob that can actually move this number. The envelope
			// binds either because it clipped the ceiling at resolution time
			// or because the rounds already spent what the verdict needed.
			key := envOutputKeyFor(s.mode)
			if mc.OutputBoundByEnvelope || forcedOutput < mc.MaxOutputTokens {
				key = envTokenCapKeyFor(s.mode)
			}
			// The difference matters: a cut-off verdict is fixed by raising
			// the ceiling, a malformed one by fixing the prompt. Reported
			// here because this string is what the dashboard shows and what
			// an operator has to act on.
			reason = fmt.Sprintf(
				"forced final round was cut off at the %d-token output ceiling before it finished the verdict; raise %s",
				forcedOutput, key)
			applog.WithFields(applog.Fields{
				"mode":        s.mode,
				"max_tokens":  forcedOutput,
				"tokens_out":  chat.TokensOut,
				"stop_reason": chat.StopReason,
				"env":         key,
			}).Warn("Validation verdict truncated at the output ceiling")
		}
		return a.unverifiable(s, reason), nil
	}
	final := a.finalise(*action.Verdict, s)
	// Refuter discipline on forced-final: even on the last round, a
	// refuter that ran zero evidence tools has not actually attempted
	// refutation. Downgrade so the measurement of refuter
	// effectiveness is not muddied by lazy "couldn't refute"
	// submissions. lookup_schema does NOT count as evidence — only
	// query_warehouse + read_step_rows.
	if s.mode == valmodels.ModeRefuter && s.queriesIssued+s.stepReadsUsed == 0 {
		final.Overall = valmodels.StatusPartial
		final.OverallReason = appendReason(final.OverallReason, "refuter discipline: zero tool calls before forced final")
	}
	return final, nil
}

func (a *Agent) trace(mode valmodels.AgentMode, round int, action ActionKind, info string) {
	if a.tracer == nil {
		return
	}
	a.tracer.OnRound(mode, round, action, info)
}

func (a *Agent) execTool(ctx context.Context, s *runState, action Action) (map[string]any, error) {
	switch action.Kind {
	case ActionLookupSchema:
		return s.executor.LookupSchema(ctx, action.LookupSchemaRefs)
	case ActionQueryWarehouse:
		return s.executor.QueryWarehouse(ctx, action.Sql)
	case ActionReadStepRows:
		if action.StepRowsReq == nil {
			return nil, fmt.Errorf("read_step_rows: missing request body")
		}
		return s.executor.ReadStepRows(ctx, *action.StepRowsReq)
	}
	return nil, fmt.Errorf("non-executable action %q in execTool", action.Kind)
}

// buildPrompt assembles the user message. The system frame is rendered
// once per bundle and stays constant across rounds.
func (a *Agent) buildPrompt(s *runState, allowed []ActionKind, forced bool) string {
	var sb strings.Builder
	sb.WriteString("# Bundle\n")
	sb.WriteString(s.bundle.renderForPrompt())
	if len(s.history) > 0 {
		sb.WriteString("\n\n# Recent tool history\n")
		for i, h := range s.history {
			if h.Error != "" {
				fmt.Fprintf(&sb, "[%d] %s ERROR: %s\n", i, h.Kind, h.Error)
				continue
			}
			fmt.Fprintf(&sb, "[%d] %s result: %s\n", i, h.Kind, serialise(h.Result))
		}
	}
	if forced {
		sb.WriteString("\n\n# Budget exhausted — submit verdict now\n")
		sb.WriteString("Emit exactly one submit_verdict payload. No more tool calls will be accepted.\n")
	} else {
		sb.WriteString("\n\n# Next turn\n")
		sb.WriteString("Emit exactly ONE of: ")
		names := make([]string, 0, len(allowed))
		for _, a := range allowed {
			names = append(names, string(a))
		}
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString(".\n")
	}
	return sb.String()
}

// truncateHistory drops the rows from the oldest non-truncated tool
// result. Marks it `rows_omitted_due_to_history_truncation: true` so
// downstream consumers can tell "no rows" apart from "rows omitted
// because we ran out of prompt budget". Returns true if a slot was
// truncated; false when there's nothing left to drop.
func (a *Agent) truncateHistory(s *runState) bool {
	for i := range s.history {
		h := &s.history[i]
		if h.Error != "" || h.Result == nil {
			continue
		}
		if _, ok := h.Result["rows_omitted_due_to_history_truncation"]; ok {
			continue
		}
		if _, ok := h.Result["rows"]; ok {
			h.Result["rows"] = []any{}
			h.Result["rows_omitted_due_to_history_truncation"] = true
			return true
		}
	}
	return false
}

// aggressiveTruncate drops rows from every history entry before the
// forced-final round so the bundle + verdict have maximum budget.
func (a *Agent) aggressiveTruncate(s *runState) {
	for i := range s.history {
		h := &s.history[i]
		if h.Result == nil {
			continue
		}
		if _, ok := h.Result["rows"]; ok {
			h.Result["rows"] = []any{}
			h.Result["rows_omitted_due_to_history_truncation"] = true
		}
	}
}

// finalise runs the deterministic coverage checks. Plan §"Hard
// coverage finaliser":
//
//	step 0 — duplicate detection on claims_considered AND on
//	         claim_verdicts.claim_text (case-folded + whitespace-
//	         collapsed)
//	step 1 — claims_considered must be non-empty
//	step 2 — headline positional rule (claims_considered[0] ==
//	         unique is_headline=true entry's claim_text)
//	step 3 — set-equality between claims_considered and claim_text
//	step 4 — evidence required for confirmed/supported/rejected
//	step 4.5 — status enum validation
//	step 5 — unverifiable rules
//	step 6 — derive Overall when omitted
//
// On any failure, Overall is rewritten (usually to partial) and a
// "coverage:" reason is appended to OverallReason.
func (a *Agent) finalise(v valmodels.StructuredVerdict, s *runState) valmodels.StructuredVerdict {
	v.Mode = s.mode
	v.DocID = s.bundle.Doc.ID
	v.DocKind = s.bundle.Doc.Kind
	v.LookupsUsed = s.lookupsUsed
	v.QueriesIssued = s.queriesIssued
	v.StepReadsUsed = s.stepReadsUsed
	v.LLMTokensIn = s.tokensIn
	v.LLMTokensOut = s.tokensOut
	v.DurationMillis = time.Since(s.startedAt).Milliseconds()

	if len(v.ClaimsConsidered) == 0 {
		v.Overall = valmodels.StatusUnverifiable
		v.OverallReason = appendReason(v.OverallReason, "coverage: no claims enumerated")
		return v
	}

	// Step 0a — duplicates in claims_considered.
	if hasDuplicates(v.ClaimsConsidered) {
		v.Overall = valmodels.StatusPartial
		v.OverallReason = appendReason(v.OverallReason, "coverage: duplicate claims_considered entries")
		return v
	}

	// Step 0b — duplicates in claim_verdicts.claim_text (catches the
	// "all empty strings" failure mode observed in MVP runs).
	cvTexts := make([]string, 0, len(v.ClaimVerdicts))
	for _, c := range v.ClaimVerdicts {
		cvTexts = append(cvTexts, c.ClaimText)
	}
	if hasDuplicates(cvTexts) {
		v.Overall = valmodels.StatusPartial
		v.OverallReason = appendReason(v.OverallReason, "coverage: duplicate claim_verdicts.claim_text entries")
		return v
	}

	headline := v.ClaimsConsidered[0]

	// Step 2 — headline positional rule.
	headlineCount := 0
	headlineMatch := false
	for _, c := range v.ClaimVerdicts {
		if c.IsHeadline {
			headlineCount++
			if c.ClaimText == headline {
				headlineMatch = true
			}
		}
	}
	if headlineCount != 1 || !headlineMatch {
		v.Overall = valmodels.StatusPartial
		v.OverallReason = appendReason(v.OverallReason, fmt.Sprintf("coverage: headline rule violated (count=%d match=%v)", headlineCount, headlineMatch))
		return v
	}

	// Step 3 — set-equality.
	expected := stringSet(v.ClaimsConsidered)
	actual := stringSet(cvTexts)
	if !setsEqual(expected, actual) {
		v.Overall = valmodels.StatusPartial
		v.OverallReason = appendReason(v.OverallReason, "coverage: claim_verdicts does not match claims_considered set")
		return v
	}

	// Step 4 — evidence requirement for non-unverifiable claims.
	// `partial` belongs in this list too: it asserts a non-trivial
	// quantitative judgment (e.g. "headline figure off by 8%") that
	// must be backed by a row, otherwise the model can launder
	// uncited claims through `partial` without tripping any of the
	// other coverage rules.
	for i, c := range v.ClaimVerdicts {
		switch c.Status {
		case valmodels.StatusConfirmed, valmodels.StatusSupported, valmodels.StatusRejected, valmodels.StatusPartial:
			if c.Evidence.Kind == "" || c.Evidence.Kind == "none" || c.Evidence.Row == nil {
				v.Overall = valmodels.StatusPartial
				v.OverallReason = appendReason(v.OverallReason, fmt.Sprintf("coverage: missing evidence on claim_verdicts[%d] (status=%s)", i, c.Status))
				return v
			}
		}
	}

	// Step 4.5 — status enum validation. Per-claim statuses are
	// restricted to the five claim-level values; `validation_disabled`
	// and `skipped_budget_cap` are doc-level trip-wires only and would
	// short-circuit the evidence-required rule above if accepted.
	for i, c := range v.ClaimVerdicts {
		if !c.Status.IsValidPerClaim() {
			v.Overall = valmodels.StatusPartial
			v.OverallReason = appendReason(v.OverallReason, fmt.Sprintf("coverage: invalid status %q on claim_verdicts[%d] (per-claim must be one of confirmed/supported/rejected/partial/unverifiable)", c.Status, i))
			return v
		}
	}

	// Step 5 — unverifiable rules.
	headlineUnv := false
	allUnv := true
	anyUnv := false
	for _, c := range v.ClaimVerdicts {
		if c.IsHeadline && c.Status == valmodels.StatusUnverifiable {
			headlineUnv = true
		}
		if c.Status != valmodels.StatusUnverifiable {
			allUnv = false
		}
		if c.Status == valmodels.StatusUnverifiable {
			anyUnv = true
		}
	}
	if headlineUnv {
		v.Overall = valmodels.StatusUnverifiable
		v.OverallReason = appendReason(v.OverallReason, "coverage: headline unverifiable")
		return v
	}
	if allUnv {
		v.Overall = valmodels.StatusUnverifiable
		v.OverallReason = appendReason(v.OverallReason, "coverage: all claims unverifiable")
		return v
	}
	if anyUnv && v.Overall != valmodels.StatusRejected {
		v.Overall = valmodels.StatusPartial
		v.OverallReason = appendReason(v.OverallReason, "coverage: some claims unverifiable")
		return v
	}

	// Step 6 — derive Overall when the model omitted it (observed
	// when every per-claim is supported and the model "feels" the
	// overall is implied) OR when the model emitted an unknown value
	// (typos like "supportd"). The per-claim enum check above
	// doesn't cover the top-level field, so an invalid Overall used
	// to slip through and collapse the combined verdict to
	// "unverifiable" downstream.
	if v.Overall == "" || !v.Overall.IsKnown() {
		original := v.Overall
		v.Overall = deriveOverall(v.ClaimVerdicts)
		if original == "" {
			v.OverallReason = appendReason(v.OverallReason, "overall derived from per-claim verdicts (model omitted)")
		} else {
			v.OverallReason = appendReason(v.OverallReason, fmt.Sprintf("overall derived from per-claim verdicts (model returned invalid %q)", string(original)))
		}
	}

	return v
}

// deriveOverall conservatively folds per-claim verdicts into a single
// Overall when the model omitted the top-level field (or returned a
// known-bad value the finaliser is correcting).
//
// Partial claims used to be ignored entirely, so one supported +
// one partial promoted to `supported` and an all-partial verdict
// collapsed to `unverifiable`. Both undercounted the partial
// signal: a single partial claim taints the whole document. The
// fold now treats partial similarly to a soft-reject:
// any partial demotes a would-be supported/confirmed overall to
// partial; all-partial returns partial (not unverifiable).
func deriveOverall(cvs []valmodels.ClaimVerdict) valmodels.Status {
	if len(cvs) == 0 {
		return valmodels.StatusUnverifiable
	}
	rej, sup, conf, partial := 0, 0, 0, 0
	for _, c := range cvs {
		switch c.Status {
		case valmodels.StatusRejected:
			rej++
		case valmodels.StatusSupported:
			sup++
		case valmodels.StatusConfirmed:
			conf++
		case valmodels.StatusPartial:
			partial++
		}
	}
	if rej > 0 {
		return valmodels.StatusRejected
	}
	if partial > 0 {
		return valmodels.StatusPartial
	}
	if conf > 0 && sup == 0 {
		return valmodels.StatusConfirmed
	}
	if sup+conf > 0 {
		return valmodels.StatusSupported
	}
	return valmodels.StatusUnverifiable
}

func (a *Agent) unverifiable(s *runState, reason string) valmodels.StructuredVerdict {
	return valmodels.StructuredVerdict{
		DocID:          s.bundle.Doc.ID,
		DocKind:        s.bundle.Doc.Kind,
		Mode:           s.mode,
		Overall:        valmodels.StatusUnverifiable,
		OverallReason:  reason,
		LookupsUsed:    s.lookupsUsed,
		QueriesIssued:  s.queriesIssued,
		StepReadsUsed:  s.stepReadsUsed,
		LLMTokensIn:    s.tokensIn,
		LLMTokensOut:   s.tokensOut,
		DurationMillis: time.Since(s.startedAt).Milliseconds(),
	}
}

// hasDuplicates returns true if xs contains two entries that compare
// equal after lowercasing + whitespace-collapsing. Empty strings count
// as duplicates of each other — observed when the LLM emits a stub
// `claim_verdicts` list.
func hasDuplicates(xs []string) bool {
	seen := make(map[string]bool, len(xs))
	for _, x := range xs {
		k := strings.Join(strings.Fields(strings.ToLower(x)), " ")
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

func stringSet(xs []string) map[string]int {
	m := make(map[string]int, len(xs))
	for _, x := range xs {
		m[x]++
	}
	return m
}

func setsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func appendReason(existing, msg string) string {
	if existing == "" {
		return msg
	}
	return existing + "; " + msg
}

// outputCapHit reports whether a response stopped because it hit the output
// ceiling rather than because the model finished.
//
// It trusts the provider's stop reason when there is one, and otherwise falls
// back to "spent the entire budget", which is what the analysis phase already
// uses (logOutputCapTruncation): a response that both failed to parse and
// consumed every available token is a truncation, not a malformed answer. The
// fallback matters because the reason is only as reliable as the longest wire
// it crossed — a managed gateway that drops it leaves the token count as the
// only evidence.
func outputCapHit(chat *ai.ChatResult, maxTokens int) bool {
	if chat == nil {
		return false
	}
	if chat.Truncated() {
		return true
	}
	return maxTokens > 0 && chat.TokensOut >= maxTokens
}

// parseFailureDetail annotates a mid-loop parse error with the output ceiling
// when the response was cut off, so the note the model reads back in its tool
// history tells it to be briefer rather than to re-emit the same oversized
// payload.
func parseFailureDetail(chat *ai.ChatResult, maxTokens int, perr error) string {
	if outputCapHit(chat, maxTokens) {
		return fmt.Sprintf("%s (response was cut off at the %d-token output ceiling — emit a shorter payload)", perr.Error(), maxTokens)
	}
	return perr.Error()
}

// envOutputKeyFor names the per-mode output knob, so the recorded reason is
// actionable without reading the source.
func envOutputKeyFor(mode valmodels.AgentMode) string {
	if mode == valmodels.ModeRefuter {
		return EnvRefuterOutput
	}
	return EnvVerifierOutput
}

// envTokenCapKeyFor names the per-mode ENVELOPE knob, for the case where the
// envelope — not the model and not the output knob — is what the verdict ran
// out of.
func envTokenCapKeyFor(mode valmodels.AgentMode) string {
	if mode == valmodels.ModeRefuter {
		return EnvRefuterTokenCap
	}
	return EnvVerifierTokenCap
}
