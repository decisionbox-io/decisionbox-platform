package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// The risk configuration the shipped packs carry, and the two ways it could
// ship wrong.
//
// It is editorial content -- a weight is a judgement about what a domain cares
// about -- so these guards do not check the numbers. They check that the
// numbers are legal, that the ids resolve, and that the two hand-maintained
// copies of the area mappings agree, which is the same relationship the prompt
// drift guard above covers for prose and the same way it has gone wrong twice.

// authoredArea is one entry of a domain-packs/<slug>/prompts/**/areas.json
// file: the authoring side of an analysis area.
type authoredArea struct {
	ID             string   `json:"id"`
	Priority       int      `json:"priority"`
	RiskCategories []string `json:"risk_categories"`
	SeverityWeight *float64 `json:"severity_weight"`
}

// authoredAreas reads every areas.json a pack's authoring tree holds, keyed by
// area id.
//
// Keyed by id rather than by file, because the same area id legitimately
// appears in two categories of one pack (system-test ships `type_mapping`
// under both `standard` and `thorough`) and the two must be mapped the same
// way -- which this keying asserts on the way past.
func authoredAreas(t *testing.T, slug string) map[string]authoredArea {
	t.Helper()
	src := filepath.Join(packSourceRoot, slug)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Skipf("no authored source at %s", src)
	}

	out := map[string]authoredArea{}
	err := filepath.Walk(filepath.Join(src, "prompts"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "areas.json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var areas []authoredArea
		if err := json.Unmarshal(raw, &areas); err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, area := range areas {
			if prev, dup := out[area.ID]; dup && !reflect.DeepEqual(prev, area) {
				t.Errorf("%s: area %q is authored twice with different risk configuration (%+v vs %+v); "+
					"one area id is one area, and a project that enables it gets one of the two",
					slug, area.ID, prev, area)
			}
			out[area.ID] = area
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", src, err)
	}
	if len(out) == 0 {
		t.Fatalf("found no authored areas for %s; the walk has stopped matching", slug)
	}
	return out
}

// TestSeedPacks_RiskConfigurationIsValid -- every shipped pack passes the same
// rules a hand-written or generated pack is held to.
//
// The seed loader deliberately does not validate (a failure there is a
// build-time mistake, and refusing to seed would leave a fresh install with no
// packs at all), so this is where an editorial weight of 4.0 or a mapping to a
// category nobody declared is caught: at build time, by name.
func TestSeedPacks_RiskConfigurationIsValid(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			pack := readSeed(t, slug).Pack
			if issues := models.RiskConfigIssues(&pack); len(issues) > 0 {
				t.Errorf("the shipped %s pack has invalid risk configuration:\n  %v", slug, issues)
			}
		})
	}
}

// TestSeedPacks_EveryAreaIsMappedToARiskCategory.
//
// Not required by the contract -- an unmapped area is legal and scores at the
// engine default -- but required of the packs this product ships. An area with
// no mapping carries no risk flags and can never be weighted, and a pack that
// maps five of its six areas is a pack whose sixth is invisible to every
// weight a customer configures. That is the failure the category grain was
// chosen to avoid, and nothing but this would report it.
func TestSeedPacks_EveryAreaIsMappedToARiskCategory(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			pack := readSeed(t, slug).Pack

			check := func(area models.PackAnalysisArea, where string) {
				if len(area.RiskCategories) == 0 {
					t.Errorf("%s area %q (%s) maps to no risk category, so no configured weight can ever "+
						"reach it and it carries no risk flags", slug, area.ID, where)
				}
			}
			for _, area := range pack.AnalysisAreas.Base {
				check(area, "base")
			}
			for _, cat := range sortedKeys(pack.AnalysisAreas.Categories) {
				for _, area := range pack.AnalysisAreas.Categories[cat] {
					check(area, "category "+cat)
				}
			}
		})
	}
}

// TestSeedPacks_AreaMappingsMatchTheirAuthoredSource is the drift guard, for
// the risk configuration rather than for the prose.
//
// The two trees are maintained by hand and nothing compiled or linted the
// relationship, which is how a prompt edit reached no install twice (#211,
// #240). An area mapping has the same shape of failure with a quieter symptom:
// the authoring file says a finding here is a compliance exposure, the shipped
// seed does not, and every project created from it is weighted as though the
// area were ordinary.
func TestSeedPacks_AreaMappingsMatchTheirAuthoredSource(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			authored := authoredAreas(t, slug)
			pack := readSeed(t, slug).Pack

			compare := func(area models.PackAnalysisArea, where string) {
				src, ok := authored[area.ID]
				if !ok {
					t.Errorf("%s ships area %q (%s) that no areas.json authors", slug, area.ID, where)
					return
				}
				if !reflect.DeepEqual(area.RiskCategories, src.RiskCategories) {
					t.Errorf("%s area %q: shipped risk_categories %v, authored %v — copy the authoring "+
						"file's mapping into seed/%s.json, otherwise it reaches no install",
						slug, area.ID, area.RiskCategories, src.RiskCategories, slug)
				}
				switch {
				case area.SeverityWeight == nil && src.SeverityWeight == nil:
				case area.SeverityWeight == nil || src.SeverityWeight == nil:
					t.Errorf("%s area %q: shipped severity_weight %v, authored %v",
						slug, area.ID, area.SeverityWeight, src.SeverityWeight)
				case *area.SeverityWeight != *src.SeverityWeight:
					t.Errorf("%s area %q: shipped severity_weight %v, authored %v",
						slug, area.ID, *area.SeverityWeight, *src.SeverityWeight)
				}
			}

			for _, area := range pack.AnalysisAreas.Base {
				compare(area, "base")
			}
			for _, cat := range sortedKeys(pack.AnalysisAreas.Categories) {
				for _, area := range pack.AnalysisAreas.Categories[cat] {
					compare(area, "category "+cat)
				}
			}

			// And the other direction: an authored mapping for an area no seed
			// ships is a mapping nobody reads.
			shipped := map[string]bool{}
			for _, area := range pack.AnalysisAreas.Base {
				shipped[area.ID] = true
			}
			for _, areas := range pack.AnalysisAreas.Categories {
				for _, area := range areas {
					shipped[area.ID] = true
				}
			}
			for id := range authored {
				if !shipped[id] {
					t.Errorf("%s authors area %q, which no seed ships", slug, id)
				}
			}
		})
	}
}

// TestSeedPacks_DeclaredCategoriesAreAllUsed -- a declared category that no
// area maps to is a weight a customer can set and nothing will read.
//
// The reverse direction (an area citing an id nothing declares) is a hard
// validation failure and is covered above; this one is legal, silent, and
// exactly as misleading, because the dashboard offers the category in its
// weight table either way.
func TestSeedPacks_DeclaredCategoriesAreAllUsed(t *testing.T) {
	for _, slug := range seedSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			pack := readSeed(t, slug).Pack
			if len(pack.RiskTaxonomy) == 0 {
				t.Skipf("%s declares no taxonomy", slug)
			}

			used := map[string]bool{}
			collect := func(areas []models.PackAnalysisArea) {
				for _, area := range areas {
					for _, id := range area.RiskCategories {
						used[id] = true
					}
				}
			}
			collect(pack.AnalysisAreas.Base)
			for _, areas := range pack.AnalysisAreas.Categories {
				collect(areas)
			}

			for _, cat := range pack.RiskTaxonomy {
				if !used[cat.ID] {
					t.Errorf("%s declares risk category %q at weight %v, and no area maps to it: "+
						"a customer can tune that weight and nothing will read it",
						slug, cat.ID, cat.Weight)
				}
			}
		})
	}
}

// TestSeedPacks_TheGlobalCategoriesAreUsedWhereTheyFit is a reminder with
// teeth, not a rule: the engine ships three cross-cutting ids, and a pack that
// maps nothing to any of them has either decided they do not apply or
// forgotten they exist.
//
// Asserted as a property of the SHIPPED SET rather than of each pack, because
// "no compliance finding exists in this domain" is a legitimate answer for one
// pack and not for all five.
func TestSeedPacks_TheGlobalCategoriesAreUsedWhereTheyFit(t *testing.T) {
	global := map[string]bool{}
	for _, id := range scoring.GlobalRiskCategories() {
		global[id] = true
	}

	usedGlobally := map[string]bool{}
	for _, slug := range seedSlugs(t) {
		pack := readSeed(t, slug).Pack
		collect := func(areas []models.PackAnalysisArea) {
			for _, area := range areas {
				for _, id := range area.RiskCategories {
					if global[id] {
						usedGlobally[id] = true
					}
				}
			}
		}
		collect(pack.AnalysisAreas.Base)
		for _, areas := range pack.AnalysisAreas.Categories {
			collect(areas)
		}
	}

	if len(usedGlobally) == 0 {
		t.Errorf("no shipped pack maps any area to the engine's cross-cutting categories (%v); "+
			"either they do not apply to any domain this product ships, or the mapping was forgotten",
			scoring.GlobalRiskCategories())
	}
}
