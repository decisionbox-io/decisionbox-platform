package askserve

import (
	"context"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// TestFormatSearch_LabelsNonTableHits is the guard on a hit being mistaken for
// a table. A metric rendered bare looks exactly like a table name, and the
// model then reaches for lookup_schema or writes a FROM clause against it —
// so surfacing catalog hits without saying what they are would be worse than
// not surfacing them at all, which is the state this replaced.
func TestFormatSearch_LabelsNonTableHits(t *testing.T) {
	out := formatSearch("traffic", []TaggedHit{
		{Table: "sessions", Kind: "metric", Blurb: "Sessions."},
		{Table: "country", Kind: "dimension", Blurb: "Country."},
	}, false)

	if !strings.Contains(out, "sessions [metric]") {
		t.Errorf("output = %q, want the metric labelled", out)
	}
	if !strings.Contains(out, "country [dimension]") {
		t.Errorf("output = %q, want the dimension labelled", out)
	}
}

// TestFormatSearch_TableRenderingIsUnchanged pins that the label is added only
// where there is something to say. A table-only turn must render exactly as it
// did before catalog hits existed.
func TestFormatSearch_TableRenderingIsUnchanged(t *testing.T) {
	out := formatSearch("users", []TaggedHit{
		{Table: "events.users", Blurb: "Users."},
	}, false)

	if strings.Contains(out, "[") {
		t.Errorf("output = %q, want no kind annotation on a table hit", out)
	}
	if !strings.Contains(out, "events.users — Users.") {
		t.Errorf("output = %q, want the original table rendering", out)
	}
}

// TestFormatSearch_LabelsSurviveTheMultiDatasourcePrefix: the datasource tag
// and the kind are both needed, and neither may displace the other.
func TestFormatSearch_LabelsSurviveTheMultiDatasourcePrefix(t *testing.T) {
	out := formatSearch("traffic", []TaggedHit{
		{DatasourceID: "ga", DatasourceLabel: "Analytics", Table: "sessions", Kind: "metric", Blurb: "Sessions."},
	}, true)

	if !strings.Contains(out, "[datasource: ga (Analytics)]") {
		t.Errorf("output = %q, want the datasource tag", out)
	}
	if !strings.Contains(out, "sessions [metric]") {
		t.Errorf("output = %q, want the kind alongside the datasource tag", out)
	}
}

// TestSearchSummary_RecordsKindOnlyWhenThereIsOne keeps a stored transcript
// from a table-only turn byte-identical to what it was before.
func TestSearchSummary_RecordsKindOnlyWhenThereIsOne(t *testing.T) {
	recs := searchSummary([]TaggedHit{
		{Table: "events.users", Blurb: "Users."},
		{Table: "sessions", Kind: "metric", Blurb: "Sessions."},
	})
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if _, ok := recs[0]["kind"]; ok {
		t.Errorf("a table record gained a kind key: %+v", recs[0])
	}
	if recs[1]["kind"] != "metric" {
		t.Errorf("recs[1][kind] = %v, want metric", recs[1]["kind"])
	}
}

// TestFormatSearch_EmptyResultDoesNotClaimTables: the source may have none, so
// saying "no matching tables" would be wrong about what was searched.
func TestFormatSearch_EmptyResultDoesNotClaimTables(t *testing.T) {
	out := formatSearch("anything", nil, false)
	if strings.Contains(out, "tables") {
		t.Errorf("output = %q, want no claim that tables were what was searched", out)
	}
}

// stubSchemaProvider is a per-datasource lookup provider returning fixed hits.
type stubSchemaProvider struct{ hits []ai.SearchHit }

func (s *stubSchemaProvider) Search(context.Context, string, int) ([]ai.SearchHit, error) {
	return s.hits, nil
}

func (s *stubSchemaProvider) Lookup(context.Context, []string) (ai.LookupResult, error) {
	return ai.LookupResult{}, nil
}

// TestSearchOne_CarriesTheKind covers the single-datasource and pinned path.
// It renders through the same formatter as a spanning turn, so dropping the
// kind here shows metrics and dimensions to the model as bare table names —
// and it does so on exactly the turns where a catalog source is most likely to
// be the one under discussion, because it was pinned.
func TestSearchOne_CarriesTheKind(t *testing.T) {
	router := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{
			"ga": &stubSchemaProvider{hits: []ai.SearchHit{
				{Table: "sessions", Kind: "metric", Blurb: "Sessions."},
				{Table: "events.users", Blurb: "Users."},
			}},
		},
		Primary: "ga",
	})

	hits, err := router.SearchOne(context.Background(), "ga", "traffic", 10)
	if err != nil {
		t.Fatalf("SearchOne() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Kind != "metric" {
		t.Errorf("hits[0].Kind = %q, want metric — the kind was dropped on the pinned path", hits[0].Kind)
	}
	if hits[1].Kind != "" {
		t.Errorf("hits[1].Kind = %q, want empty for a table", hits[1].Kind)
	}

	// And it must survive all the way to what the model reads.
	out := formatSearch("traffic", hits, false)
	if !strings.Contains(out, "sessions [metric]") {
		t.Errorf("rendered output = %q, want the metric labelled", out)
	}
}
