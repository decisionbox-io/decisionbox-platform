package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// The API DiscoveryResult / ExplorationStep are read-only mirrors of the agent's
// documents. The multi-warehouse warehouse_id must survive decode so the
// discovery endpoints don't silently drop the datasource attribution.
func TestDiscovery_WarehouseIDDecodesFromAgentDoc(t *testing.T) {
	// A document as the agent writes it (warehouse_id set on the run + a step).
	raw, err := bson.Marshal(bson.M{
		"project_id":   "p1",
		"warehouse_id": "wh_b",
	})
	if err != nil {
		t.Fatalf("marshal raw discovery: %v", err)
	}
	var disc DiscoveryResult
	if err := bson.Unmarshal(raw, &disc); err != nil {
		t.Fatalf("unmarshal discovery: %v", err)
	}
	if disc.WarehouseID != "wh_b" {
		t.Errorf("DiscoveryResult.WarehouseID = %q, want wh_b (must not be dropped on decode)", disc.WarehouseID)
	}

	stepRaw, err := bson.Marshal(bson.M{"step": 1, "warehouse_id": "wh_b"})
	if err != nil {
		t.Fatalf("marshal raw step: %v", err)
	}
	var step ExplorationStep
	if err := bson.Unmarshal(stepRaw, &step); err != nil {
		t.Fatalf("unmarshal step: %v", err)
	}
	if step.WarehouseID != "wh_b" {
		t.Errorf("ExplorationStep.WarehouseID = %q, want wh_b", step.WarehouseID)
	}
}
