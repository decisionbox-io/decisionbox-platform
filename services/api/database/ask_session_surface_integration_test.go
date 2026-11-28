//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The surface filter's whole difficulty is that the Ask surface is spelled
// three ways on disk — the field absent, present but empty, and "ask" — and a
// mock that reimplements the predicate proves only that the reimplementation
// agrees with itself. These run against real Mongo and a real missing field.
func TestInteg_AskSessions_SurfaceFilter(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)
	const project = "surface-proj"

	for _, s := range []*commonmodels.AskSession{
		{ID: "sf-explicit-ask", ProjectID: project, UserID: "alice", Title: "an Ask question", Surface: commonmodels.AskSurfaceAsk},
		{ID: "sf-playbook-1", ProjectID: project, UserID: "alice", Title: "add an area", Surface: commonmodels.AskSurfacePlaybook},
		{ID: "sf-playbook-2", ProjectID: project, UserID: "alice", Title: "merge two areas", Surface: commonmodels.AskSurfacePlaybook},
		{ID: "sf-other-project", ProjectID: project + "-other", UserID: "alice", Title: "elsewhere", Surface: commonmodels.AskSurfacePlaybook},
	} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}

	// A row as a pre-surface build wrote it: no surface field at all. Created
	// through the model (so omitempty drops it) and then asserted, because the
	// nil branch of the predicate is the only thing that can match it.
	legacy := &commonmodels.AskSession{ID: "sf-legacy", ProjectID: project, UserID: "alice", Title: "before surfaces", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	var raw bson.M
	if err := testDB.Collection("ask_sessions").FindOne(ctx, bson.M{"_id": "sf-legacy"}).Decode(&raw); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if _, present := raw["surface"]; present {
		t.Fatalf("fixture is wrong: the legacy row carries a surface field: %v", raw)
	}

	// And one written with an explicit empty string, which is what a client
	// that sends the field but leaves it blank would produce.
	if _, err := testDB.Collection("ask_sessions").InsertOne(ctx, bson.M{
		"_id": "sf-empty", "project_id": project, "user_id": "alice",
		"title": "blank surface", "surface": "", "messages": bson.A{},
		"message_count": 0, "created_at": time.Now(), "updated_at": time.Now(),
	}); err != nil {
		t.Fatalf("seed empty-surface row: %v", err)
	}

	ids := func(t *testing.T, surface string) map[string]bool {
		t.Helper()
		sessions, err := repo.ListByProject(ctx, project, "alice", 50, "", "", surface)
		if err != nil {
			t.Fatalf("ListByProject(%q): %v", surface, err)
		}
		out := map[string]bool{}
		for _, s := range sessions {
			out[s.ID] = true
		}
		return out
	}

	for _, ask := range []string{"", commonmodels.AskSurfaceAsk} {
		got := ids(t, ask)
		for _, want := range []string{"sf-legacy", "sf-empty", "sf-explicit-ask"} {
			if !got[want] {
				t.Errorf("surface %q: %s missing from the Ask list: %v", ask, want, got)
			}
		}
		for _, unwanted := range []string{"sf-playbook-1", "sf-playbook-2"} {
			if got[unwanted] {
				t.Errorf("surface %q: playbook session %s leaked into the Ask list", ask, unwanted)
			}
		}
		if len(got) != 3 {
			t.Errorf("surface %q: listed %d sessions, want 3: %v", ask, len(got), got)
		}
	}

	got := ids(t, commonmodels.AskSurfacePlaybook)
	if len(got) != 2 || !got["sf-playbook-1"] || !got["sf-playbook-2"] {
		t.Errorf("playbook list = %v, want exactly the two playbook sessions in this project", got)
	}

	// The surface survives the projection — a client that lists a surface has
	// to be able to see which one each row is on.
	sessions, err := repo.ListByProject(ctx, project, "alice", 50, "", "", commonmodels.AskSurfacePlaybook)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	for _, s := range sessions {
		if s.Surface != commonmodels.AskSurfacePlaybook {
			t.Errorf("session %s: Surface = %q, want %q (projection drops it?)", s.ID, s.Surface, commonmodels.AskSurfacePlaybook)
		}
	}

	// An unknown surface lists nothing rather than falling back to Ask.
	if got := ids(t, "no-such-surface"); len(got) != 0 {
		t.Errorf("unknown surface listed %v, want nothing", got)
	}
}
