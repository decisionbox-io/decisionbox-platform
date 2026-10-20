package models

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestLedgerFindingRank(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-24 * time.Hour)
	old := now.Add(-120 * 24 * time.Hour) // 120 days ago

	t.Run("severity dominates for equal recency", func(t *testing.T) {
		crit := (&LedgerFinding{Severity: "critical", Status: "confirmed", LastSeen: recent}).Rank(now)
		low := (&LedgerFinding{Severity: "low", Status: "confirmed", LastSeen: recent}).Rank(now)
		if crit <= low {
			t.Errorf("critical (%.3f) should outrank low (%.3f)", crit, low)
		}
	})

	t.Run("recency decays", func(t *testing.T) {
		fresh := (&LedgerFinding{Severity: "high", Status: "confirmed", LastSeen: recent}).Rank(now)
		stale := (&LedgerFinding{Severity: "high", Status: "confirmed", LastSeen: old}).Rank(now)
		if fresh <= stale {
			t.Errorf("fresh (%.3f) should outrank stale (%.3f)", fresh, stale)
		}
	})

	t.Run("liked and recurrence boost", func(t *testing.T) {
		base := (&LedgerFinding{Severity: "medium", Status: "confirmed", LastSeen: recent, SeenCount: 1}).Rank(now)
		liked := (&LedgerFinding{Severity: "medium", Status: "confirmed", LastSeen: recent, SeenCount: 1, Liked: true}).Rank(now)
		recurring := (&LedgerFinding{Severity: "medium", Status: "confirmed", LastSeen: recent, SeenCount: 5}).Rank(now)
		if liked <= base {
			t.Errorf("liked (%.3f) should outrank base (%.3f)", liked, base)
		}
		if recurring <= base {
			t.Errorf("recurring (%.3f) should outrank base (%.3f)", recurring, base)
		}
	})

	t.Run("recurrence multiplier is capped", func(t *testing.T) {
		many := (&LedgerFinding{Severity: "medium", Status: "confirmed", LastSeen: recent, SeenCount: 100}).Rank(now)
		atCap := (&LedgerFinding{Severity: "medium", Status: "confirmed", LastSeen: recent, SeenCount: 5}).Rank(now)
		if many != atCap {
			t.Errorf("recurrence should cap: seen=100 (%.3f) must equal seen=5-at-cap (%.3f)", many, atCap)
		}
	})

	t.Run("resolved and refuted rank far below active", func(t *testing.T) {
		active := (&LedgerFinding{Severity: "high", Status: "confirmed", LastSeen: recent}).Rank(now)
		resolved := (&LedgerFinding{Severity: "high", Status: LedgerFindingStatusResolved, LastSeen: recent}).Rank(now)
		refuted := (&LedgerFinding{Severity: "high", Status: LedgerFindingStatusRefuted, LastSeen: recent}).Rank(now)
		if resolved >= active || refuted >= active {
			t.Errorf("done findings should rank below active: active=%.3f resolved=%.3f refuted=%.3f", active, resolved, refuted)
		}
		if refuted >= resolved {
			t.Errorf("refuted (%.3f) should rank below resolved (%.3f)", refuted, resolved)
		}
	})

	t.Run("zero last_seen is neutral, not zero", func(t *testing.T) {
		if got := (&LedgerFinding{Severity: "medium", Status: "confirmed"}).Rank(now); got <= 0 {
			t.Errorf("a legacy finding with zero LastSeen should still rank > 0, got %.3f", got)
		}
	})
}

func TestNormalizedFindingKey(t *testing.T) {
	a := NormalizedFindingKey("Churn", "High churn in EU!")
	b := NormalizedFindingKey("churn", "high  churn in eu")
	if a != b {
		t.Errorf("trivial rewordings should collide: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("key should not be empty")
	}
}

// --- Investigation-graph fields (enterprise#389) ---

// TestLedgerFindingBSONRoundTrip covers the whole point of these types living
// in go-common: the agent writes them and the enterprise API reads them, so a
// drifted BSON tag is a silently empty field on the other side. Marshalling to
// BSON and back is the only check that actually catches that.
func TestLedgerFindingBSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 4, 2, 9, 30, 0, 0, time.UTC)
	in := LedgerFinding{
		ID:               "f-1",
		ProjectID:        "p-1",
		Area:             "inventory",
		Name:             "Dead stock concentrated in a few sellers",
		Severity:         "high",
		Status:           LedgerFindingStatusChanged,
		SeenCount:        3,
		FirstSeen:        at,
		LastSeen:         at.Add(48 * time.Hour),
		SourceInsightIDs: []string{"ins-1", "ins-2"},
		AnsweredTaskIDs:  []string{"t-1"},
		Sightings: []FindingSighting{
			{RunID: "r-1", DiscoveryID: "d-1", Date: at, KeyMetric: "units=1200", AffectedCount: 42, Severity: "medium", InsightID: "ins-1"},
			{RunID: "r-2", DiscoveryID: "d-2", Date: at.Add(48 * time.Hour), KeyMetric: "units=2600", AffectedCount: 91, Severity: "high", InsightID: "ins-2"},
		},
		StatusHistory: []FindingStatusChange{
			{Status: LedgerFindingStatusConfirmed, RunID: "r-1", DiscoveryID: "d-1", At: at, Source: FindingStatusSourceReflection, Reason: "verified against the orders table"},
			{Status: LedgerFindingStatusChanged, RunID: "r-2", DiscoveryID: "d-2", At: at.Add(48 * time.Hour), Source: FindingStatusSourceTrend, Reason: "affected count up 2.1x"},
		},
		Archived:   true,
		ArchivedAt: at.Add(72 * time.Hour),
	}

	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LedgerFinding
	if err := bson.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(in.SourceInsightIDs, out.SourceInsightIDs) {
		t.Errorf("source_insight_ids: got %v, want %v", out.SourceInsightIDs, in.SourceInsightIDs)
	}
	if !reflect.DeepEqual(in.AnsweredTaskIDs, out.AnsweredTaskIDs) {
		t.Errorf("answered_task_ids: got %v, want %v", out.AnsweredTaskIDs, in.AnsweredTaskIDs)
	}
	if len(out.Sightings) != 2 || out.Sightings[1].AffectedCount != 91 || out.Sightings[1].InsightID != "ins-2" {
		t.Errorf("sightings did not round-trip: %+v", out.Sightings)
	}
	if len(out.StatusHistory) != 2 || out.StatusHistory[1].Reason != "affected count up 2.1x" ||
		out.StatusHistory[1].Source != FindingStatusSourceTrend {
		t.Errorf("status_history did not round-trip: %+v", out.StatusHistory)
	}
	if !out.Archived || out.ArchivedAt.IsZero() {
		t.Errorf("archive flags did not round-trip: archived=%v at=%v", out.Archived, out.ArchivedAt)
	}
}

// TestLedgerFindingBSONOmitsEmptyGraphFields pins that a finding written by a
// deployment that has never produced any of these edges stores no keys for
// them. Every new field is `omitempty` precisely so the existing documents and
// the existing investigation-view response shape are untouched.
func TestLedgerFindingBSONOmitsEmptyGraphFields(t *testing.T) {
	raw, err := bson.Marshal(LedgerFinding{ID: "f-1", ProjectID: "p-1", Name: "n"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"source_insight_ids", "answered_task_ids", "sightings", "status_history",
		"archived", "archived_at",
	} {
		if _, present := doc[key]; present {
			t.Errorf("empty %q must be omitted, got %v", key, doc[key])
		}
	}
}

// TestLedgerTaskBSONRoundTrip is the task half of the same contract.
func TestLedgerTaskBSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 4, 2, 9, 30, 0, 0, time.UTC)
	in := LedgerTask{
		ID:                   "t-1",
		ProjectID:            "p-1",
		Title:                "Check whether those sellers share a fulfilment centre",
		Text:                 "Join sellers to fulfilment centres and compare the dead-stock share",
		Kind:                 LedgerTaskKindHypothesis,
		Status:               LedgerTaskStatusDone,
		Supersedes:           "t-0",
		OriginFindingIDs:     []string{"f-1"},
		ResolvedByFindingIDs: []string{"f-2", "f-3"},
		PursuedInRunIDs:      []string{"r-1", "r-2"},
		CreatedRunID:         "r-1",
		ClosedRunID:          "r-2",
		ClosedDiscoveryID:    "d-2",
		ClosedAt:             at,
		CloseReason:          "9 of 12 share the same centre",
	}
	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LedgerTask
	if err := bson.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in.OriginFindingIDs, out.OriginFindingIDs) ||
		!reflect.DeepEqual(in.ResolvedByFindingIDs, out.ResolvedByFindingIDs) ||
		!reflect.DeepEqual(in.PursuedInRunIDs, out.PursuedInRunIDs) {
		t.Errorf("edge arrays did not round-trip: %+v", out)
	}
	if out.ClosedRunID != "r-2" || out.ClosedDiscoveryID != "d-2" ||
		!out.ClosedAt.Equal(at) || out.CloseReason != "9 of 12 share the same centre" {
		t.Errorf("close metadata did not round-trip: %+v", out)
	}
}

// TestLedgerTaskBSONOmitsEmptyGraphFields — same reasoning as the finding side.
func TestLedgerTaskBSONOmitsEmptyGraphFields(t *testing.T) {
	raw, err := bson.Marshal(LedgerTask{ID: "t-1", ProjectID: "p-1", Text: "x", Status: LedgerTaskStatusOpen})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"origin_finding_ids", "resolved_by_finding_ids", "pursued_in_run_ids",
		"closed_run_id", "closed_discovery_id", "closed_at", "close_reason",
	} {
		if _, present := doc[key]; present {
			t.Errorf("empty %q must be omitted, got %v", key, doc[key])
		}
	}
}

// TestTrimFindingTrail covers the document bound. A finding seen on every run
// of a long-lived project would otherwise grow its document without limit;
// SeenCount keeps the honest total, so trimming the trail loses no count.
func TestTrimFindingTrail(t *testing.T) {
	t.Run("under the cap is untouched", func(t *testing.T) {
		in := make([]FindingSighting, 3)
		if got := TrimFindingTrail(in); len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
	})
	t.Run("at the cap is untouched", func(t *testing.T) {
		in := make([]FindingSighting, MaxFindingTrailEntries)
		if got := TrimFindingTrail(in); len(got) != MaxFindingTrailEntries {
			t.Errorf("len = %d, want %d", len(got), MaxFindingTrailEntries)
		}
	})
	t.Run("over the cap keeps the most recent", func(t *testing.T) {
		in := make([]FindingSighting, MaxFindingTrailEntries+5)
		for i := range in {
			in[i].RunID = "r-" + strconv.Itoa(i)
		}
		got := TrimFindingTrail(in)
		if len(got) != MaxFindingTrailEntries {
			t.Fatalf("len = %d, want %d", len(got), MaxFindingTrailEntries)
		}
		if got[0].RunID != "r-5" {
			t.Errorf("oldest kept = %q, want r-5 (the first 5 dropped)", got[0].RunID)
		}
		if want := "r-" + strconv.Itoa(MaxFindingTrailEntries+4); got[len(got)-1].RunID != want {
			t.Errorf("newest kept = %q, want %q", got[len(got)-1].RunID, want)
		}
	})
	t.Run("status history uses the same cap", func(t *testing.T) {
		in := make([]FindingStatusChange, MaxFindingTrailEntries+2)
		for i := range in {
			in[i].Status = LedgerFindingStatusConfirmed
			in[i].RunID = "r-" + strconv.Itoa(i)
		}
		got := TrimFindingTrail(in)
		if len(got) != MaxFindingTrailEntries || got[0].RunID != "r-2" {
			t.Errorf("status trail trim wrong: len=%d first=%q", len(got), got[0].RunID)
		}
	})
}

// TestRankIgnoresGraphFields pins acceptance criterion "Rank() unchanged": the
// prompt slice is ordered by the same score it was before any of this existed,
// so adding provenance cannot silently reorder what the next run reads.
func TestRankIgnoresGraphFields(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	bare := LedgerFinding{Severity: "high", Status: LedgerFindingStatusConfirmed, LastSeen: now.Add(-24 * time.Hour), SeenCount: 2}
	rich := bare
	rich.SourceInsightIDs = []string{"ins-1"}
	rich.AnsweredTaskIDs = []string{"t-1"}
	rich.Sightings = []FindingSighting{{RunID: "r-1"}, {RunID: "r-2"}}
	rich.StatusHistory = []FindingStatusChange{{Status: LedgerFindingStatusConfirmed}}
	rich.Archived = true
	if bare.Rank(now) != rich.Rank(now) {
		t.Errorf("Rank must ignore the graph fields: bare=%.6f rich=%.6f", bare.Rank(now), rich.Rank(now))
	}
}

// TestNormalizedLearningKey mirrors the finding-key test: a learning repeated
// across runs with trivial rewording must bump the existing row, not duplicate.
func TestNormalizedLearningKey(t *testing.T) {
	a := NormalizedLearningKey("Schema", "order_status 'C' means cancelled!")
	b := NormalizedLearningKey("schema", "order_status  'C' means cancelled")
	if a != b {
		t.Errorf("trivial rewordings should collide: %q vs %q", a, b)
	}
	if NormalizedLearningKey("", "") != "" {
		t.Error("an empty learning should produce an empty key, not a punctuation artefact")
	}
}
