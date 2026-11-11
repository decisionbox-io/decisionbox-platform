package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// The project-level lists are server-paged, which is what makes the default
// order a contract rather than a presentation choice: a client receives one
// page and cannot re-sort what it was not sent. Priority is that default now.

func TestTheListsDefaultToPriority(t *testing.T) {
	t.Run("insights", func(t *testing.T) {
		repo := &mockInsightRepo{}
		h := NewInsightsHandler(repo)
		req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/insights", nil)
		req.SetPathValue("id", "proj-1")
		w := httptest.NewRecorder()
		h.List(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if len(repo.sortsSeen) != 1 || repo.sortsSeen[0] != "" {
			t.Fatalf("sorts seen = %v, want one empty value the repo resolves to priority", repo.sortsSeen)
		}
	})

	t.Run("recommendations", func(t *testing.T) {
		repo := &mockRecommendationRepo{}
		h := NewRecommendationsHandler(repo)
		req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/recommendations", nil)
		req.SetPathValue("id", "proj-1")
		w := httptest.NewRecorder()
		h.List(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if len(repo.sortsSeen) != 1 || repo.sortsSeen[0] != "" {
			t.Fatalf("sorts seen = %v, want one empty value", repo.sortsSeen)
		}
	})
}

// TestTheOldOrderStaysReachable -- changing the default without leaving
// creation order available would silently reorder every existing client's
// pages, which is a behaviour change nobody asked for dressed up as a
// default.
func TestTheOldOrderStaysReachable(t *testing.T) {
	repo := &mockInsightRepo{}
	h := NewInsightsHandler(repo)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/insights?sort=created_at", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(repo.sortsSeen) != 1 || repo.sortsSeen[0] != database.SortCreatedAt {
		t.Fatalf("sorts seen = %v, want %q", repo.sortsSeen, database.SortCreatedAt)
	}
}

// TestAnUnknownSortIs400 -- not a quiet fall back to the default. A client
// that asked for an order it did not get is a client whose paging is wrong in
// a way it cannot detect, and a 200 with the rows in a different order than
// the one requested is worse than an error.
func TestAnUnknownSortIs400(t *testing.T) {
	for _, path := range []string{
		"/api/v1/projects/proj-1/insights?sort=severity",
		"/api/v1/projects/proj-1/insights?sort=PRIORITY",
		"/api/v1/projects/proj-1/insights?sort=-priority",
	} {
		repo := &mockInsightRepo{}
		h := NewInsightsHandler(repo)
		req := httptest.NewRequest("GET", path, nil)
		req.SetPathValue("id", "proj-1")
		w := httptest.NewRecorder()
		h.List(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
		if len(repo.sortsSeen) != 0 {
			t.Errorf("%s: the repo was queried anyway (%v)", path, repo.sortsSeen)
		}
	}
}

// TestThePrioritySortIsATotalOrder is the paging bug this would otherwise have
// shipped with.
//
// A whole run's documents share one `created_at` -- the agent stamps `now`
// once for the batch when it denormalises them -- and a run's findings
// routinely share a priority score, because the score is a tier plus two
// banded refinements. So a sort on the score alone, or even on the score and
// the timestamp, leaves large groups genuinely tied, and under limit/offset
// paging a tie means a document can come back on two pages or on none.
func TestThePrioritySortIsATotalOrder(t *testing.T) {
	order, err := database.ListSort("")
	if err != nil {
		t.Fatalf("the default sort does not resolve: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("the default sort has %d keys (%v); it needs a unique tie-break", len(order), order)
	}
	if order[0].Key != "scoring.priority_score" || order[0].Value != -1 {
		t.Errorf("first key = %v, want scoring.priority_score descending", order[0])
	}
	// `_id` is the only unique field on these collections, so it is what
	// makes the order total.
	last := order[len(order)-1]
	if last.Key != "_id" {
		t.Errorf("last key = %q, want _id; nothing else on this collection is unique", last.Key)
	}

	createdAt, err := database.ListSort(database.SortCreatedAt)
	if err != nil {
		t.Fatalf("the created_at sort does not resolve: %v", err)
	}
	if createdAt[len(createdAt)-1].Key != "_id" {
		t.Errorf("the created_at sort has no unique tie-break either: %v", createdAt)
	}
}

// TestAnUnknownSortNamesTheOnesItKnows -- the error reaches a client, so it
// has to say what to send instead.
func TestAnUnknownSortNamesTheOnesItKnows(t *testing.T) {
	_, err := database.ListSort("whatever")
	if err == nil {
		t.Fatal("an unknown sort resolved")
	}
	for _, want := range []string{database.SortPriority, database.SortCreatedAt} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
