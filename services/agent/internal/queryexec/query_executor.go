package queryexec

import (
	"context"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// QueryExecutor executes queries with self-healing capabilities.
//
// It depends on the narrow gowarehouse.QueryRunner seam rather than on the
// full SQL Provider interface: executing a query and repairing a failed one
// are all it does, and neither needs schema introspection or identifier
// quoting. A SQL provider is adapted onto the seam by
// gowarehouse.AsQueryRunner, so the SQL path is unchanged.
type QueryExecutor struct {
	runner      gowarehouse.QueryRunner
	sqlFixer    SQLFixer
	debugLogger *debug.Logger
	maxRetries  int
	filterField string
	filterValue string
	// nonSQLLanguage names the language this source's queries are written in
	// when that is not SQL, and is "" for every SQL warehouse. It decides
	// whether the tenant filter can be verified at all — see verifyFilter.
	nonSQLLanguage string
	currentStep    int
	currentPhase   string
}

// FixOpts carries per-call context for the SQL fixer that does not belong on
// the fixer instance because it varies per request — verification SQL has
// different column-grounding evidence per insight, while exploration queries
// have none. Empty by default; exploration callers pass FixOpts{}, the
// validator passes a rendered VerificationContext so the fixer does not
// re-emit the same hallucinated column on retry.
type FixOpts struct {
	// VerificationContext is the same string the verifier renders into its
	// own generation prompt: source-step SQL + (in a later layer) lookup_schema
	// results, in priority order. Inserted into the fixer prompt verbatim via
	// the {{VERIFICATION_CONTEXT}} placeholder; an empty value strips the
	// surrounding {{#VERIFICATION_CONTEXT}}…{{/VERIFICATION_CONTEXT}} section
	// from the rendered prompt.
	VerificationContext string
}

// FixResult is the structured return shape of SQLFixer.FixSQL. It
// carries the parsed proposed SQL alongside the raw prompt / response
// and token / latency accounting from the LLM call. The executor records
// one FixAttempt per call into ExecuteResult.FixHistory from these
// fields, so consumers downstream of the executor (the exploration
// engine, the debug logger, any plugin extension) have access to the
// full repair trajectory rather than just the final fixed SQL.
type FixResult struct {
	FixedSQL     string
	Prompt       string
	Response     string
	InputTokens  int
	OutputTokens int
	DurationMs   int64
}

// SQLFixer defines the interface for fixing SQL queries.
type SQLFixer interface {
	FixSQL(ctx context.Context, query string, error string, attempt int, opts FixOpts) (FixResult, error)
}

// QueryExecutorOptions configures the query executor.
type QueryExecutorOptions struct {
	// Warehouse is a SQL provider, adapted onto the QueryRunner seam. This is
	// how every existing caller configures the executor.
	Warehouse gowarehouse.Provider
	// Runner supplies the query seam directly, for a source that is not a SQL
	// provider. When set it takes precedence over Warehouse.
	Runner      gowarehouse.QueryRunner
	SQLFixer    SQLFixer
	DebugLogger *debug.Logger
	MaxRetries  int
	// ProviderSlug is the datasource's registered provider. Supplying it is
	// what makes the tenant-filter guard robust: the language is then read
	// from the registry, which no middleware can erase, rather than from a
	// type assertion on a provider that may have been wrapped.
	ProviderSlug string
	FilterField  string // optional: field to verify in queries (e.g., "app_id")
	FilterValue  string // optional: value the field must match
}

// NewQueryExecutor creates a new query executor with self-healing.
func NewQueryExecutor(opts QueryExecutorOptions) *QueryExecutor {
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 5
	}
	runner := opts.Runner
	if runner == nil && opts.Warehouse != nil {
		runner = gowarehouse.AsQueryRunner(opts.Warehouse)
	}
	// Which language this source's queries are written in, decided from what
	// the caller supplied rather than from the runner it becomes.
	//
	// Runner first, and unconditionally: it exists to supply the seam for a
	// source that is NOT a SQL provider, so a caller reaching for it has
	// already said what this is. Asking whether that runner also happens to
	// implement Provider would leave a pure one looking like SQL, and a pure
	// one is the ordinary case — which would reopen the unscoped-query bypass
	// on the exact path built for the sources that have it.
	//
	// Warehouse otherwise, read from the PROVIDER: AsQueryRunner adapts
	// anything, so by the time a SQL warehouse is a runner the distinction is
	// gone and every warehouse would look native.
	nonSQL := ""
	switch {
	// The registry first, because it is the only source of this answer that
	// cannot be erased. A provider reaches this constructor through
	// middleware, and a wrapper only has to return a Provider — one that does
	// not re-expose QueryRunner turns a native source into an apparent SQL
	// warehouse and skips the guard below. A registration cannot be wrapped.
	case gowarehouse.NonSQLLanguageOf(opts.ProviderSlug) != "":
		nonSQL = gowarehouse.NonSQLLanguageOf(opts.ProviderSlug)
	// Runner exists to supply the seam for a source that is NOT a SQL
	// provider, so a caller reaching for it has already said what this is.
	case opts.Runner != nil:
		nonSQL = opts.Runner.QueryLanguage()
	// And the live provider last: still right for a caller that supplies
	// neither a slug nor a runner, and still wrong through a flattening
	// wrapper — which is why the slug is preferred wherever there is one.
	case opts.Warehouse != nil:
		nonSQL = gowarehouse.NonSQLLanguage(opts.Warehouse)
	}
	return &QueryExecutor{
		runner:         runner,
		nonSQLLanguage: nonSQL,
		sqlFixer:       opts.SQLFixer,
		debugLogger:    opts.DebugLogger,
		maxRetries:     opts.MaxRetries,
		filterField:    opts.FilterField,
		filterValue:    opts.FilterValue,
		currentPhase:   "exploration",
	}
}

func (e *QueryExecutor) SetStep(step int)                { e.currentStep = step }
func (e *QueryExecutor) SetPhase(phase string)           { e.currentPhase = phase }
func (e *QueryExecutor) SetDebugLogger(dl *debug.Logger) { e.debugLogger = dl }

// CurrentStep reports the step number the executor is currently bound
// to — what FixHistory entries and debug-log rows stamp as their parent
// step. Tests use this to assert the exploration engine wires the
// per-step number through before each Execute call.
func (e *QueryExecutor) CurrentStep() int { return e.currentStep }

// ExecuteResult represents the result of a query execution.
type ExecuteResult struct {
	Data            []map[string]interface{}
	RowCount        int
	ExecutionTimeMs int64
	FixAttempts     int
	Fixed           bool
	OriginalQuery   string
	FinalQuery      string
	Errors          []string

	// FixHistory is one entry per LLM fix call made during this
	// execution, in chronological order. Empty when no fix was needed.
	// Callers that persist the executing step (the exploration engine)
	// copy this onto ExplorationStep.FixHistory so the per-attempt trail
	// is preserved in storage alongside the rest of the step's dialog.
	FixHistory []models.FixAttempt

	// Quality carries the source-reported caveats attached to the result that
	// finally succeeded — rows withheld, values sampled, a tail truncated.
	// Nil when the source declared none, which is every SQL warehouse.
	//
	// It is deliberately on the success path rather than folded into Errors:
	// the query did not fail, and treating a caveat as a failure would discard
	// a usable answer. The caller decides what a caveat means for the
	// conclusion it is about to draw — which it can only do if the caveat
	// survives the executor, so this field is what stops it being dropped at
	// the provider boundary.
	Quality []gowarehouse.QualityCaveat
}

// Execute executes a query with automatic self-healing. It forwards to
// ExecuteWithFixOpts with an empty FixOpts — exploration callers (the only
// other consumer) have no per-call grounding context to forward. Validator
// callers should call ExecuteWithFixOpts directly so the SQL fixer sees the
// same column-grounding evidence the verification prompt was built on.
func (e *QueryExecutor) Execute(ctx context.Context, query string, purpose string) (*ExecuteResult, error) {
	return e.ExecuteWithFixOpts(ctx, query, purpose, FixOpts{})
}

// ExecuteWithFixOpts is Execute plus per-call FixOpts that propagate to the
// SQL fixer on every retry. The opts are forwarded unchanged on each retry
// attempt — the fixer is expected to substitute them into its prompt template
// each time, so the LLM sees the same evidence regardless of which retry
// attempt is in flight.
func (e *QueryExecutor) ExecuteWithFixOpts(ctx context.Context, query string, purpose string, opts FixOpts) (*ExecuteResult, error) {
	return e.ExecuteNative(ctx, gowarehouse.SQLQuery(query), purpose, opts)
}

// ExecuteNative is ExecuteWithFixOpts over a query in any source's language.
// It is the executor's general entry point; the string-based methods above
// wrap a SQL statement into a NativeQuery and call through here.
//
// A structured query — one carrying a provider-native payload rather than
// text — executes through the same loop but is NOT repaired on failure. The
// repair path rewrites query *text*, and applying a text rewrite to a query
// whose meaning lives in its payload would either drop the payload or pair it
// with contradicting text: a query that runs cleanly and answers a different
// question than the one asked. Sources with structured queries are expected to
// bring their own repair mechanism (a pre-flight validator catches more, and
// earlier, than a post-hoc rewrite); until one is wired in, such a failure is
// returned honestly rather than silently patched.
func (e *QueryExecutor) ExecuteNative(ctx context.Context, query gowarehouse.NativeQuery, purpose string, opts FixOpts) (*ExecuteResult, error) {
	startTime := time.Now()

	result := &ExecuteResult{
		OriginalQuery: query.String(),
		FinalQuery:    query.String(),
		Errors:        make([]string, 0),
	}

	currentQuery := query

	// A structured query cannot be scope-checked. verifyFilter inspects query
	// text, but a structured query's meaning lives in its payload — the two are
	// set independently, so text mentioning the scope field says nothing about
	// what the payload asks for. Checking the text anyway would report a
	// guarantee this code cannot keep, and the result would be a query that
	// passed the security check and ran unscoped. Refuse instead: a source with
	// structured queries must enforce scope by its own mechanism (a scoped
	// credential, or a filter the adapter builds into the payload it submits).
	if query.IsStructured() && e.filterField != "" {
		return nil, fmt.Errorf("security violation: a structured query cannot be scope-checked against %s; "+
			"the source must enforce scope through its own credential or payload", e.filterField)
	}

	if err := e.verifyFilter(currentQuery.String()); err != nil {
		return nil, fmt.Errorf("security violation: %w", err)
	}

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		applog.WithFields(applog.Fields{
			"attempt":   attempt,
			"max":       e.maxRetries,
			"purpose":   purpose,
			"phase":     e.currentPhase,
			"step":      e.currentStep,
			"query_len": len(currentQuery.String()),
		}).Debug("Executing warehouse query")

		qr, err := e.runner.RunQuery(ctx, currentQuery)
		executionTime := time.Since(startTime).Milliseconds()

		if err == nil {
			result.Data = qr.Rows
			result.RowCount = len(qr.Rows)
			result.ExecutionTimeMs = executionTime
			result.FinalQuery = currentQuery.String()
			result.Fixed = attempt > 0
			result.Quality = qr.Quality

			if qr.Degraded() {
				caveats := make([]string, 0, len(qr.Quality))
				for _, c := range qr.Quality {
					caveats = append(caveats, c.String())
				}
				applog.WithFields(applog.Fields{
					"purpose": purpose,
					"phase":   e.currentPhase,
					"step":    e.currentStep,
					"caveats": strings.Join(caveats, "; "),
				}).Warn("Query succeeded but the source reported the result is degraded")
			}

			applog.WithFields(applog.Fields{
				"rows":     result.RowCount,
				"time_ms":  executionTime,
				"fixed":    result.Fixed,
				"attempts": attempt + 1,
				"purpose":  purpose,
			}).Debug("Query executed successfully")

			if e.debugLogger != nil {
				fixedQuery := ""
				if result.Fixed {
					fixedQuery = result.FinalQuery
				}
				e.debugLogger.LogWarehouseQuery(ctx, e.currentStep, e.currentPhase,
					query.String(), purpose, result.Data, result.RowCount, result.ExecutionTimeMs,
					nil, result.FixAttempts, fixedQuery)
			}

			return result, nil
		}

		result.Errors = append(result.Errors, err.Error())

		applog.WithFields(applog.Fields{
			"attempt": attempt,
			"max":     e.maxRetries,
			"error":   err.Error(),
			"purpose": purpose,
		}).Warn("Query failed")

		if attempt >= e.maxRetries {
			applog.WithFields(applog.Fields{
				"attempts": attempt + 1,
				"purpose":  purpose,
				"error":    err.Error(),
			}).Error("Query exhausted all retry attempts")

			if e.debugLogger != nil {
				e.debugLogger.LogWarehouseQuery(ctx, e.currentStep, e.currentPhase,
					query.String(), purpose, nil, 0, time.Since(startTime).Milliseconds(),
					err, result.FixAttempts, "")
			}
			return result, fmt.Errorf("query failed after %d attempts: %w", attempt+1, err)
		}

		// Classify a structured failure first. A non-SQL source is the case
		// that legitimately runs without a SQL fixer, so checking the fixer
		// first would report "no SQL fixer available" for a query that could
		// not have been repaired by one anyway — an accurate-sounding message
		// pointing at the wrong thing.
		if currentQuery.IsStructured() {
			applog.WithFields(applog.Fields{
				"purpose": purpose,
				"error":   err.Error(),
			}).Error("Structured query failed and the text repair loop cannot repair it")
			return result, fmt.Errorf("query failed and structured queries have no repair path: %w", err)
		}

		if e.sqlFixer == nil {
			applog.Error("Query failed and no SQL fixer available — cannot retry")
			return result, fmt.Errorf("query failed and no SQL fixer available: %w", err)
		}

		applog.WithFields(applog.Fields{
			"attempt": attempt + 1,
			"error":   err.Error(),
		}).Info("Attempting SQL fix via LLM")

		fix, fixErr := e.sqlFixer.FixSQL(ctx, currentQuery.String(), err.Error(), attempt, opts)
		if fixErr != nil {
			// The fixer call failed (LLM transport error OR the response
			// couldn't be parsed into SQL). Record the attempt so the
			// LLM dialog and accounting aren't lost — these are exactly
			// the negative examples downstream tooling wants — then
			// return the partial result so the caller can read it.
			result.FixHistory = append(result.FixHistory, models.FixAttempt{
				Step:         e.currentStep,
				Attempt:      attempt,
				PromptIn:     fix.Prompt,
				ResponseOut:  fix.Response,
				SQLBefore:    currentQuery.String(),
				SQLAfter:     fix.FixedSQL,
				ErrorIn:      err.Error(),
				FixerError:   fixErr.Error(),
				InputTokens:  fix.InputTokens,
				OutputTokens: fix.OutputTokens,
				DurationMs:   fix.DurationMs,
				Timestamp:    time.Now(),
			})
			applog.WithError(fixErr).Error("SQL fixer failed")
			return result, fmt.Errorf("failed to fix SQL query: %w", fixErr)
		}

		if verifyErr := e.verifyFilter(fix.FixedSQL); verifyErr != nil {
			// The fixer produced parseable SQL but it violated the
			// security filter contract. Record the attempt with
			// FixerError set so the rejection is visible — same negative-
			// example value as a fixer-side failure.
			result.FixHistory = append(result.FixHistory, models.FixAttempt{
				Step:         e.currentStep,
				Attempt:      attempt,
				PromptIn:     fix.Prompt,
				ResponseOut:  fix.Response,
				SQLBefore:    currentQuery.String(),
				SQLAfter:     fix.FixedSQL,
				ErrorIn:      err.Error(),
				FixerError:   "fixed query security violation: " + verifyErr.Error(),
				InputTokens:  fix.InputTokens,
				OutputTokens: fix.OutputTokens,
				DurationMs:   fix.DurationMs,
				Timestamp:    time.Now(),
			})
			applog.WithError(verifyErr).Error("Fixed query failed security filter check")
			return result, fmt.Errorf("fixed query security violation: %w", verifyErr)
		}

		result.FixHistory = append(result.FixHistory, models.FixAttempt{
			Step:         e.currentStep,
			Attempt:      attempt,
			PromptIn:     fix.Prompt,
			ResponseOut:  fix.Response,
			SQLBefore:    currentQuery.String(),
			SQLAfter:     fix.FixedSQL,
			ErrorIn:      err.Error(),
			InputTokens:  fix.InputTokens,
			OutputTokens: fix.OutputTokens,
			DurationMs:   fix.DurationMs,
			Timestamp:    time.Now(),
		})

		applog.Debug("SQL fix applied, retrying with corrected query")
		result.FixAttempts++
		currentQuery = gowarehouse.SQLQuery(fix.FixedSQL)
		startTime = time.Now()
	}

	return nil, fmt.Errorf("query execution failed unexpectedly")
}

// ExecuteWithHistory executes a query and returns a QueryHistory record.
func (e *QueryExecutor) ExecuteWithHistory(ctx context.Context, query string, purpose string) (*ExecuteResult, *models.QueryHistory) {
	result, err := e.Execute(ctx, query, purpose)

	history := &models.QueryHistory{
		Query:      query,
		Purpose:    purpose,
		ExecutedAt: time.Now(),
	}

	if err != nil {
		history.Success = false
		history.Error = err.Error()
		if result != nil {
			history.FixAttempts = result.FixAttempts
		}
		return result, history
	}

	history.Success = true
	history.RowsReturned = result.RowCount
	history.ExecutionTimeMs = result.ExecutionTimeMs
	history.FixAttempts = result.FixAttempts

	return result, history
}

// verifyFilter checks if the query contains the required filter field.
// If no filter is configured (self-hosted, dedicated dataset), all queries pass.
func (e *QueryExecutor) verifyFilter(query string) error {
	if e.filterField == "" {
		return nil // no filter required
	}
	// A substring check is only evidence in SQL, where the field name appearing in
	// the text at least means a predicate probably mentions it. In any other
	// query language it means nothing: a GA4 report request naming `country`
	// among its DIMENSIONS contains the string and filters by nothing, so the
	// check passes and the query runs property-wide while reporting as scoped.
	//
	// Refuse instead of pretending. Scope for such a source rests on its
	// credential — a property or an org is what the credential can reach — and
	// a filter configured here is a guarantee this code cannot keep. Failing
	// loudly at configuration is the only honest answer; silently returning
	// unscoped rows as though they were scoped is the failure this refusal
	// exists to prevent.
	if e.nonSQLLanguage != "" {
		return fmt.Errorf("a %s query cannot be checked for the %s filter; "+
			"scope for this source rests on its credential, so remove the filter from the datasource "+
			"or scope the credential instead", e.nonSQLLanguage, e.filterField)
	}
	if !strings.Contains(strings.ToLower(query), strings.ToLower(e.filterField)) {
		applog.WithFields(applog.Fields{
			"filter_field":  e.filterField,
			"query_preview": query[:min(len(query), 80)],
		}).Warn("Query missing required filter field")
		return fmt.Errorf("query must filter by %s for security", e.filterField)
	}
	return nil
}
