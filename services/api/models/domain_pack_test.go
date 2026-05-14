package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// TestDomainPack_BSONRoundTrip locks the wire shape that MongoDB sees.
// A future rename that drops the input_tokens / output_tokens tags would
// silently lose data on packs already persisted by the enterprise pack
// generator — this test fails first.
func TestDomainPack_BSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	original := DomainPack{
		ID:           "pack-1",
		Slug:         "ecommerce",
		Name:         "Ecommerce",
		Description:  "Cart & order analytics",
		Version:      "0.1.0",
		IsPublished:  true,
		InputTokens:  18000,
		OutputTokens: 4200,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	bs, err := bson.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw bson.M
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if got, ok := raw["input_tokens"].(int32); !ok || int(got) != 18000 {
		t.Errorf("input_tokens key missing or wrong; raw=%v", raw["input_tokens"])
	}
	if got, ok := raw["output_tokens"].(int32); !ok || int(got) != 4200 {
		t.Errorf("output_tokens key missing or wrong; raw=%v", raw["output_tokens"])
	}

	var back DomainPack
	if err := bson.Unmarshal(bs, &back); err != nil {
		t.Fatalf("Unmarshal typed: %v", err)
	}
	if back.InputTokens != 18000 || back.OutputTokens != 4200 {
		t.Errorf("tokens round-trip lost: got (%d, %d), want (18000, 4200)", back.InputTokens, back.OutputTokens)
	}
}

// TestDomainPack_JSONOmitemptyOnZero — community filesystem-loaded packs
// (and any pack persisted before the enterprise pack generator wired
// token tracking) render the token fields as absent rather than 0.
// Distinguishes "unknown" from "zero spent" for the dashboard.
func TestDomainPack_JSONOmitemptyOnZero(t *testing.T) {
	p := DomainPack{ID: "p", Slug: "s", Name: "n", Version: "0.1.0"}
	bs, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(bs)
	if strings.Contains(got, `"input_tokens"`) {
		t.Errorf("input_tokens must be omitted when zero; got %s", got)
	}
	if strings.Contains(got, `"output_tokens"`) {
		t.Errorf("output_tokens must be omitted when zero; got %s", got)
	}
}

// TestDomainPack_JSONIncludesNonZero — the inverse: when either field is
// non-zero the wire shape carries it.
func TestDomainPack_JSONIncludesNonZero(t *testing.T) {
	p := DomainPack{ID: "p", Slug: "s", Name: "n", Version: "0.1.0", InputTokens: 9000, OutputTokens: 1200}
	bs, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(bs)
	if !strings.Contains(got, `"input_tokens":9000`) {
		t.Errorf("input_tokens=9000 missing; got %s", got)
	}
	if !strings.Contains(got, `"output_tokens":1200`) {
		t.Errorf("output_tokens=1200 missing; got %s", got)
	}
}
