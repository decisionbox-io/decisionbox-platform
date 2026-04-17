package models

import (
	"encoding/json"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestBookmarkList_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := BookmarkList{
		ID:          "list-1",
		ProjectID:   "p1",
		UserID:      "anonymous",
		Name:        "Retention ideas",
		Description: "churn + activation",
		Color:       "#2b7",
		CreatedAt:   now,
		UpdatedAt:   now,
		ItemCount:   5,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded BookmarkList
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Name != original.Name || decoded.UserID != original.UserID || decoded.ItemCount != original.ItemCount {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestBookmarkList_JSONOmitEmpty(t *testing.T) {
	minimal := BookmarkList{
		ID:        "list-1",
		ProjectID: "p1",
		UserID:    "alice",
		Name:      "L",
	}
	data, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{`"description"`, `"color"`} {
		if contains(s, field) {
			t.Errorf("empty %s should be omitted, got %s", field, s)
		}
	}
}

func TestBookmarkList_BSONSkipsItemCount(t *testing.T) {
	list := BookmarkList{
		ID:        "list-1",
		Name:      "L",
		UserID:    "alice",
		ItemCount: 42,
	}
	data, err := bson.Marshal(list)
	if err != nil {
		t.Fatalf("bson.Marshal: %v", err)
	}
	var raw bson.M
	if err := bson.Unmarshal(data, &raw); err != nil {
		t.Fatalf("bson.Unmarshal: %v", err)
	}
	if _, ok := raw["item_count"]; ok {
		t.Error("item_count is derived and must not be persisted to BSON")
	}
}

func TestBookmark_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := Bookmark{
		ID:          "bm-1",
		ListID:      "list-1",
		ProjectID:   "p1",
		UserID:      "alice",
		DiscoveryID: "d1",
		TargetType:  TargetTypeInsight,
		TargetID:    "i1",
		Note:        "follow up next week",
		CreatedAt:   now,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Bookmark
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.TargetType != original.TargetType || decoded.Note != original.Note {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestBookmark_OmitsEmptyNote(t *testing.T) {
	minimal := Bookmark{TargetType: TargetTypeInsight, TargetID: "i1"}
	data, _ := json.Marshal(minimal)
	if contains(string(data), `"note"`) {
		t.Errorf("empty note should be omitted: %s", data)
	}
}

func TestReadMark_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := ReadMark{
		ID:         "rm-1",
		ProjectID:  "p1",
		UserID:     "alice",
		TargetType: TargetTypeRecommendation,
		TargetID:   "r1",
		ReadAt:     now,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ReadMark
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !decoded.ReadAt.Equal(original.ReadAt) || decoded.TargetType != original.TargetType {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestIsValidTargetType(t *testing.T) {
	cases := map[string]bool{
		"insight":        true,
		"recommendation": true,
		"":               false,
		"step":           false,
		"INSIGHT":        false,
		"exploration":    false,
	}
	for in, want := range cases {
		if got := IsValidTargetType(in); got != want {
			t.Errorf("IsValidTargetType(%q) = %v, want %v", in, got, want)
		}
	}
}

// contains is a tiny local helper to avoid pulling in strings for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
