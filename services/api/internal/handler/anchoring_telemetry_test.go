package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// refusalRecord is one call the refusal recorder received.
type refusalRecord struct {
	at        string
	projectID string
	providers []string
}

// captureRefusals substitutes the recorder for the duration of a test. The
// site label is the part worth pinning: every call passes a string, and a
// copy-pasted one is wrong in a way nothing surfaces — the refusal still
// works, and the counts quietly attribute it to the wrong place.
func captureRefusals(t *testing.T) *[]refusalRecord {
	t.Helper()
	var got []refusalRecord
	prev := recordAnchoringRefusal
	recordAnchoringRefusal = func(at, projectID string, whs []models.WarehouseConfig) {
		got = append(got, refusalRecord{at, projectID, anchoringProviders(whs)})
	}
	t.Cleanup(func() { recordAnchoringRefusal = prev })
	return &got
}

func cube(id string) models.WarehouseConfig {
	no := false
	return models.WarehouseConfig{ID: id, Provider: "some-cube", Anchoring: &no}
}

// A refused configuration is recorded with the site that refused it.
func TestRejectUnanchoredProject_RecordsTheRefusalWithItsSite(t *testing.T) {
	got := captureRefusals(t)
	w := httptest.NewRecorder()

	if rejectUnanchoredProject(w, []models.WarehouseConfig{cube("wh_a")}, telemetry.AnchoringAtProjectCreate, "p1") {
		t.Fatal("an unanchored set was accepted")
	}
	if len(*got) != 1 {
		t.Fatalf("records = %+v, want exactly one", *got)
	}
	rec := (*got)[0]
	if rec.at != telemetry.AnchoringAtProjectCreate {
		t.Errorf("at = %q, want %q", rec.at, telemetry.AnchoringAtProjectCreate)
	}
	if rec.projectID != "p1" {
		t.Errorf("project_id = %q, want p1 — an operator asked why THIS project was refused", rec.projectID)
	}
	// Where there IS no project yet, the field is empty rather than
	// misleadingly present: the create path refuses before the insert that
	// assigns an id, so the providers are the whole attribution.
	*got = nil
	if rejectUnanchoredProject(httptest.NewRecorder(), []models.WarehouseConfig{cube("wh_a")}, telemetry.AnchoringAtProjectCreate, "") {
		t.Fatal("an unanchored set was accepted")
	}
	if len(*got) != 1 || (*got)[0].projectID != "" {
		t.Errorf("records = %+v, want one with no project id", *got)
	}
	if len(rec.providers) != 1 || rec.providers[0] != "some-cube" {
		t.Errorf("providers = %v, want the refused source's provider", rec.providers)
	}
}

// An accepted configuration records nothing. A counter that fires on the happy
// path measures traffic, not refusals, and the rule's calibration is exactly
// what it is supposed to measure.
func TestRejectUnanchoredProject_RecordsNothingWhenItAccepts(t *testing.T) {
	got := captureRefusals(t)

	for _, whs := range [][]models.WarehouseConfig{
		nil, // an empty set is unconfigured, not unanchored
		{{ID: "wh_a", Provider: "bigquery"}},
		{cube("wh_a"), {ID: "wh_b", Provider: "bigquery"}},
	} {
		if !rejectUnanchoredProject(httptest.NewRecorder(), whs, telemetry.AnchoringAtProjectCreate, "p1") {
			t.Fatalf("refused an acceptable set: %+v", whs)
		}
	}
	if len(*got) != 0 {
		t.Errorf("recorded %+v on sets that were accepted", *got)
	}
}

// A blank placeholder row is not a datasource anyone chose. Naming it would
// attribute the refusal to a source that does not exist.
func TestAnchoringProviders_SkipsPlaceholders(t *testing.T) {
	got := anchoringProviders([]models.WarehouseConfig{
		{ID: "wh_pending"}, {ID: "wh_a", Provider: "ga4"},
	})
	if len(got) != 1 || got[0] != "ga4" {
		t.Errorf("providers = %v, want just [ga4]", got)
	}
}
