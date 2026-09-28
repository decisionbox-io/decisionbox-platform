package discovery

// Offline check of the arm construction, run before any Bedrock call. Asserts the
// claim the experiment rests on: arms A and B differ in exactly one region, and both
// carry today's platform-enforced tail in full.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/discipline"
)

func TestS9PromptInvariants(t *testing.T) {
	for _, corpus := range []string{"s7", "s8"} {
		raw, err := os.ReadFile("/home/abacigil/tpch-lab/" + corpus + "/evidence/analysis.json")
		if err != nil {
			t.Skipf("corpus %s not on disk: %v", corpus, err)
		}
		var areas []frozenArea
		if err := json.Unmarshal(raw, &areas); err != nil {
			t.Fatal(err)
		}
		tail := "\n\n" + quantifierContract
		wantTail := discipline.AppendAnalysisRules(tail)
		for _, a := range areas {
			pa := armPrompt(t, a.Prompt, "A")
			pb := armPrompt(t, a.Prompt, "B")

			if !strings.HasSuffix(pa, wantTail) {
				t.Errorf("%s/%s: arm A does not end in today's contract+rules", corpus, a.AreaID)
			}
			if !strings.HasSuffix(pb, wantTail) {
				t.Errorf("%s/%s: arm B does not end in today's contract+rules", corpus, a.AreaID)
			}
			if len(pa) != len(pb) {
				t.Errorf("%s/%s: arm lengths differ (%d vs %d); a line swap cannot change length", corpus, a.AreaID, len(pa), len(pb))
			}
			// Exactly two lines may differ, and each must be the other's partner.
			la, lb := strings.Split(pa, "\n"), strings.Split(pb, "\n")
			if len(la) != len(lb) {
				t.Fatalf("%s/%s: line counts differ", corpus, a.AreaID)
			}
			var diff []int
			for i := range la {
				if la[i] != lb[i] {
					diff = append(diff, i)
				}
			}
			if len(diff) != 2 || diff[1] != diff[0]+1 {
				t.Errorf("%s/%s: %d differing lines at %v, want exactly 2 adjacent", corpus, a.AreaID, len(diff), diff)
				continue
			}
			i := diff[0]
			if la[i] != lb[i+1] || la[i+1] != lb[i] {
				t.Errorf("%s/%s: differing lines are not a swap of each other", corpus, a.AreaID)
			}
			if !strings.Contains(la[i], `"name":`) || !strings.Contains(la[i+1], `"description":`) {
				t.Errorf("%s/%s: swapped region is not the name/description pair", corpus, a.AreaID)
			}
			t.Logf("%s/%-20s ok: %d chars, swap at line %d", corpus, a.AreaID, len(pa), i)
		}
	}
}
