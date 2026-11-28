package verifier

import (
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// The query contract for a SQL warehouse is the text each frame carried
// before the contract became a placeholder, character for character.
func TestQueryContract_SQLIsUnchanged(t *testing.T) {
	systems, err := loadSystemPrompts()
	if err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[valmodels.AgentMode]string{
		valmodels.ModeVerifier: "```json\n{\"query_warehouse\": \"SELECT col, COUNT(*) AS n FROM `dataset.table` GROUP BY col\"}\n```\n\n" +
			"`query_warehouse` is a BARE SQL STRING. NOT `{\"sql\": \"...\"}`, NOT `{\"query\": \"...\"}`. Just the SQL.\n",
		valmodels.ModeRefuter: "```json\n{\"query_warehouse\": \"SELECT * FROM `dataset.table` WHERE …\"}\n```\n\n" +
			"`query_warehouse` is a BARE SQL STRING. Not an object, not nested. Just the SQL.\n",
	} {
		got := renderSystemPrompt(systems[mode], mode, Bundle{Warehouse: WarehouseInfo{Dialect: "PostgreSQL"}}, 0.2, 30)
		if !strings.Contains(got, want) {
			t.Errorf("%s: the SQL query contract changed; want it to contain:\n%s", mode, want)
		}
	}
}

func TestQueryContract_NonSQLNamesTheLanguageAndShowsNoSQL(t *testing.T) {
	systems, err := loadSystemPrompts()
	if err != nil {
		t.Fatal(err)
	}
	b := Bundle{Warehouse: WarehouseInfo{Dialect: "Hypercube Request (JSON)", QueryLanguage: "Hypercube Request (JSON)", QueryGuide: "Write one JSON object."}}
	for _, mode := range []valmodels.AgentMode{valmodels.ModeVerifier, valmodels.ModeRefuter} {
		got := renderSystemPrompt(systems[mode], mode, b, 0.2, 30)
		for _, banned := range []string{"BARE SQL STRING", `"query_warehouse": "SELECT`, "Just the SQL"} {
			if strings.Contains(got, banned) {
				t.Errorf("%s: a non-SQL source is told %q", mode, banned)
			}
		}
		if !strings.Contains(got, "holding one request written in Hypercube Request (JSON) — not SQL") {
			t.Errorf("%s: the language is not named in the query contract", mode)
		}
		if !strings.Contains(got, "How to write a request in Hypercube Request (JSON):\n\nWrite one JSON object.") {
			t.Errorf("%s: the source's query guide is not shown", mode)
		}
		if strings.Contains(got, "{{") {
			t.Errorf("%s: unsubstituted placeholder", mode)
		}
	}
}
