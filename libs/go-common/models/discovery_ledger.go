package models

import (
	"math"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
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
	// TotalTables is the number of tables in the indexed catalog at last
	// update, so a frontier count (TotalTables - len(ExploredTables)) can be
	// shown. It counts the project's TABLE-shaped datasources only: a
	// cube-shaped one has no tables to count, and folding its catalog in here
	// would present a combinatorial surface as a frontier to exhaust.
	TotalTables int `bson:"total_tables" json:"total_tables"`
	// ExploredCatalogItems are the catalog refs — metrics and dimensions — the
	// agent has queried on cube-shaped datasources across runs.
	//
	// Held apart from ExploredTables rather than merged into it because the two
	// are not the same kind of thing to a frontier. A table is tileable: cover
	// it and it is done, which is what makes "N of M" meaningful. A cube's
	// slices are combinatorial, so there is no M at which it is finished, and a
	// ratio built over one would read as a completion percentage for something
	// that never completes. This set is a record of what has already been
	// sliced, not a numerator.
	ExploredCatalogItems []string `bson:"explored_catalog_items,omitempty" json:"explored_catalog_items,omitempty"`
	// TotalCatalogItems is how many catalog items the run's cube-shaped
	// datasources offer. Non-zero is also the signal that this project HAS a
	// cube at all, which is what stops the coverage line from reporting an
	// empty frontier once every table is covered.
	TotalCatalogItems int `bson:"total_catalog_items,omitempty" json:"total_catalog_items,omitempty"`
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
	// UnrecordedFindings is how many of the run's findings never reached the
	// ledger — a failed write, or a consolidation that could not read what it
	// needed. Stored because NewFindings alone cannot tell "found nothing"
	// from "found things and lost them", and the second silently understates
	// every run that comes after: the next run builds on this ledger.
	UnrecordedFindings int `bson:"unrecorded_findings,omitempty" json:"unrecorded_findings,omitempty"`
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

	FirstSeen time.Time `bson:"first_seen" json:"first_seen"`
	LastSeen  time.Time `bson:"last_seen" json:"last_seen"`
	// SourceDiscoveryID is the MOST RECENT run that saw this finding — it is
	// overwritten on every merge. Sightings below is the per-run history.
	SourceDiscoveryID string `bson:"source_discovery_id,omitempty" json:"source_discovery_id,omitempty"`

	// --- Investigation-graph provenance (enterprise#389) ---
	//
	// The reflection phase holds all of this at write time and used to discard
	// it, which left the ledger able to say what is known and not where any of
	// it came from.

	// SourceInsightIDs are the insight ids this finding was built from. One id
	// reaches the narrative, the SQL, the quality caveats and the validation
	// verdict, because the same UUID keys the insight in the discovery
	// document, the standalone `insights` collection and the Qdrant point.
	SourceInsightIDs []string `bson:"source_insight_ids,omitempty" json:"source_insight_ids,omitempty"`
	// AnsweredTaskIDs are the tasks a run closed by pointing at this finding —
	// the reverse of LedgerTask.ResolvedByFindingIDs, written in the same pass
	// so the investigation tree is walkable from either end without a join.
	AnsweredTaskIDs []string `bson:"answered_task_ids,omitempty" json:"answered_task_ids,omitempty"`
	// Sightings is one row per run that observed this finding, most recent
	// last. SeenCount stays the honest total; this is what makes a magnitude
	// trend drawable instead of implied by a counter.
	Sightings []FindingSighting `bson:"sightings,omitempty" json:"sightings,omitempty"`
	// StatusHistory records WHY each status moved, and which run moved it.
	StatusHistory []FindingStatusChange `bson:"status_history,omitempty" json:"status_history,omitempty"`

	// Archived marks a finding the retention cap pushed out of the active set.
	// It is a soft archive, not a delete: once edges point at findings, a hard
	// prune silently breaks them, and a graph with dangling refs is worse than
	// a slightly larger collection. Archived findings are excluded from every
	// prompt and from the investigation view, and included — dimmed — in the
	// graph, because they are still real history. A later run that sees the
	// finding again un-archives it.
	Archived   bool      `bson:"archived,omitempty" json:"archived,omitempty"`
	ArchivedAt time.Time `bson:"archived_at,omitempty" json:"archived_at,omitempty"`

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

// FindingSighting is one run's observation of a finding — the row that makes a
// trend drawable instead of implied by a counter. The magnitude fields are
// copied as they were AT THAT RUN, because the finding document itself only
// ever carries the latest.
type FindingSighting struct {
	RunID         string    `bson:"run_id" json:"run_id"`
	DiscoveryID   string    `bson:"discovery_id" json:"discovery_id"`
	Date          time.Time `bson:"date" json:"date"`
	KeyMetric     string    `bson:"key_metric,omitempty" json:"key_metric,omitempty"`
	AffectedCount int       `bson:"affected_count,omitempty" json:"affected_count,omitempty"`
	Severity      string    `bson:"severity,omitempty" json:"severity,omitempty"`
	// InsightID is this run's insight that produced the sighting, so the
	// sighting itself drills through to the SQL that proved it.
	InsightID string `bson:"insight_id,omitempty" json:"insight_id,omitempty"`
}

// FindingStatusChange records WHY a status moved, with the run that moved it.
// Source distinguishes a model verdict from a server-detected trend and from a
// value derived by the one-shot backfill, so a reader can tell a judgement
// from an inference.
type FindingStatusChange struct {
	Status      string    `bson:"status" json:"status"`
	Reason      string    `bson:"reason,omitempty" json:"reason,omitempty"`
	RunID       string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	DiscoveryID string    `bson:"discovery_id,omitempty" json:"discovery_id,omitempty"`
	At          time.Time `bson:"at" json:"at"`
	Source      string    `bson:"source,omitempty" json:"source,omitempty"`
}

// Sources for FindingStatusChange.Source.
const (
	FindingStatusSourceReflection = "reflection" // the reflection model's verdict
	FindingStatusSourceTrend      = "trend"      // server-detected magnitude change
	FindingStatusSourceBackfill   = "backfill"   // derived by the one-shot backfill
)

// MaxFindingTrailEntries caps Sightings and StatusHistory, keeping the most
// recent entries. Both are appended once per run, so without a cap a
// long-lived finding grows its document without bound — the same reason the
// ledger's convergence history is capped. SeenCount keeps the honest total, so
// trimming the trail loses no count. Intrinsic to the document shape rather
// than operator config, like the ranking weights below.
const MaxFindingTrailEntries = 50

// TrimFindingTrail keeps the most recent MaxFindingTrailEntries of a trail.
// Generic over the two trail types so the two cannot drift apart.
func TrimFindingTrail[T FindingSighting | FindingStatusChange](trail []T) []T {
	if len(trail) <= MaxFindingTrailEntries {
		return trail
	}
	return trail[len(trail)-MaxFindingTrailEntries:]
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
	Title string `bson:"title,omitempty" json:"title,omitempty"`
	Text  string `bson:"text" json:"text"`
	Kind  string `bson:"kind" json:"kind"`
	// Supersedes is the id of the task this one grew out of — set when a run
	// answers a task and the follow-up question becomes a new task, so the
	// investigation thread stays connected and the next run doesn't re-tread.
	Supersedes   string         `bson:"supersedes,omitempty" json:"supersedes,omitempty"`
	Status       string         `bson:"status" json:"status"`
	LinkedTarget QuestionTarget `bson:"linked_target,omitempty" json:"linked_target,omitempty"`
	CreatedRunID string         `bson:"created_run_id,omitempty" json:"created_run_id,omitempty"`

	// --- Investigation-graph provenance (enterprise#389) ---

	// OriginFindingIDs are the findings this thread grew out of. Supersedes
	// links a follow-up to its parent TASK; this is what attributes the ROOT of
	// a chain, which was previously unattributed. LinkedTarget is not the same
	// thing: it means "about", and the model usually omits it.
	OriginFindingIDs []string `bson:"origin_finding_ids,omitempty" json:"origin_finding_ids,omitempty"`
	// ResolvedByFindingIDs are the findings that answered this task. Set only
	// alongside a `done` — a dead end (`dropped`) has nothing to point at.
	ResolvedByFindingIDs []string `bson:"resolved_by_finding_ids,omitempty" json:"resolved_by_finding_ids,omitempty"`
	// PursuedInRunIDs are the runs that had this thread in front of them,
	// whether or not they closed it — the honest record of what was pursued.
	PursuedInRunIDs []string `bson:"pursued_in_run_ids,omitempty" json:"pursued_in_run_ids,omitempty"`

	// Close metadata: when the task closed, which run closed it, and why.
	ClosedRunID       string    `bson:"closed_run_id,omitempty" json:"closed_run_id,omitempty"`
	ClosedDiscoveryID string    `bson:"closed_discovery_id,omitempty" json:"closed_discovery_id,omitempty"`
	ClosedAt          time.Time `bson:"closed_at,omitempty" json:"closed_at,omitempty"`
	CloseReason       string    `bson:"close_reason,omitempty" json:"close_reason,omitempty"`

	NormalizedKey string    `bson:"normalized_key" json:"-"`
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at" json:"updated_at"`
}

// LedgerLearning is one durable, reusable thing a run learned about the
// warehouse or the domain — an opaque code decoded, a table's grain, a join
// that works. The reflection phase already produced these and wrote them
// straight into the project-context notes, with no run attribution, into a
// collection the ledger API never reads; so they were invisible on the
// investigation view. This is a second, attributed copy the ledger owns. The
// project-context write stays as it is — this is not a migration.
//
// Deduped on NormalizedKey so a learning repeated across runs bumps SeenCount
// and refreshes its attribution rather than duplicating.
type LedgerLearning struct {
	ID            string    `bson:"_id" json:"id"`
	ProjectID     string    `bson:"project_id" json:"project_id"`
	Category      string    `bson:"category,omitempty" json:"category,omitempty"`
	Note          string    `bson:"note" json:"note"`
	Relevance     float64   `bson:"relevance,omitempty" json:"relevance,omitempty"`
	RunID         string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	DiscoveryID   string    `bson:"discovery_id,omitempty" json:"discovery_id,omitempty"`
	SeenCount     int       `bson:"seen_count" json:"seen_count"`
	NormalizedKey string    `bson:"normalized_key" json:"-"`
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at" json:"updated_at"`
}

// NormalizedLearningKey builds the exact-dedup fingerprint for a learning from
// its category + note, with the same normalizer the finding and question keys
// use, so trivial rewordings collide.
func NormalizedLearningKey(category, note string) string {
	return NormalizedQuestionKey(category + " " + note)
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

// The reflection phase's wall-clock budget. Shared, because three places have
// to agree on it: the agent that spends it, and the two readers that decide how
// long to believe a `reflecting` entry an agent may have died holding — the
// investigation-graph backfill, and the ledger view that tells a user the
// ledger is still being updated. Two modules agreeing on a number by copying it
// is how they come to disagree on it.
const (
	ReflectionTimeoutEnv     = "DISCOVERY_REFLECTION_TIMEOUT"
	DefaultReflectionTimeout = 3 * time.Hour
)

// ReflectionTimeout is the budget this deployment runs the reflection phase
// with. The API process reads the same variable it forwards to the agent, so
// both sides of a deployment see one number.
func ReflectionTimeout() time.Duration {
	return config.GetEnvAsDuration(ReflectionTimeoutEnv, DefaultReflectionTimeout)
}

// ReflectionMarkerTTL is how long a `reflecting` entry is believed: the budget,
// plus an hour of slack. The agent puts the entry up before a run is stamped
// completed and refreshes it in place when the phase starts writing, so the
// budget is counted from the phase's start; the slack covers the hop before it
// (the clarifying questions, a few minutes) and an agent's clock disagreeing
// with the reader's. An agent killed mid-phase never takes its entry down, so
// without a ceiling one crash would announce an update in progress for ever.
func ReflectionMarkerTTL() time.Duration {
	return ReflectionTimeout() + time.Hour
}

// ReflectionMarker is one entry of a ledger's in-flight list, stored under
// mongodb.ReflectingKey: a run whose findings are still being folded into the
// ledger. Deliberately not a field of DiscoveryLedger — the agent saves that
// struct with a whole-document $set, which would write a stale copy of the list
// back over entries added since it was read.
type ReflectionMarker struct {
	RunID string    `bson:"run_id" json:"run_id"`
	Since time.Time `bson:"since" json:"since"`
}

// Fresh reports whether the entry is still to be believed at now.
func (m ReflectionMarker) Fresh(now time.Time) bool {
	return !m.Since.IsZero() && now.Sub(m.Since) < ReflectionMarkerTTL()
}

