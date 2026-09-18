package qdrant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
)

// Schema-editor point operations on the per-project schema collection
// (decisionbox_schema_{projectID}). These read/write individual table-blurb
// points so a user can correct a blurb or remove a table without a full
// re-index. They are NOT on the vectorstore.Provider interface — only the
// schema editor uses them, and adding them there would force every Provider
// mock in the codebase to implement them.
//
// The point id and payload shape MUST match the agent's schema indexer
// (services/agent/internal/ai/schema_retrieve/retrieve.go): an edited point has
// to overwrite the indexer's point in place, and search reads the same payload
// keys. schemaPointID / schemaDefaultWarehouseID below mirror that package's
// pointID / normWarehouseID / DefaultWarehouseID; the payload keys are the
// caller's responsibility (the schema-editor handler builds the map).

// schemaDefaultWarehouseID is the reserved id for the primary of a legacy /
// single-warehouse project. Mirrors models.DefaultWarehouseID and the agent's
// schema_retrieve.DefaultWarehouseID (kept local so this leaf package stays
// dependency-free).
const schemaDefaultWarehouseID = "default"

// normSchemaWarehouseID maps the empty id to the reserved default so point ids
// are stable for legacy / single-warehouse projects — same rule the agent
// applies on write.
func normSchemaWarehouseID(warehouseID string) string {
	if warehouseID == "" {
		return schemaDefaultWarehouseID
	}
	return warehouseID
}

// schemaPointID returns the deterministic UUID-shaped id for a (project,
// warehouse, table) schema point. Mirrors the agent's pointID exactly —
// SHA-256(projectID::warehouseID::table), first 16 bytes in UUID notation — so
// an edit overwrites the same point the indexer wrote.
func schemaPointID(projectID, warehouseID, table string) string {
	sum := sha256.Sum256([]byte(projectID + "::" + normSchemaWarehouseID(warehouseID) + "::" + table))
	b := sum[:16]
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// isMissingCollection reports whether an error is Qdrant's "collection not
// found" — treated as an empty/no-op result for schema-point ops (a project
// without a schema index, or whose collection was dropped by a cache clear).
func isMissingCollection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Not found") || strings.Contains(msg, "doesn't exist")
}

// GetSchemaPoints reads the current payloads for the given qualified tables from
// a project's schema collection, keyed by table. Tables without a point (e.g.
// blurb generation failed) are simply absent from the map. Returns an empty map
// when the project has no schema collection yet. Vectors are not fetched.
func (p *Provider) GetSchemaPoints(ctx context.Context, projectID, warehouseID string, tables []string) (map[string]vectorstore.SchemaPoint, error) {
	out := make(map[string]vectorstore.SchemaPoint, len(tables))
	if projectID == "" || len(tables) == 0 {
		return out, nil
	}
	wh := normSchemaWarehouseID(warehouseID)
	idToTable := make(map[string]string, len(tables))
	ids := make([]*pb.PointId, 0, len(tables))
	for _, t := range tables {
		if t == "" {
			continue
		}
		id := schemaPointID(projectID, wh, t)
		idToTable[id] = t
		ids = append(ids, pb.NewID(id))
	}
	if len(ids) == 0 {
		return out, nil
	}
	retrieved, err := p.client.Get(ctx, &pb.GetPoints{
		CollectionName: schemaCollectionName(projectID),
		Ids:            ids,
		WithPayload:    pb.NewWithPayload(true),
	})
	if err != nil {
		if isMissingCollection(err) {
			return out, nil
		}
		return nil, fmt.Errorf("qdrant: get schema points for %q: %w", projectID, err)
	}
	for _, rp := range retrieved {
		id := pointIDToString(rp.Id)
		t, ok := idToTable[id]
		if !ok {
			continue
		}
		out[t] = vectorstore.SchemaPoint{ID: id, Payload: payloadToMap(rp.Payload)}
	}
	return out, nil
}

// UpsertSchemaPoint creates or replaces a single table's schema point with a new
// vector + full payload — used when a blurb is rewritten (the text is
// re-embedded). The caller must supply the complete payload (an upsert replaces
// the whole point); the payload keys must match the agent's schema indexer. The
// collection must already exist (the editor is gated on a ready index).
func (p *Provider) UpsertSchemaPoint(ctx context.Context, projectID, warehouseID, table string, vector []float64, payload map[string]interface{}) error {
	if projectID == "" || table == "" {
		return fmt.Errorf("qdrant: projectID and table are required")
	}
	if len(vector) == 0 {
		return fmt.Errorf("qdrant: upsert schema point %q has empty vector", table)
	}
	payloadPB, err := pb.TryValueMap(payload)
	if err != nil {
		return fmt.Errorf("qdrant: encode schema payload for %q: %w", table, err)
	}
	wait := true
	_, err = p.client.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: schemaCollectionName(projectID),
		Wait:           &wait,
		Points: []*pb.PointStruct{{
			Id:      pb.NewID(schemaPointID(projectID, warehouseID, table)),
			Vectors: pb.NewVectorsDense(float64sToFloat32s(vector)),
			Payload: payloadPB,
		}},
	})
	if err != nil {
		return fmt.Errorf("qdrant: upsert schema point %q: %w", table, err)
	}
	return nil
}

// SetSchemaPayload merges the given payload fields into an existing table's
// schema point without touching its vector — used for edits that don't change
// the blurb text (keyword edits, column-count after a column removal). A missing
// point or collection is a no-op (nothing to keep in sync).
func (p *Provider) SetSchemaPayload(ctx context.Context, projectID, warehouseID, table string, fields map[string]interface{}) error {
	if projectID == "" || table == "" {
		return fmt.Errorf("qdrant: projectID and table are required")
	}
	if len(fields) == 0 {
		return nil
	}
	payloadPB, err := pb.TryValueMap(fields)
	if err != nil {
		return fmt.Errorf("qdrant: encode schema payload for %q: %w", table, err)
	}
	wait := true
	_, err = p.client.SetPayload(ctx, &pb.SetPayloadPoints{
		CollectionName: schemaCollectionName(projectID),
		Wait:           &wait,
		Payload:        payloadPB,
		PointsSelector: pb.NewPointsSelector(pb.NewID(schemaPointID(projectID, warehouseID, table))),
	})
	if err != nil {
		if isMissingCollection(err) {
			return nil
		}
		return fmt.Errorf("qdrant: set schema payload for %q: %w", table, err)
	}
	return nil
}

// DeleteSchemaPoint removes a single table's schema point so it stops surfacing
// in semantic retrieval. A missing point or collection is a no-op.
func (p *Provider) DeleteSchemaPoint(ctx context.Context, projectID, warehouseID, table string) error {
	if projectID == "" || table == "" {
		return fmt.Errorf("qdrant: projectID and table are required")
	}
	wait := true
	_, err := p.client.Delete(ctx, &pb.DeletePoints{
		CollectionName: schemaCollectionName(projectID),
		Wait:           &wait,
		Points:         pb.NewPointsSelectorIDs([]*pb.PointId{pb.NewID(schemaPointID(projectID, warehouseID, table))}),
	})
	if err != nil {
		if isMissingCollection(err) {
			return nil
		}
		return fmt.Errorf("qdrant: delete schema point %q: %w", table, err)
	}
	return nil
}
