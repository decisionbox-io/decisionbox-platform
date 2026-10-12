package handler

import (
	"encoding/json"
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
			src := filepath.Join(packSourceRoot, slug)
			if _, err := os.Stat(src); os.IsNotExist(err) {
				// Generated elsewhere — the cube-shaped pack has no source here.
				t.Skipf("no authored source at %s", src)
			}
			base := readSeed(t, slug).Pack.Prompts.Base
			for _, p := range []struct {
				file    string
				shipped string
			}{
				{"base_context.md", base.BaseContext},
				{"exploration.md", base.Exploration},
				{"recommendations.md", base.Recommendations},
			} {
				path := filepath.Join(src, "prompts", "base", p.file)
				authored, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("read %s: %v", path, err)
					continue
				}
				if p.shipped != string(authored) {
					t.Errorf("the shipped %s prompt has drifted from %s\n"+
						"copy the authored text into seed/%s.json — otherwise the change does not reach any install\n"+
						"(shipped %d bytes, authored %d bytes)",
						strings.TrimSuffix(p.file, ".md"), path, slug, len(p.shipped), len(authored))
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
			ex := readSeed(t, slug).Pack.Prompts.Base.Exploration
			if n := strings.Count(ex, "FROM `"); n > 0 {
				t.Errorf("%d backtick-quoted table ref(s) in the exploration prompt; "+
					"use {{REF:table}} so the connected warehouse's quoting is applied", n)
			}
			// A table-shaped pack writes SQL, so it must state its dialect. The
			// cube-shaped pack queries metrics/dimensions and writes none.
			if strings.Contains(ex, "{{REF:") && !strings.Contains(ex, "{{DIALECT}}") {
				t.Error("the prompt uses {{REF:}} but never states {{DIALECT}}, " +
					"so the model is told how to quote but not which SQL to write")
			}
		})
	}
}
