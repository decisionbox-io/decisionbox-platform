package qdrant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
)

// agentPointID replicates the agent's schema_retrieve.pointID formula verbatim.
// The schema editor's schemaPointID MUST match it byte-for-byte so an edited
// point overwrites the indexer's point in place — this test is the guard that
// the two implementations never drift.
func agentPointID(projectID, warehouseID, table string) string {
	wh := warehouseID
	if wh == "" {
		wh = "default"
	}
	sum := sha256.Sum256([]byte(projectID + "::" + wh + "::" + table))
	b := sum[:16]
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func TestSchemaPointID_MatchesAgentFormula(t *testing.T) {
	cases := []struct{ proj, wh, table string }{
		{"p1", "default", "dbo.orders"},
		{"p1", "", "dbo.orders"},         // empty warehouse normalises to default
		{"proj-2", "wh_b", "sales.line"}, // named secondary warehouse
	}
	for _, c := range cases {
		got := schemaPointID(c.proj, c.wh, c.table)
		want := agentPointID(c.proj, c.wh, c.table)
		if got != want {
			t.Errorf("schemaPointID(%q,%q,%q) = %q, want %q (drift from agent formula)", c.proj, c.wh, c.table, got, want)
		}
	}
}

func TestSchemaPointID_EmptyWarehouseEqualsDefault(t *testing.T) {
	if schemaPointID("p1", "", "t") != schemaPointID("p1", "default", "t") {
		t.Error("empty warehouse id must map to the reserved default")
	}
}

func TestSchemaPointID_ShapeAndDeterminism(t *testing.T) {
	id := schemaPointID("p1", "default", "dbo.orders")
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(id) {
		t.Errorf("point id %q is not UUID-shaped", id)
	}
	if id != schemaPointID("p1", "default", "dbo.orders") {
		t.Error("point id must be deterministic")
	}
	if id == schemaPointID("p1", "default", "dbo.customers") {
		t.Error("different tables must yield different ids")
	}
}

func TestNormSchemaWarehouseID(t *testing.T) {
	if normSchemaWarehouseID("") != schemaDefaultWarehouseID {
		t.Error("empty → default")
	}
	if normSchemaWarehouseID("wh_b") != "wh_b" {
		t.Error("named id preserved")
	}
}

func TestIsMissingCollection(t *testing.T) {
	if !isMissingCollection(fmt.Errorf("Not found: collection foo")) {
		t.Error("should match 'Not found'")
	}
	if !isMissingCollection(fmt.Errorf("collection doesn't exist")) {
		t.Error("should match \"doesn't exist\"")
	}
	if isMissingCollection(nil) || isMissingCollection(fmt.Errorf("some other error")) {
		t.Error("should not match nil / unrelated errors")
	}
}
