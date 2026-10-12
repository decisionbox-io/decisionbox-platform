package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packSourceRoot is domain-packs/ relative to this package. Go runs a test
// with its package directory as the working directory, so this resolves from
// services/api/internal/handler back to the repo root.
const packSourceRoot = "../../../../domain-packs"

func loadSeedPacks(t *testing.T) map[string]portableFormat {
	t.Helper()
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		t.Fatalf("read embedded seed dir: %v", err)
	}
	out := make(map[string]portableFormat, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := seedFS.ReadFile("seed/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var p portableFormat
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = p
	}
	if len(out) == 0 {
		t.Fatal("no embedded seed packs found")
	}
	return out
}

// The prompts that reach an install come from these embedded seeds, not from
// domain-packs/. Nothing kept the two in step, so PR #211 fixed the authored
// exploration prompts and the shipped ones kept the old hardcoded-BigQuery
// text — the dialect work never reached a single install.
//
// Regenerate with: make regen-pack-seeds
func TestSeedPacks_MatchTheirAuthoredSource(t *testing.T) {
	for slug, pack := range loadSeedPacks(t) {
		t.Run(slug, func(t *testing.T) {
			src := filepath.Join(packSourceRoot, slug, "prompts", "base", "exploration.md")
			want, err := os.ReadFile(src)
			if os.IsNotExist(err) {
				// Generated elsewhere (the cube-shaped pack has no source here).
				t.Skipf("no authored source at %s", src)
			}
			if err != nil {
				t.Fatalf("read %s: %v", src, err)
			}
			got := pack.Pack.Prompts.Base.Exploration
			if got != string(want) {
				t.Errorf("the shipped exploration prompt has drifted from %s;\n"+
					"run `make regen-pack-seeds` and commit the result\n"+
					"(shipped %d bytes, authored %d bytes)", src, len(got), len(want))
			}
		})
	}
}

// A shipped prompt must not hardcode one dialect's identifier quoting. Table
// refs go through {{REF:table}}, which substituteDialectTokens renders with
// the connected provider's QuoteRef — backticks for BigQuery/Databricks,
// "double quotes" for Postgres/Redshift/Snowflake, [brackets] for SQL Server.
// Five of the six shipped providers are not BigQuery.
func TestSeedPacks_DoNotHardcodeDialectQuoting(t *testing.T) {
	for slug, pack := range loadSeedPacks(t) {
		t.Run(slug, func(t *testing.T) {
			ex := pack.Pack.Prompts.Base.Exploration
			if n := strings.Count(ex, "FROM `"); n > 0 {
				t.Errorf("%d backtick-quoted table ref(s) in the exploration prompt; "+
					"use {{REF:table}} so the connected warehouse's quoting is applied", n)
			}
			// A table-shaped pack writes SQL, so it must state its dialect.
			// The cube-shaped pack queries metrics/dimensions and writes none.
			if strings.Contains(ex, "{{REF:") && !strings.Contains(ex, "{{DIALECT}}") {
				t.Error("the prompt uses {{REF:}} but never states {{DIALECT}}, " +
					"so the model is told how to quote but not which SQL to write")
			}
		})
	}
}
