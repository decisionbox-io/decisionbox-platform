package handler

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// packSourceRoot is domain-packs/ relative to this package. Go runs a test
// with its package directory as the working directory, so this resolves from
// services/api/internal/handler back to the repo root.
const packSourceRoot = "../../../../domain-packs"

func seedSlugs(t *testing.T) []string {
	t.Helper()
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		t.Fatalf("read embedded seed dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	if len(out) == 0 {
		t.Fatal("no embedded seed packs found")
	}
	sort.Strings(out)
	return out
}

func readSeed(t *testing.T, slug string) portableFormat {
	t.Helper()
	data, err := seedFS.ReadFile("seed/" + slug + ".json")
	if err != nil {
		t.Fatalf("read seed %s: %v", slug, err)
	}
	var p portableFormat
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("parse seed %s: %v", slug, err)
	}
	return p
}

// shippedPrompt pairs one prompt string a seed ships with the authored file it
// must equal. A pack ships 11-12 of these, not the 3 base ones: the category
// exploration contexts SeedProjectPrompts appends, and every analysis area's
// inline prompt. Enumerating them once means a guard added below covers the
// whole shipped surface instead of the corner of it that was easiest to reach.
type shippedPrompt struct {
	label string // "base/exploration", "categories/casual/analysis_economy", ...
	path  string // the authored source it must equal
	text  string
}

// shippedPrompts enumerates every prompt string in one seed alongside its
// authored path, or skips the pack when it has no authored source (the
// cube-shaped pack is generated elsewhere).
func shippedPrompts(t *testing.T, slug string) []shippedPrompt {
	t.Helper()
	src := filepath.Join(packSourceRoot, slug)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Skipf("no authored source at %s", src)
	}
	pack := readSeed(t, slug).Pack

	var out []shippedPrompt
	add := func(label, text string, pathParts ...string) {
		out = append(out, shippedPrompt{
			label: label,
			path:  filepath.Join(append([]string{src, "prompts"}, pathParts...)...),
			text:  text,
		})
	}

	add("base/base_context", pack.Prompts.Base.BaseContext, "base", "base_context.md")
	add("base/exploration", pack.Prompts.Base.Exploration, "base", "exploration.md")
	add("base/recommendations", pack.Prompts.Base.Recommendations, "base", "recommendations.md")
	for _, area := range pack.AnalysisAreas.Base {
		add("base/analysis_"+area.ID, area.Prompt, "base", "analysis_"+area.ID+".md")
	}

	// Map iteration order is random and these drive subtests, so sort for a
	// stable -run selector and a stable failure order.
	for _, cat := range sortedKeys(pack.Prompts.Categories) {
		if ctx := pack.Prompts.Categories[cat].ExplorationContext; ctx != "" {
			add("categories/"+cat+"/exploration_context", ctx, "categories", cat, "exploration_context.md")
		}
	}
	for _, cat := range sortedKeys(pack.AnalysisAreas.Categories) {
		for _, area := range pack.AnalysisAreas.Categories[cat] {
			add("categories/"+cat+"/analysis_"+area.ID, area.Prompt, "categories", cat, "analysis_"+area.ID+".md")
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assembledExplorations returns the exploration prompts as a project actually
// receives them: SeedProjectPrompts joins the base prompt with the selected
// category's context, so the two halves are one unit at runtime and a rule
// about the whole prompt has to be checked on the whole prompt.
func assembledExplorations(t *testing.T, slug string) map[string]string {
	t.Helper()
	pack := readSeed(t, slug).Pack
	out := make(map[string]string, len(pack.Prompts.Categories)+1)
	out["(no category)"] = pack.Prompts.Base.Exploration
	for cat, cp := range pack.Prompts.Categories {
		if cp.ExplorationContext == "" {
			continue
		}
		out[cat] = pack.Prompts.Base.Exploration + "\n\n" + cp.ExplorationContext
	}
	return out
}

// The prompts an install runs come from the embedded seeds, which
// SeedBuiltInPacks writes to MongoDB on every API startup — NOT from
// domain-packs/. The two trees are maintained by hand and nothing compiled,
// tested or linted the relationship, so a forgotten regeneration looked
// exactly like a completed one. It has gone wrong twice:
//
//	#211 bbe5bf4  wire SQL dialect + dialect-correct table refs  (prompts not shipped)
//	#240 f14647a  harden recommendation related_insight_ids      (prompts not shipped)
//
// Both sat unshipped for months. This test is what makes the next one loud:
// edit a prompt under domain-packs/ without updating the matching seed and it
// fails, naming the pack and the prompt.
func TestSeedPacks_PromptsMatchTheirAuthoredSource(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			for _, sp := range shippedPrompts(t, slug) {
				authored, err := os.ReadFile(sp.path)
				if err != nil {
					t.Errorf("read %s: %v", sp.path, err)
					continue
				}
				if sp.text != string(authored) {
					t.Errorf("the shipped %s prompt has drifted from %s\n"+
						"copy the authored text into seed/%s.json — otherwise the change does not reach any install\n"+
						"(shipped %d bytes, authored %d bytes)",
						sp.label, sp.path, slug, len(sp.text), len(authored))
				}
			}
		})
	}
}

// A shipped prompt must not hardcode one dialect's identifier quoting. Table
// refs go through {{REF:table}}, which substituteDialectTokens renders with
// the connected provider's QuoteRef — backticks for BigQuery/Databricks,
// "double quotes" for Postgres/Redshift/Snowflake, [brackets] for SQL Server.
// Five of the six shipped providers are not BigQuery, and #211 measured the
// cost of getting this wrong at ~30 extra LLM round-trips per run.
func TestSeedPacks_DoNotHardcodeDialectQuoting(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			for _, sp := range shippedPrompts(t, slug) {
				if n := strings.Count(sp.text, "FROM `"); n > 0 {
					t.Errorf("%s: %d backtick-quoted table ref(s); "+
						"use {{REF:table}} so the connected warehouse's quoting is applied", sp.label, n)
				}
			}
			// Checked on the assembled prompt, not per file: a category context
			// uses {{REF:}} and leaves {{DIALECT}} to the base half it is
			// appended to, which is correct — the model reads one prompt.
			for cat, ex := range assembledExplorations(t, slug) {
				// A table-shaped pack writes SQL, so it must state its dialect.
				// The cube-shaped pack queries metrics/dimensions and writes none.
				if strings.Contains(ex, "{{REF:") && !strings.Contains(ex, "{{DIALECT}}") {
					t.Errorf("the exploration prompt for category %q uses {{REF:}} but never states "+
						"{{DIALECT}}, so the model is told how to quote but not which SQL to write", cat)
				}
			}
		})
	}
}

// {{REF:table}} renders with the provider's quoting, and for Postgres /
// Redshift / Snowflake that quoting is the double quote — the same character
// that delimits a JSON string. Inside a ```json example the rendered line
// becomes
//
//	"query": "SELECT ... FROM "shop"."table" ..."
//
// which does not parse. The exploration loop requires exactly one JSON object
// per turn, so a malformed example sits in the worst possible place: the
// template the model copies. Teach dialect quoting in prose and in SQL fences
// (where it renders fine) and keep it out of the JSON envelope examples.
func TestSeedPacks_DialectRefsStayOutOfJSONExamples(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			for _, sp := range shippedPrompts(t, slug) {
				inJSON := false
				for i, line := range strings.Split(sp.text, "\n") {
					if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") {
						inJSON = strings.HasPrefix(trimmed, "```json")
						continue
					}
					if inJSON && strings.Contains(line, "{{REF:") {
						t.Errorf("%s line %d puts {{REF:}} inside a JSON example, "+
							"which renders as unparseable JSON on a double-quote dialect:\n\t%s",
							sp.label, i+1, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// The guards above iterate shippedPrompts, so an enumeration that quietly
// returned nothing would make all three pass while checking nothing — the
// failure mode that hid #211 and #240 in the first place, one level up.
//
// This asserts the mapping is a bijection: every authored .md under
// domain-packs/<slug>/prompts/ is shipped by exactly one seed string, and
// every shipped string has an author. The second direction is the drift test's
// read error; the first is its own bug, and a quiet one — a prompt someone
// wrote, reviewed and merged that reaches no install at all.
func TestSeedPacks_EveryAuthoredPromptIsShipped(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			shipped := make(map[string]string, 16)
			for _, sp := range shippedPrompts(t, slug) {
				if prev, dup := shipped[sp.path]; dup {
					t.Errorf("%s and %s both ship %s", prev, sp.label, sp.path)
				}
				shipped[sp.path] = sp.label
			}
			if len(shipped) == 0 {
				t.Fatal("enumerated no prompts, so every guard over them is vacuous")
			}

			root := filepath.Join(packSourceRoot, slug, "prompts")
			var authored int
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
					return err
				}
				authored++
				if _, ok := shipped[path]; !ok {
					t.Errorf("%s is authored but no seed string ships it, so it reaches no install", path)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", root, err)
			}
			if authored != len(shipped) {
				t.Errorf("%d authored prompt file(s) but %d shipped string(s)", authored, len(shipped))
			}
		})
	}
}
