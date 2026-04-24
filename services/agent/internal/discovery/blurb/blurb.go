// Package blurb turns warehouse table metadata into the per-table
// natural-language descriptions (~80-150 tokens) that the retrieval
// layer embeds and stores in Qdrant.
//
// The prompt is taken verbatim from PLAN-SCHEMA-RETRIEVAL.md §6.1.
// The spike (blurb-spike/FINDINGS.md) validated that this prompt + a
// Haiku-class model produces hallucination-free descriptions with
// top-1 retrieval accuracy on a real 2K-table ERP warehouse.
//
// The generator is parallelised (default 8 workers, env BLURB_WORKERS).
// Per-table failures do not abort the run — blurbs are independent, so
// a single LLM timeout should not cost the whole FINPORT-scale rebuild.
// An overall failure-rate budget (MaxFailureRate, default 5 %) caps how
// many individual failures the caller will tolerate before declaring
// the run bankrupt.
package blurb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// DefaultMaxTokens for a blurb response. The prompt targets 2-4 sentences,
// but the model sometimes pads — 512 is plenty while capping blowouts.
const DefaultMaxTokens = 512

// DefaultMaxFailureRate is the fraction of per-table blurb failures the
// generator will tolerate before aborting the run. 5% on a 2000-table
// warehouse = 100 failed blurbs — acceptable because the retrieval layer
// will simply not have those tables indexed (they fall back to the
// catalog line + inspect_table path). Beyond that it usually indicates
// credentials/quota problems, so we fail fast.
const DefaultMaxFailureRate = 0.05

// DefaultWorkers defaults to 8 parallel LLM calls. §9 of the plan says
// this achieves ~6 min wall-clock on FINPORT (2K tables) with Bedrock
// Haiku blurbs — bumping higher risks hitting Bedrock quotas; lower and
// the wall-clock balloons.
const DefaultWorkers = 8

// MaxBlurbLen is the hard ceiling on blurb text (characters). The prompt
// targets 150 tokens (~600 chars) — anything over 4000 is a runaway.
const MaxBlurbLen = 4000

// Input is what the caller feeds per table.
type Input struct {
	// Dataset is the dataset / schema / database the table lives in.
	Dataset string
	// Schema supplies the column list + row count for the prompt.
	Schema models.TableSchema
	// DomainPackBlurb is the 1-2 sentence domain-pack context (e.g. the
	// "match-3 game" intro) to ground the blurb. Optional — blank is
	// acceptable per plan §6.1, with a documented quality-drop tradeoff.
	DomainPackBlurb string
}

// Output is the generator's per-table result. Err is set on failure and
// Blurb is empty; callers decide whether to skip the table or retry.
type Output struct {
	Table string
	Blurb string
	// InputTokens + OutputTokens are stamped for cost estimation.
	InputTokens  int
	OutputTokens int
	Err          error
}

// Config parameterises a Generator.
type Config struct {
	// LLM is the blurb-generation provider. Must support text responses;
	// tool use is not required and not used. Reasoning-class models are
	// rejected at construction time (see ErrReasoningModelNotSupported).
	LLM gollm.Provider
	// Model is the exact model ID to send on every request. Not inferred
	// from LLM.
	Model string
	// ProviderName is used for error-context + the payload's blurb_model
	// field in the Qdrant index.
	ProviderName string
	// Workers defaults to DefaultWorkers.
	Workers int
	// MaxTokens defaults to DefaultMaxTokens.
	MaxTokens int
	// MaxFailureRate defaults to DefaultMaxFailureRate.
	MaxFailureRate float64
}

// Generator turns a batch of TableSchemas into blurbs in parallel.
// Construct with New; reuse across index runs to amortise any per-
// provider setup cost inside the LLM implementation.
type Generator struct {
	llm            gollm.Provider
	model          string
	providerName   string
	workers        int
	maxTokens      int
	maxFailureRate float64
}

// Errors the generator can return upfront from New.
var (
	ErrReasoningModelNotSupported = errors.New("reasoning-class models are not supported for blurb generation — their <think> channel is not exposed via Converse/Chat, producing empty blurbs (spike finding §4)")
	ErrLLMMissing                 = errors.New("LLM provider is required")
	ErrModelMissing               = errors.New("model is required")
)

// New builds a Generator. Rejects reasoning-class models at construction
// so misconfiguration surfaces the moment the user hits "Save" on the
// project settings page, not 30 minutes into a failed index run.
func New(cfg Config) (*Generator, error) {
	if cfg.LLM == nil {
		return nil, ErrLLMMissing
	}
	if cfg.Model == "" {
		return nil, ErrModelMissing
	}
	if IsReasoningClassModel(cfg.Model) {
		return nil, fmt.Errorf("blurb: %w (model=%q)", ErrReasoningModelNotSupported, cfg.Model)
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	rate := cfg.MaxFailureRate
	if rate <= 0 {
		rate = DefaultMaxFailureRate
	}
	return &Generator{
		llm:            cfg.LLM,
		model:          cfg.Model,
		providerName:   cfg.ProviderName,
		workers:        workers,
		maxTokens:      maxTokens,
		maxFailureRate: rate,
	}, nil
}

// Progress callbacks are fired after each table completes (success or
// failure). Nil is fine — the Generator treats no-progress as a valid
// operating mode for tests / one-off CLI runs.
type ProgressFunc func(done int)

// Generate produces blurbs for every input in parallel.
//
// Returns when:
//  1. every input has produced an Output (order preserved), OR
//  2. the failure rate exceeds MaxFailureRate (returns ErrTooManyFailures +
//     the partial Outputs so the caller can decide whether to persist
//     what it has).
//
// ctx cancellation aborts in-flight workers; outstanding Inputs get an
// Output{Err: ctx.Err()} so the Output slice stays one-to-one with the
// Input slice.
func (g *Generator) Generate(ctx context.Context, inputs []Input, progress ProgressFunc) ([]Output, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	outputs := make([]Output, len(inputs))
	failures := int64(0)
	// Budget computed up-front so it doesn't drift if the caller mutates
	// MaxFailureRate between runs (it shouldn't — but defensive).
	maxFailures := int64(float64(len(inputs)) * g.maxFailureRate)
	// On tiny inputs (len < 20) any failure already exceeds 5 %, which
	// is harsher than necessary. Allow at least one failure to tolerate
	// transient errors without aborting a 3-table dev warehouse.
	if maxFailures < 1 {
		maxFailures = 1
	}

	// Fan out a fixed-size worker pool. Buffered input channel keeps the
	// producer unblocked so ctx cancellation surfaces promptly.
	type job struct {
		idx int
		in  Input
	}
	jobs := make(chan job, g.workers)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(g.workers)
	for w := 0; w < g.workers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					// ctx cancelled between submit and pickup — stamp the
					// slot + count as a failure so the failure-budget check
					// at the end returns a meaningful error instead of
					// swallowing the cancellation.
					outputs[j.idx] = Output{Table: j.in.Schema.TableName, Err: ctx.Err()}
					atomic.AddInt64(&failures, 1)
					if progress != nil {
						progress(1)
					}
					continue
				}
				out := g.oneBlurb(ctx, j.in)
				outputs[j.idx] = out
				if out.Err != nil {
					n := atomic.AddInt64(&failures, 1)
					if n > maxFailures {
						cancel() // signal workers to short-circuit
					}
				}
				if progress != nil {
					progress(1)
				}
			}
		}()
	}

	for i, in := range inputs {
		select {
		case <-ctx.Done():
			// ctx cancelled: fill remaining slots with ctx.Err so the
			// length invariant holds, and stop submitting.
			outputs[i] = Output{Table: in.Schema.TableName, Err: ctx.Err()}
			for j := i + 1; j < len(inputs); j++ {
				outputs[j] = Output{Table: inputs[j].Schema.TableName, Err: ctx.Err()}
			}
			close(jobs)
			wg.Wait()
			if failures > maxFailures {
				return outputs, fmt.Errorf("blurb: %w (%d of %d failed, cap %d)", ErrTooManyFailures, failures, len(inputs), maxFailures)
			}
			return outputs, ctx.Err()
		case jobs <- job{idx: i, in: in}:
		}
	}
	close(jobs)
	wg.Wait()

	if failures > maxFailures {
		return outputs, fmt.Errorf("blurb: %w (%d of %d failed, cap %d)", ErrTooManyFailures, failures, len(inputs), maxFailures)
	}
	return outputs, nil
}

// ErrTooManyFailures is returned from Generate when the fraction of per-
// table failures exceeds MaxFailureRate. Callers should surface this to
// the indexing worker so the run is marked failed and the UI shows the
// reason.
var ErrTooManyFailures = errors.New("too many per-table blurb failures")

func (g *Generator) oneBlurb(ctx context.Context, in Input) Output {
	table := in.Schema.TableName
	prompt := buildPrompt(in)

	req := gollm.ChatRequest{
		Model:     g.model,
		MaxTokens: g.maxTokens,
		// Deterministic output helps with idempotent indexing — same
		// schema → same blurb → same embedding → same point ID.
		Temperature: 0,
		Messages:    []gollm.Message{{Role: "user", Content: prompt}},
	}
	// Bedrock + direct Anthropic reject Temperature on Opus 4.x extended-
	// thinking modes (FINDINGS.md §5). §6.2 of the plan rejects them
	// entirely — we already do at construction — but a belt-and-braces
	// safeguard for any "omit temperature on X" flavour: if the selected
	// model is one that complains, drop the field. Today no non-reasoning
	// model errors on temperature=0, so this branch stays empty.

	resp, err := g.llm.Chat(ctx, req)
	if err != nil {
		return Output{Table: table, Err: fmt.Errorf("blurb(%s): %w", g.providerID(), err)}
	}

	text := strings.TrimSpace(resp.Content)
	if text == "" {
		// Reasoning models would land here — we already reject them up-
		// front, so this only triggers on genuinely empty API responses.
		return Output{Table: table, Err: fmt.Errorf("blurb(%s): empty response from %s", g.providerID(), g.model)}
	}
	if len(text) > MaxBlurbLen {
		// Runaway response — mid-word truncation would poison the
		// embedding. Fail the per-table blurb; the generator's failure
		// budget decides whether that's fatal for the run.
		return Output{Table: table, Err: fmt.Errorf("blurb(%s): response exceeds MaxBlurbLen=%d chars (got %d); pick a less verbose model or raise the cap", g.providerID(), MaxBlurbLen, len(text))}
	}
	return Output{
		Table:        table,
		Blurb:        text,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
}

func (g *Generator) providerID() string {
	if g.providerName != "" {
		return g.providerName + "/" + g.model
	}
	return g.model
}

// buildPrompt fills in the grounded prompt from plan §6.1.
//
// Notable constraints codified in the template:
//   - "ground every claim in the supplied metadata" + "do not invent"
//     is the single line doing most of the work against hallucination —
//     spike tested this across 5 blurb models with zero invented
//     columns/FKs/use-cases observed.
//   - row count is injected explicitly so the blurb mentions it rather
//     than guessing from column names.
//   - FK hints are left blank when we don't have any — the prompt says
//     that's acceptable rather than making something up.
func buildPrompt(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Describe the table %q in 2-4 sentences for a data analyst:\n", in.Schema.TableName)
	b.WriteString("- what a row represents\n")
	b.WriteString("- 3-5 most important columns (name, type, role)\n")
	b.WriteString("- foreign-key-shaped relationships to other tables\n")
	b.WriteString("- approximate row count\n")
	b.WriteString("- 1-2 plausible analytical uses\n\n")
	b.WriteString("Ground every claim in the supplied metadata. Do not invent columns, relationships, or use cases.\n\n")

	b.WriteString("Metadata:\n")
	fmt.Fprintf(&b, "  Dataset:        %s\n", nonEmpty(in.Dataset, "(unknown)"))
	b.WriteString("  Columns:\n")
	for _, c := range in.Schema.Columns {
		nullable := "NOT NULL"
		if c.Nullable {
			nullable = "NULL"
		}
		category := ""
		if c.Category != "" {
			category = " [" + c.Category + "]"
		}
		sample := firstNonNilSample(in.Schema.SampleData, c.Name, 3)
		if sample != "" {
			fmt.Fprintf(&b, "    - %s %s %s%s   samples: %s\n", c.Name, c.Type, nullable, category, sample)
		} else {
			fmt.Fprintf(&b, "    - %s %s %s%s\n", c.Name, c.Type, nullable, category)
		}
	}
	fmt.Fprintf(&b, "  Row count:      %d\n", in.Schema.RowCount)

	fkHints := formatFKHints(in.Schema.KeyColumns)
	if fkHints != "" {
		fmt.Fprintf(&b, "  FK hints:       %s\n", fkHints)
	} else {
		b.WriteString("  FK hints:       (none)\n")
	}

	if strings.TrimSpace(in.DomainPackBlurb) != "" {
		fmt.Fprintf(&b, "  Domain pack:    %s\n", strings.TrimSpace(in.DomainPackBlurb))
	} else {
		b.WriteString("  Domain pack:    (none — describe generically)\n")
	}

	b.WriteString("\nReturn plain text, 2-4 sentences.")
	return b.String()
}

func firstNonNilSample(rows []map[string]interface{}, col string, maxSamples int) string {
	if len(rows) == 0 {
		return ""
	}
	seen := 0
	parts := make([]string, 0, maxSamples)
	for _, row := range rows {
		v, ok := row[col]
		if !ok || v == nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		s = strings.Join(strings.Fields(s), " ")
		if len(s) > 40 {
			s = s[:40] + "…"
		}
		parts = append(parts, s)
		seen++
		if seen >= maxSamples {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// formatFKHints renders KeyColumns (primary / foreign key names) as an
// arrow-separated list. We don't have first-class FK metadata on the
// TableSchema today — KeyColumns is the best proxy and suffices for
// grounding per the spike.
func formatFKHints(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	trimmed := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			trimmed = append(trimmed, k)
		}
	}
	return strings.Join(trimmed, ", ")
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// reasoningClassPatterns lists the model-ID substrings that our blurb
// pipeline cannot use because their output is routed through a <think>
// channel that Converse / Chat Completions don't expose as text
// (FINDINGS.md §4). Catch-all patterns — we'd rather reject a non-
// reasoning match by accident than let a reasoning model silently emit
// empty blurbs.
var reasoningClassPatterns = []string{
	"deepseek-r1",
	"r1-", // DeepSeek R1 variants (r1-distill, r1-lite, ...)
	"o1-",
	"o3",
	"o4-mini",
	"extended-thinking",
}

// IsReasoningClassModel returns true for models whose output channel
// does not carry the user-visible text on OpenAI / Bedrock Converse /
// Anthropic Messages APIs. Used by New() and exposed for the API
// project-save handler to block misconfiguration at the earliest point.
// Matching is case-insensitive.
func IsReasoningClassModel(model string) bool {
	lc := strings.ToLower(model)
	for _, p := range reasoningClassPatterns {
		if strings.Contains(lc, p) {
			return true
		}
	}
	return false
}
