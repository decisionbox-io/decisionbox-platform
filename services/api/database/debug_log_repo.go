package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// debugLogRaw mirrors the subset of `discovery_debug_logs` we project on
// read. `_id` is an ObjectId in Mongo but the API surface uses a hex string,
// so we decode into this type then convert in ListByRun.
type debugLogRaw struct {
	ID                 primitive.ObjectID `bson:"_id"`
	DiscoveryRunID     string             `bson:"discovery_run_id"`
	CreatedAt          time.Time          `bson:"created_at"`
	LogType            string             `bson:"log_type"`
	Component          string             `bson:"component"`
	Operation          string             `bson:"operation"`
	Phase              string             `bson:"phase"`
	Step               int                `bson:"step"`
	DurationMs         int64              `bson:"duration_ms"`
	Success            bool               `bson:"success"`
	SQLQuery           string             `bson:"sql_query"`
	QueryPurpose       string             `bson:"query_purpose"`
	RowCount           int                `bson:"row_count"`
	FixAttempts        int                `bson:"fix_attempts"`
	QueryError         string             `bson:"query_error"`
	LLMModel        string             `bson:"llm_model"`
	LLMResponse     string             `bson:"llm_response"`
	LLMInputTokens  int                `bson:"llm_input_tokens"`
	LLMOutputTokens int                `bson:"llm_output_tokens"`
	ErrorMessage       string             `bson:"error_message"`
}

// maxLLMResponseBytes caps the per-entry response snippet returned by
// the API. The debug-logs endpoint is polled every 2s by the dashboard;
// uncapped responses can be 20KB+ each, and 200 entries × 20KB = 4MB per
// poll. Capping at 4KB keeps a typical poll payload under 1MB while
// preserving enough context (a full SQL query, a decision JSON object, or
// an error message) to see what the agent is doing.
const maxLLMResponseBytes = 4096

// DebugLogRepository reads `discovery_debug_logs` entries written by the
// agent. The API never writes to this collection — it's append-only from the
// agent side.
type DebugLogRepository struct {
	col *mongo.Collection
}

func NewDebugLogRepository(db *DB) *DebugLogRepository {
	return &DebugLogRepository{col: db.Collection("discovery_debug_logs")}
}

// ListByRun returns debug log entries for a given discovery run, ordered by
// created_at ascending (oldest first — the UI appends rows at the bottom of
// a scrolling list). If `since` is non-zero, only entries created after it
// are returned — this is what the UI uses to poll for new events since the
// last tick. `limit` caps the result set; pass 0 for the default of 200.
//
// The returned shape is the public-safe `DebugLogEntry`, *not* the raw
// document: full Claude prompts/responses and raw query result rows stay in
// Mongo and never cross the API boundary.
func (r *DebugLogRepository) ListByRun(ctx context.Context, runID string, since time.Time, limit int) ([]models.DebugLogEntry, error) {
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	if limit <= 0 {
		limit = 200
	}

	filter := bson.M{"discovery_run_id": runID}
	if !since.IsZero() {
		filter["created_at"] = bson.M{"$gt": since}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}}).
		SetLimit(int64(limit)).
		// Project only the fields exposed by DebugLogEntry. This is a
		// second line of defence — even if the underlying document grows
		// new sensitive fields, the API won't leak them.
		SetProjection(bson.M{
			"_id":                  1,
			"discovery_run_id":     1,
			"created_at":           1,
			"log_type":             1,
			"component":            1,
			"operation":            1,
			"phase":                1,
			"step":                 1,
			"duration_ms":          1,
			"success":              1,
			"sql_query":            1,
			"query_purpose":        1,
			"row_count":            1,
			"fix_attempts":         1,
			"query_error":          1,
			"llm_model":         1,
			"llm_response":      1,
			"llm_input_tokens":  1,
			"llm_output_tokens": 1,
			"error_message":        1,
		})

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find debug logs: %w", err)
	}
	defer cursor.Close(ctx) //nolint:errcheck

	var raw []debugLogRaw
	if err := cursor.All(ctx, &raw); err != nil {
		return nil, fmt.Errorf("decode debug logs: %w", err)
	}

	out := make([]models.DebugLogEntry, len(raw))
	for i, d := range raw {
		response := d.LLMResponse
		if len(response) > maxLLMResponseBytes {
			response = response[:maxLLMResponseBytes] + "\n…[truncated]"
		}
		out[i] = models.DebugLogEntry{
			ID:                 d.ID.Hex(),
			DiscoveryRunID:     d.DiscoveryRunID,
			CreatedAt:          d.CreatedAt,
			LogType:            d.LogType,
			Component:          d.Component,
			Operation:          d.Operation,
			Phase:              d.Phase,
			Step:               d.Step,
			DurationMs:         d.DurationMs,
			Success:            d.Success,
			SQLQuery:           d.SQLQuery,
			QueryPurpose:       d.QueryPurpose,
			RowCount:           d.RowCount,
			FixAttempts:        d.FixAttempts,
			QueryError:         d.QueryError,
			LLMModel:        d.LLMModel,
			LLMResponse:     response,
			LLMInputTokens:  d.LLMInputTokens,
			LLMOutputTokens: d.LLMOutputTokens,
			ErrorMessage:       d.ErrorMessage,
		}
	}
	return out, nil
}
