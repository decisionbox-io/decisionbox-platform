package models

import "time"

// Collection names for the asynchronous data-Q&A turn log. A turn is a
// single long-running, tool-using question handled by the agent's
// ask-serve mode: the serving process appends a tool event to
// CollectionAskTurnEvents as each step completes and updates the parent
// AskTurn in CollectionAskTurns, while a reader (the API) tails the event
// log by cursor — the same persist-and-poll shape discovery uses for its
// run steps. Kept as constants so the writer and the reader, which live in
// different modules, cannot drift on the collection names.
const (
	CollectionAskTurns      = "ask_turns"
	CollectionAskTurnEvents = "ask_turn_events"
)

// AskTurn lifecycle statuses. A turn starts Running and reaches exactly one
// terminal status. Terminal turns preserve whatever events were already
// persisted, so a partial transcript survives a timeout or a crash.
const (
	AskTurnStatusRunning  = "running"  // in flight
	AskTurnStatusDone     = "done"     // produced a grounded answer (or a clarifying question)
	AskTurnStatusDeclined = "declined" // the model declined to answer
	AskTurnStatusTimedOut = "timeout"  // hit the per-turn wall-clock budget
	AskTurnStatusFailed   = "failed"   // errored, or stranded by a serving-process restart
)

// AskTurn dispositions distinguish the three terminal shapes a completed
// turn can take. Disposition is advisory metadata for the reader's
// rendering; Status is the authority for "is this turn still running".
const (
	AskTurnDispositionAnswer  = "answer"
	AskTurnDispositionClarify = "clarify"
	AskTurnDispositionDecline = "decline"
)

// AskTurn is the durable record of a single asynchronous data-Q&A turn,
// stored in CollectionAskTurns keyed by the turn id. It is the analog of a
// discovery run: created when the turn is kicked off, updated in place as
// the serving process makes progress, and finalized with the answer when
// the turn reaches a terminal status. The incremental tool-event log lives
// in CollectionAskTurnEvents (one document per event) so the parent
// document stays small and a reader can poll new events by cursor.
type AskTurn struct {
	ID        string `bson:"_id" json:"id"`                // turn id (caller-supplied, e.g. a UUID)
	SessionID string `bson:"session_id" json:"session_id"` // the AskSession this turn belongs to
	ProjectID string `bson:"project_id" json:"project_id"`
	Question  string `bson:"question" json:"question"`

	Status      string `bson:"status" json:"status"`
	Disposition string `bson:"disposition,omitempty" json:"disposition,omitempty"`

	// Answer is the final grounded answer (or the clarifying question / decline
	// reason). Empty until the turn reaches a terminal status.
	Answer string `bson:"answer,omitempty" json:"answer,omitempty"`
	// Error is the user-facing reason a turn ended in failed/timeout. Empty otherwise.
	Error string `bson:"error,omitempty" json:"error,omitempty"`

	Model        string `bson:"model,omitempty" json:"model,omitempty"`
	InputTokens  int    `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int    `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`

	// Routing telemetry (multi-warehouse). RoutedDatasourceIDs are the
	// datasource (warehouse) ids the turn actually queried, in first-touched
	// order — the ground truth of where the answer's evidence came from.
	// RoutingReason / RoutingConfidence / RoutingClarify are filled by the
	// evidence-grounded router when it picks the datasource(s); they stay
	// empty/zero for a single-datasource project or a turn pinned to an
	// explicit datasource, where no routing decision is made.
	RoutedDatasourceIDs []string `bson:"routed_datasource_ids,omitempty" json:"routed_datasource_ids,omitempty"`
	RoutingReason       string   `bson:"routing_reason,omitempty" json:"routing_reason,omitempty"`
	RoutingConfidence   float64  `bson:"routing_confidence,omitempty" json:"routing_confidence,omitempty"`
	RoutingClarify      bool     `bson:"routing_clarify,omitempty" json:"routing_clarify,omitempty"`

	CreatedAt   time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `bson:"updated_at" json:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
}

// Terminal reports whether the turn has reached a terminal status — used by
// pollers to decide when to stop reading.
func (t AskTurn) Terminal() bool {
	switch t.Status {
	case AskTurnStatusDone, AskTurnStatusDeclined, AskTurnStatusTimedOut, AskTurnStatusFailed:
		return true
	default:
		return false
	}
}

// AskTurnEvent is one entry in a turn's incremental tool-event log, stored
// in CollectionAskTurnEvents (one document per event). The embedded
// ToolEvent is the same summary-only shape persisted on a finished
// AskSessionMessage, so the live transcript and the stored transcript
// render identically. The Mongo document _id (assigned by the writer) is
// the opaque, monotonic cursor a reader feeds back to fetch only newer
// events; it is added by each module's local document wrapper rather than
// here, to keep this package free of any storage-driver dependency.
type AskTurnEvent struct {
	TurnID    string    `bson:"turn_id" json:"turn_id"`
	ProjectID string    `bson:"project_id,omitempty" json:"project_id,omitempty"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`

	ToolEvent `bson:",inline" json:",inline"`
}
