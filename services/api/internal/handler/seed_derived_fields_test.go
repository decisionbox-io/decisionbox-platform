package handler

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The four fields the model used to author and the platform now derives:
// confidence from the validation verdict, severity and risk_score from the
// figures Go re-checks, and a recommendation's priority from the matrix over
// the two.
//
// Removing the ask is token hygiene rather than the mechanism -- the scorer
// clears and overwrites regardless, because a project snapshots its area
// prompts at creation and a pack's seeds are never overwritten on upgrade, so
// an existing deployment keeps asking indefinitely. But a prompt that still
// asks is a prompt in disagreement with the schema beside it and with the
// struct that stores the answer, and it spends tokens on a key the decoder
// ignores.
//
// This is checked in BOTH places on purpose. The embedded seeds are the copy
// that actually ships to a new project; the files under domain-packs/ are what
// a human edits. They have drifted before.
var derivedInsightFields = []string{"severity", "risk_score", "confidence"}

// derivedRecommendationFields adds the one that is only ever on a
// recommendation.
var derivedRecommendationFields = append([]string{"priority"}, derivedInsightFields...)

// jsonKeyAsk matches a field being asked for as a JSON key in a worked
// example -- `"severity": …` -- rather than the word appearing in prose. A
// prompt may legitimately explain that the platform derives severity; what it
// may not do is show the model a slot to put one in.
func jsonKeyAsk(field string) *regexp.Regexp {
	return regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:`)
}

func TestTheSeedPacksAskForNoDerivedField(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		pack := readSeed(t, slug).Pack
		t.Run(slug, func(t *testing.T) {
			checked := 0
			for name, body := range map[string]string{
				"base_context": pack.Prompts.Base.BaseContext,
				"exploration":  pack.Prompts.Base.Exploration,
			} {
				assertNoDerivedAsk(t, slug+"/"+name, body, derivedInsightFields)
				checked++
			}
			assertNoDerivedAsk(t, slug+"/recommendations",
				pack.Prompts.Base.Recommendations, derivedRecommendationFields)
			checked++

			// The analysis prompts, which is where the ask actually was: one
			// per area, inline on the area.
			for _, area := range pack.AnalysisAreas.Base {
				assertNoDerivedAsk(t, slug+"/analysis/"+area.ID, area.Prompt, derivedInsightFields)
				checked++
			}
			for category, areas := range pack.AnalysisAreas.Categories {
				for _, area := range areas {
					assertNoDerivedAsk(t, slug+"/"+category+"/"+area.ID, area.Prompt, derivedInsightFields)
					checked++
				}
			}
			if checked < 4 {
				t.Fatalf("only %d prompts were read from %s, so this proves nothing", checked, slug)
			}
		})
	}
}

// TestTheAuthoredPackFilesAskForNoDerivedField is the same assertion over the
// files a human edits, which is the half that goes stale: the seeds are
// generated from these, and nothing regenerates them on an edit.
func TestTheAuthoredPackFilesAskForNoDerivedField(t *testing.T) {
	root, err := filepath.Abs(packSourceRoot)
	if err != nil {
		t.Fatalf("resolving %s: %v", packSourceRoot, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("domain-packs/ is not present beside this module: %v", err)
	}

	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		fields := derivedInsightFields
		if strings.Contains(d.Name(), "recommendation") {
			fields = derivedRecommendationFields
		}
		assertNoDerivedAsk(t, rel, string(body), fields)
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walking the pack sources: %v", err)
	}
	if checked == 0 {
		t.Fatal("no pack prompt files were read, so every assertion over them is vacuous")
	}
}

func assertNoDerivedAsk(t *testing.T, where, body string, fields []string) {
	t.Helper()
	if strings.TrimSpace(body) == "" {
		return
	}
	for _, field := range fields {
		if jsonKeyAsk(field).MatchString(body) {
			t.Errorf("%s shows the model a %q key. The platform derives that value, "+
				"so an answer to it is discarded before anything reads it -- and the schema "+
				"sent on the same call no longer declares the property, which leaves the "+
				"prompt and the grammar telling the model two different things.", where, field)
		}
	}
}

// TestThePacksExplainWhatSeverityIsDerivedFrom -- removing the ask without
// replacing it would leave a model with no idea why its figures matter.
//
// The analysis prompts tell it the one thing that still changes the outcome: a
// finding that declares the number behind it gets ranked on that number, and
// one that declares nothing falls back to a baseline. That is a statement
// about the platform's behaviour rather than an instruction to self-assess,
// which is the distinction the whole change rests on.
func TestThePacksExplainWhatSeverityIsDerivedFrom(t *testing.T) {
	const explanation = "Severity is derived from the figures you declare"
	for _, slug := range seedSlugs(t) {
		pack := readSeed(t, slug).Pack
		explained, total := 0, 0
		for _, area := range pack.AnalysisAreas.Base {
			total++
			if strings.Contains(area.Prompt, explanation) {
				explained++
			}
		}
		if total == 0 {
			continue
		}
		if explained == 0 {
			t.Errorf("%s: none of its %d base analysis prompts says what severity is derived from, "+
				"so a model has no reason to declare the figure that decides it", slug, total)
		}
	}
}
