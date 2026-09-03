package models

import (
	"testing"
	"time"
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
