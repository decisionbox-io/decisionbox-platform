package models

import (
	"math"
	"time"
)

// Discovery Ledger — the persistent, per-project investigation state that makes
// discovery compound (enterprise#261). The agent's end-of-run reflection phase
// writes these; the read path and the enterprise RAG/API read them, so run N+1
// builds on run N instead of re-treading it. The document types live here in
// libs/go-common so the agent (writer) and the enterprise API (reader) cannot
// drift on their BSON tags — the same reason DiscoveryQuestion lives here.

// LedgerFinding lifecycle statuses. A finding is carried forward with its status
// so the next run knows whether to drill it (confirmed/monitoring), re-check it
// (changed), or leave it (resolved/refuted).
const (
	LedgerFindingStatusConfirmed  = "confirmed"  // verified, still true
	LedgerFindingStatusMonitoring = "monitoring" // worth watching for change
	LedgerFindingStatusChanged    = "changed"    // same finding, different magnitude (a trend)
	LedgerFindingStatusResolved   = "resolved"   // no longer present
	LedgerFindingStatusRefuted    = "refuted"    // a later run disproved it
)

// LedgerFindingVectorType is the Qdrant `type` payload marker under which ledger
// findings are indexed in the shared decisionbox_<dims> collection, so the
// enterprise ledger retriever can filter to them (SearchOpts.Types). Shared here
// so the platform writer and the enterprise reader cannot drift on the value.
const LedgerFindingVectorType = "ledger_finding"

// LedgerTask kinds + statuses. Tasks are the open-thread / hypothesis queue the
// reflection phase emits to seed the next run.
const (
	LedgerTaskKindNextTask   = "next_task"
	LedgerTaskKindHypothesis = "hypothesis"

	LedgerTaskStatusOpen       = "open"
	LedgerTaskStatusInProgress = "in_progress"
	LedgerTaskStatusDone       = "done"
	LedgerTaskStatusDropped    = "dropped"
)

// DiscoveryLedger is the per-project investigation state: a coverage map over the
// warehouse plus a rolling convergence history. One document per project.
type DiscoveryLedger struct {
	ProjectID   string             `bson:"project_id" json:"project_id"`
	Coverage    LedgerCoverage     `bson:"coverage" json:"coverage"`
	Convergence []ConvergencePoint `bson:"convergence" json:"convergence"`
	UpdatedAt   time.Time          `bson:"updated_at" json:"updated_at"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
}

// LedgerCoverage summarizes what the investigation has touched, so the next run
// can steer toward the frontier instead of re-exploring covered ground.
type LedgerCoverage struct {
	// ExploredTables are fully-qualified tables (dataset.table) the agent has
	// queried across runs.
	ExploredTables []string `bson:"explored_tables" json:"explored_tables"`
	// AreaDepth maps analysis-area id -> a coarse "runs that produced findings
	// in this area" counter, so depth-first policy can chase the richest seam.
	AreaDepth map[string]int `bson:"area_depth" json:"area_depth"`
	// TotalTables is the size of the indexed catalog at last update, so a
	// frontier count (TotalTables - len(ExploredTables)) can be shown.
	TotalTables int `bson:"total_tables" json:"total_tables"`
	// Summary is a short natural-language coverage note the reflection phase
	// maintains ("orders + customers well covered; the events tables untouched").
	Summary string `bson:"summary" json:"summary"`
}

// ConvergencePoint is the marginal-new signal for one run: how much genuinely
// new the run added. A decaying MarginalRatio means the warehouse is tiled and
// the policy should shift breadth→depth or open a new area.
type ConvergencePoint struct {
	RunID         string    `bson:"run_id" json:"run_id"`
	NewFindings   int       `bson:"new_findings" json:"new_findings"`
	TotalFindings int       `bson:"total_findings" json:"total_findings"`
	MarginalRatio float64   `bson:"marginal_ratio" json:"marginal_ratio"`
	Date          time.Time `bson:"date" json:"date"`
}

// LedgerFinding is a durable, deduped finding carried across runs WITH substance
// — the finding + its key metric + evidence — plus a lifecycle status. This is
// the fix for today's names-only carry-forward (InsightSummary), which dropped
// description/evidence.
type LedgerFinding struct {
	ID          string `bson:"_id" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	Area        string `bson:"area" json:"area"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
	KeyMetric   string `bson:"key_metric,omitempty" json:"key_metric,omitempty"`
	Evidence    string `bson:"evidence,omitempty" json:"evidence,omitempty"`
	Severity    string `bson:"severity" json:"severity"`
	Status      string `bson:"status" json:"status"`

	AffectedCount int  `bson:"affected_count,omitempty" json:"affected_count,omitempty"`
	SeenCount     int  `bson:"seen_count" json:"seen_count"`
	Liked         bool `bson:"liked,omitempty" json:"liked,omitempty"`

	FirstSeen         time.Time `bson:"first_seen" json:"first_seen"`
	LastSeen          time.Time `bson:"last_seen" json:"last_seen"`
	SourceDiscoveryID string    `bson:"source_discovery_id,omitempty" json:"source_discovery_id,omitempty"`

	// NormalizedKey is a stable fingerprint of area+name for exact dedup. The
	// reflection phase's semantic-dedup path catches rewordings this misses.
	NormalizedKey string `bson:"normalized_key" json:"-"`

	// Embed* track Qdrant indexing state (enterprise RAG). Not part of any API
	// contract.
	EmbedStatus    string `bson:"embed_status,omitempty" json:"-"`
	EmbeddingModel string `bson:"embedding_model,omitempty" json:"-"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// Ranking weights — intrinsic to the ranking algorithm, not operator config.
// severity × recency × liked × recurrence × status, so the read path carries the
// most important findings instead of an arbitrary first-30 slice.
const (
	rankRecencyHalfLifeDays = 30.0 // a finding's recency weight halves every 30 days
	rankLikedBoost          = 1.5  // user marked the source insight useful
	rankRecurrenceStep      = 0.25 // per extra sighting
	rankRecurrenceMax       = 2.0  // cap on the recurrence multiplier
)

// Rank scores a finding for prompt inclusion, replacing the arbitrary
// first-30 truncation with severity × recency × liked × recurrence × status.
// Higher = more important to carry forward. `now` is a parameter so the
// function stays pure and testable.
func (f *LedgerFinding) Rank(now time.Time) float64 {
	sev := 1.0
	switch f.Severity {
	case "critical":
		sev = 4.0
	case "high":
		sev = 3.0
	case "medium":
		sev = 2.0
	case "low", "info":
		sev = 1.0
	}

	// Recency: exponential decay from LastSeen. A zero LastSeen (legacy row)
	// gets a neutral 1.0 rather than decaying to nothing.
	recency := 1.0
	if !f.LastSeen.IsZero() {
		days := now.Sub(f.LastSeen).Hours() / 24
		if days < 0 {
			days = 0
		}
		recency = math.Exp(-math.Ln2 * days / rankRecencyHalfLifeDays)
	}

	liked := 1.0
	if f.Liked {
		liked = rankLikedBoost
	}

	recurrence := 1.0
	if f.SeenCount > 1 {
		recurrence = 1.0 + rankRecurrenceStep*float64(f.SeenCount-1)
		if recurrence > rankRecurrenceMax {
			recurrence = rankRecurrenceMax
		}
	}

	// Status: resolved/refuted findings are done — keep them retrievable but
	// well below active ones so they rarely make the top slice.
	status := 1.0
	switch f.Status {
	case LedgerFindingStatusResolved:
		status = 0.2
	case LedgerFindingStatusRefuted:
		status = 0.1
	}

	return sev * recency * liked * recurrence * status
}

// LedgerTask is one open thread / hypothesis / next-task the reflection phase
// emits to seed the next run ("couldn't verify X → check next", "A⋈B looked
// anomalous → investigate", "table Z untouched").
type LedgerTask struct {
	ID        string `bson:"_id" json:"id"`
	ProjectID string `bson:"project_id" json:"project_id"`
	// Title is a short, plain-language label for the task a business user can
	// scan (e.g. "Check which sellers drive dead inventory"). Text keeps the
	// full, technical description (tables, metrics, the specific hypothesis).
	// Older tasks have no title; the UI falls back to Text.
	Title         string         `bson:"title,omitempty" json:"title,omitempty"`
	Text          string         `bson:"text" json:"text"`
	Kind          string         `bson:"kind" json:"kind"`
	Status        string         `bson:"status" json:"status"`
	LinkedTarget  QuestionTarget `bson:"linked_target,omitempty" json:"linked_target,omitempty"`
	CreatedRunID  string         `bson:"created_run_id,omitempty" json:"created_run_id,omitempty"`
	NormalizedKey string         `bson:"normalized_key" json:"-"`
	CreatedAt     time.Time      `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time      `bson:"updated_at" json:"updated_at"`
}

// NormalizedFindingKey builds the exact-dedup fingerprint for a finding from its
// area + name. Reuses the question normalizer (lower-case, collapse whitespace,
// drop punctuation) so trivial rewordings of the same finding collide.
func NormalizedFindingKey(area, name string) string {
	return NormalizedQuestionKey(area + " " + name)
}

// ValidLedgerFindingStatus reports whether s is a known finding status.
func ValidLedgerFindingStatus(s string) bool {
	switch s {
	case LedgerFindingStatusConfirmed, LedgerFindingStatusMonitoring,
		LedgerFindingStatusChanged, LedgerFindingStatusResolved, LedgerFindingStatusRefuted:
		return true
	}
	return false
}
