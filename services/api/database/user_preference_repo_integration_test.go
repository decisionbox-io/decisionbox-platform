//go:build integration

package database

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// --- UserPreferenceRepository ---

func TestInteg_UserPref_GetMissingReturnsNil(t *testing.T) {
	ctx := context.Background()
	repo := NewUserPreferenceRepository(testDB)

	got, err := repo.Get(ctx, "nobody@example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(missing) = %+v, want nil", got)
	}
}

func TestInteg_UserPref_SetThenGet(t *testing.T) {
	ctx := context.Background()
	repo := NewUserPreferenceRepository(testDB)

	if err := repo.SetLocale(ctx, "alice-sub", "tr"); err != nil {
		t.Fatalf("SetLocale: %v", err)
	}
	got, err := repo.Get(ctx, "alice-sub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after SetLocale")
	}
	if got.Locale != "tr" {
		t.Errorf("Locale = %q, want tr", got.Locale)
	}
	if got.Sub != "alice-sub" {
		t.Errorf("Sub = %q, want alice-sub", got.Sub)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
}

func TestInteg_UserPref_UpsertKeepsSingleDoc(t *testing.T) {
	ctx := context.Background()
	repo := NewUserPreferenceRepository(testDB)
	col := testDB.Collection("user_preferences")

	for _, loc := range []string{"en", "tr", "en", "pt-BR"} {
		if err := repo.SetLocale(ctx, "bob-sub", loc); err != nil {
			t.Fatalf("SetLocale(%s): %v", loc, err)
		}
	}

	n, err := col.CountDocuments(ctx, bson.M{"sub": "bob-sub"})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if n != 1 {
		t.Errorf("doc count = %d, want 1 (upsert must update in place)", n)
	}

	got, err := repo.Get(ctx, "bob-sub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Locale != "pt-BR" {
		t.Errorf("Locale = %q, want pt-BR (last write wins)", got.Locale)
	}
}

func TestInteg_UserPref_ScopedPerSub(t *testing.T) {
	ctx := context.Background()
	repo := NewUserPreferenceRepository(testDB)

	if err := repo.SetLocale(ctx, "carol-sub", "tr"); err != nil {
		t.Fatalf("SetLocale carol: %v", err)
	}
	if err := repo.SetLocale(ctx, "dave-sub", "en"); err != nil {
		t.Fatalf("SetLocale dave: %v", err)
	}

	carol, _ := repo.Get(ctx, "carol-sub")
	dave, _ := repo.Get(ctx, "dave-sub")
	if carol.Locale != "tr" || dave.Locale != "en" {
		t.Errorf("cross-user leak: carol=%q dave=%q", carol.Locale, dave.Locale)
	}
}

// TestInteg_UserPref_UniqueIndexEnforced confirms the (sub) unique index the
// schema init creates rejects a second raw document for the same principal —
// the durable guarantee behind the upsert-in-place behaviour above.
func TestInteg_UserPref_UniqueIndexEnforced(t *testing.T) {
	ctx := context.Background()
	repo := NewUserPreferenceRepository(testDB)
	col := testDB.Collection("user_preferences")

	if err := repo.SetLocale(ctx, "erin-sub", "tr"); err != nil {
		t.Fatalf("SetLocale: %v", err)
	}
	_, err := col.InsertOne(ctx, bson.M{"sub": "erin-sub", "locale": "en"})
	if err == nil {
		t.Fatal("expected duplicate-key error inserting a second doc for the same sub")
	}
}
