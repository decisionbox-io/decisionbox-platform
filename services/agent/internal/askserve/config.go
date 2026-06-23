// Package askserve implements the agent's ad-hoc data Q&A serve mode: an
// always-up HTTP service that answers a natural-language data question by
// running a bounded, tool-using reasoning loop against a project's
// warehouse (read-only) and the project's schema index, persisting each
// step as it completes so a reader can tail progress by cursor.
//
// It reuses the agent's existing primitives — the self-healing query
// executor (tenant filter + SQL repair), the cached schema provider
// (lookup_schema / search_tables), and the LLM client — wired per project
// behind a warm connection pool. A single question is modeled exactly like
// a discovery run: a long-running background job whose progress is durable,
// not tied to any held connection.
package askserve

import (
	"time"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
)

// Env var names for the serve mode. All knobs are parametric (no hardcoded
// operational values) so a deployment can tune budgets without a rebuild.
const (
	EnvPort                = "ASK_SERVE_PORT"
	EnvMaxFetchRows        = "ASK_SERVE_MAX_FETCH_ROWS"
	EnvPreviewRows         = "ASK_SERVE_PREVIEW_ROWS"
	EnvMaxQueriesPerTurn   = "ASK_SERVE_MAX_QUERIES_PER_TURN"
	EnvMaxRounds           = "ASK_SERVE_MAX_ROUNDS"
	EnvWallClockSeconds    = "ASK_SERVE_WALLCLOCK_SECONDS"
	EnvQueryTimeoutSeconds = "ASK_SERVE_QUERY_TIMEOUT_SECONDS"
	EnvPoolIdleTTL         = "ASK_SERVE_POOL_IDLE_TTL"
	EnvPoolMaxProjects     = "ASK_SERVE_POOL_MAX_PROJECTS"
	EnvMaxConcurrentTurns  = "ASK_SERVE_MAX_CONCURRENT_TURNS"
	EnvMaxConcurrentPerPrj = "ASK_SERVE_MAX_CONCURRENT_TURNS_PER_PROJECT"
	EnvHistoryCharBudget   = "ASK_SERVE_HISTORY_CHAR_BUDGET"
	EnvTurnTTLSeconds      = "ASK_SERVE_TURN_TTL_SECONDS"
	EnvConnectTimeoutSecs  = "ASK_SERVE_CONNECT_TIMEOUT_SECONDS"
)

// Defaults match the reviewed plan. A single enterprise query can run for
// hours, so the per-turn wall-clock and per-query timeouts default to 12h;
// these bound a durable background job, not a held connection.
const (
	defaultPort                = 8090
	defaultMaxFetchRows        = 1000
	defaultPreviewRows         = 50
	defaultMaxQueriesPerTurn   = 6
	defaultMaxRounds           = 12
	defaultWallClock           = 12 * time.Hour
	defaultQueryTimeout        = 12 * time.Hour
	defaultPoolIdleTTL         = 10 * time.Minute
	defaultPoolMaxProjects     = 50
	defaultMaxConcurrentTurns  = 20
	defaultMaxConcurrentPerPrj = 3
	// defaultHistoryCharBudget bounds the prior-conversation context fed
	// into a turn. ~48k characters ≈ ~12k tokens at the usual 4 chars/token
	// rule of thumb — generous headroom under any modern model's context
	// window while keeping token cost linear as a session grows.
	defaultHistoryCharBudget = 48000
	// defaultTurnTTL prunes completed turn records (and their event log) so
	// the short-lived in-flight collections don't grow unbounded. The
	// finished transcript also lives on the AskSession, so this only drops
	// the redundant turn-progress copy.
	defaultTurnTTL = 7 * 24 * time.Hour
	// defaultConnectTimeout bounds the lazy per-project build (warehouse
	// connect + ValidateReadOnly + schema load). Kept separate from the turn
	// budget so a misconfigured project fails fast instead of hanging a turn.
	defaultConnectTimeout = 60 * time.Second
)

// Config holds the serve-mode budgets and limits, all sourced from the
// environment with sane defaults.
type Config struct {
	Port int

	MaxFetchRows      int
	PreviewRows       int
	MaxQueriesPerTurn int
	MaxRounds         int

	WallClock    time.Duration
	QueryTimeout time.Duration

	PoolIdleTTL     time.Duration
	PoolMaxProjects int

	MaxConcurrentTurns      int
	MaxConcurrentPerProject int

	HistoryCharBudget int
	TurnTTL           time.Duration
	ConnectTimeout    time.Duration
}

// LoadConfig reads the ASK_SERVE_* environment into a Config, applying
// defaults for any unset or invalid value.
func LoadConfig() Config {
	return Config{
		Port:                    goconfig.GetEnvAsInt(EnvPort, defaultPort),
		MaxFetchRows:            positive(goconfig.GetEnvAsInt(EnvMaxFetchRows, defaultMaxFetchRows), defaultMaxFetchRows),
		PreviewRows:             positive(goconfig.GetEnvAsInt(EnvPreviewRows, defaultPreviewRows), defaultPreviewRows),
		MaxQueriesPerTurn:       positive(goconfig.GetEnvAsInt(EnvMaxQueriesPerTurn, defaultMaxQueriesPerTurn), defaultMaxQueriesPerTurn),
		MaxRounds:               positive(goconfig.GetEnvAsInt(EnvMaxRounds, defaultMaxRounds), defaultMaxRounds),
		WallClock:               durationFromSeconds(EnvWallClockSeconds, defaultWallClock),
		QueryTimeout:            durationFromSeconds(EnvQueryTimeoutSeconds, defaultQueryTimeout),
		PoolIdleTTL:             goconfig.GetEnvAsDuration(EnvPoolIdleTTL, defaultPoolIdleTTL),
		PoolMaxProjects:         positive(goconfig.GetEnvAsInt(EnvPoolMaxProjects, defaultPoolMaxProjects), defaultPoolMaxProjects),
		MaxConcurrentTurns:      positive(goconfig.GetEnvAsInt(EnvMaxConcurrentTurns, defaultMaxConcurrentTurns), defaultMaxConcurrentTurns),
		MaxConcurrentPerProject: positive(goconfig.GetEnvAsInt(EnvMaxConcurrentPerPrj, defaultMaxConcurrentPerPrj), defaultMaxConcurrentPerPrj),
		HistoryCharBudget:       positive(goconfig.GetEnvAsInt(EnvHistoryCharBudget, defaultHistoryCharBudget), defaultHistoryCharBudget),
		TurnTTL:                 durationFromSeconds(EnvTurnTTLSeconds, defaultTurnTTL),
		ConnectTimeout:          durationFromSeconds(EnvConnectTimeoutSecs, defaultConnectTimeout),
	}
}

// durationFromSeconds reads an integer-seconds env var (matching the rest of
// the agent's *_SECONDS knobs) and returns it as a Duration, falling back to
// def when unset or non-positive.
func durationFromSeconds(env string, def time.Duration) time.Duration {
	secs := goconfig.GetEnvAsInt(env, int(def/time.Second))
	if secs <= 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}

func positive(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
