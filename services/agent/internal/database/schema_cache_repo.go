package database

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SchemaCacheRepository persists discovered warehouse table schemas so a
// subsequent BuildIndex call can skip the expensive catalog pass when the
// warehouse config (datasets, filters, connection shape) hasn't changed.
//
// Keyed by (project_id, warehouse_hash). The hash covers every input that
// could change what discover_schemas returns — a mismatch invalidates the
// cache implicitly via query miss, so we never need an explicit
// invalidation path for config edits. A 7-day TTL caps staleness for
// warehouses whose physical schema drifts without the project config
// changing.
//
// One doc per (project, schema_key). We intentionally don't bundle into a
// single per-project doc: Mongo's 16 MB BSON cap would be at risk on
// ERP-scale warehouses (FINPORT-class, 1400+ tables), and a partial write
// on a crashed run still leaves the previous cache intact.
type SchemaCacheRepository struct {
	db *DB
}

func NewSchemaCacheRepository(db *DB) *SchemaCacheRepository {
	return &SchemaCacheRepository{db: db}
}

func (r *SchemaCacheRepository) col() *mongo.Collection {
	return r.db.Collection(CollectionSchemaCache)
}

// SchemaCacheEntry is the on-disk shape. SchemaKey matches the key shape
// that DiscoverSchemas returns (e.g. "dbo.orders").
type SchemaCacheEntry struct {
	ProjectID     string             `bson:"project_id"`
	WarehouseID   string             `bson:"warehouse_id"`
	WarehouseHash string             `bson:"warehouse_hash"`
	SchemaKey     string             `bson:"schema_key"`
	Schema        models.TableSchema `bson:"schema"`
	CachedAt      time.Time          `bson:"cached_at"`
}

// warehouseIDCond builds the BSON condition that matches a warehouse id.
// The default/primary warehouse ("" or "default") also matches legacy
// rows written before warehouse_id existed (missing field / "" / null),
// so existing single-warehouse caches stay valid without a backfill.
func warehouseIDCond(warehouseID string) bson.M {
	if warehouseID == "" || warehouseID == models.DefaultWarehouseID {
		return bson.M{"$in": bson.A{models.DefaultWarehouseID, "", nil}}
	}
	return bson.M{"$eq": warehouseID}
}

// Find returns the cached schema map for (projectID, warehouseID,
// warehouseHash) or (nil, nil) if the cache is cold or was invalidated by
// a hash change. An empty result is indistinguishable from "no cache" —
// callers treat both the same and fall through to fresh discovery.
func (r *SchemaCacheRepository) Find(ctx context.Context, projectID, warehouseID, warehouseHash string) (map[string]models.TableSchema, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	if warehouseHash == "" {
		return nil, errors.New("warehouseHash is required")
	}
	cur, err := r.col().Find(ctx, bson.M{
		"project_id":     projectID,
		"warehouse_id":   warehouseIDCond(warehouseID),
		"warehouse_hash": warehouseHash,
	})
	if err != nil {
		return nil, fmt.Errorf("schema cache find: %w", err)
	}
	defer cur.Close(ctx)

	out := make(map[string]models.TableSchema)
	for cur.Next(ctx) {
		var e SchemaCacheEntry
		if err := cur.Decode(&e); err != nil {
			return nil, fmt.Errorf("schema cache decode: %w", err)
		}
		out[e.SchemaKey] = e.Schema
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("schema cache cursor: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Save replaces the cached schemas for (projectID, warehouseID). Every
// prior row for THIS warehouse — including those tagged with a different
// hash — is dropped so stale schemas don't accumulate, but rows belonging
// to OTHER warehouses of the same project are left intact. (Scoping the
// wipe to the warehouse is load-bearing: the old whole-project
// DeleteMany({project_id}) would wipe a sibling warehouse's catalog the
// instant a second warehouse existed.) TTL handles absolute-age cleanup
// if the project is abandoned.
//
// SampleData is the only unbounded field on a TableSchema — every
// other field has natural per-table caps (column count, dataset name,
// …). On data-warehouse-class MSSQL / BigQuery tables with VARCHAR(MAX),
// XML, JSON, or wide nested struct columns, a single SELECT TOP 5 *
// sample can run into multiple MB per row and trip Mongo's hard 16 MB
// BSON document limit. capSampleData below caps row count and
// truncates per-value strings / bytes before insertion so a fat
// table's schema row stays well inside the limit while still
// preserving shape information for blurb generation and on-demand
// schema lookup.
func (r *SchemaCacheRepository) Save(ctx context.Context, projectID, warehouseID, warehouseHash string, schemas map[string]models.TableSchema) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if warehouseHash == "" {
		return errors.New("warehouseHash is required")
	}
	if len(schemas) == 0 {
		return nil
	}

	if warehouseID == "" {
		warehouseID = models.DefaultWarehouseID
	}
	if _, err := r.col().DeleteMany(ctx, bson.M{
		"project_id":   projectID,
		"warehouse_id": warehouseIDCond(warehouseID),
	}); err != nil {
		return fmt.Errorf("schema cache clear prior: %w", err)
	}

	now := time.Now().UTC()
	docs := make([]interface{}, 0, len(schemas))
	for key, sch := range schemas {
		sch.SampleData = capSampleData(sch.SampleData)
		docs = append(docs, SchemaCacheEntry{
			ProjectID:     projectID,
			WarehouseID:   warehouseID,
			WarehouseHash: warehouseHash,
			SchemaKey:     key,
			Schema:        sch,
			CachedAt:      now,
		})
	}
	// Ordered=false so a single bad row doesn't abort the rest — the
	// cache is best-effort; a partial write just means a partial cache
	// hit next run.
	if _, err := r.col().InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("schema cache save: %w", err)
	}
	return nil
}

// schemaCacheSampleRowLimit caps how many sample rows the cache stores
// per table. The discovery on-demand lookup and blurb generation only
// need a few examples to pick out the value shape; more rows mean
// more risk of tripping the 16 MB BSON doc cap on wide DW tables.
const schemaCacheSampleRowLimit = 3

// schemaCacheSampleValueMaxRunes caps the rune count of any string-typed
// sample value before it goes into the cache. Wide MSSQL tables backed
// by VARCHAR(MAX) / XML / JSON commonly carry multi-MB values that
// destroy the 16 MB doc budget on their own. 256 runes is long enough
// to convey shape (URL prefix, format, key fragment) and short enough
// that a 3-row × 200-column table caps at well under 1 MB per cached
// doc. Counted in runes (not bytes) so multi-byte UTF-8 sequences
// are never split mid-codepoint — BSON strings must be valid UTF-8,
// and a byte-length truncation can leave a partial rune at the tail.
const schemaCacheSampleValueMaxRunes = 256

// schemaCacheSampleBinaryMaxBytes caps the byte count of any non-UTF-8
// []byte sample value before it goes into the cache. Binary values
// (VARBINARY, BLOB, FILESTREAM) are base64-encoded after truncation —
// 256 bytes encodes to ~344 chars, still tiny.
const schemaCacheSampleBinaryMaxBytes = 256

// capSampleData returns a copy of rows with the row count capped at
// schemaCacheSampleRowLimit and each string / []byte value truncated
// per the rune / byte limits below. Non-string values (numbers,
// bools, time.Time) pass through untouched. Empty input returns nil
// so the BSON `omitempty` tag drops the field from the stored doc
// entirely.
func capSampleData(rows []map[string]interface{}) []map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > schemaCacheSampleRowLimit {
		rows = rows[:schemaCacheSampleRowLimit]
	}
	out := make([]map[string]interface{}, len(rows))
	for i, r := range rows {
		capped := make(map[string]interface{}, len(r))
		for k, v := range r {
			capped[k] = capSampleValue(v)
		}
		out[i] = capped
	}
	return out
}

// capSampleValue truncates a single sample-row value when it is a
// long string or byte slice. Strings (and UTF-8 []byte) are truncated
// at a rune boundary so the result is always valid UTF-8 — required
// because BSON rejects strings with invalid UTF-8. Non-UTF-8 byte
// slices are base64-encoded after truncation so they can be stored
// as a BSON string without losing fidelity.
//
// Truncation markers tell downstream consumers (blurb generator,
// discovery LLM) the value is incomplete so the truncated form is
// not treated as the real value to match against:
//
//   - text:     "…(truncated, original N chars)"
//   - binary:   "…(truncated binary, original N bytes, base64)"
func capSampleValue(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return capSampleString(x)
	case []byte:
		// MSSQL / Postgres drivers usually return CHAR / VARCHAR /
		// TEXT data as Go strings; []byte typically means VARBINARY,
		// BLOB, or text columns the driver couldn't decode. Treat as
		// text iff the bytes are valid UTF-8, otherwise base64.
		if utf8.Valid(x) {
			return capSampleString(string(x))
		}
		return capSampleBinary(x)
	default:
		return v
	}
}

// capSampleString returns s truncated at a rune boundary so it never
// exceeds schemaCacheSampleValueMaxRunes runes. The original rune
// count is reported in the marker so downstream consumers know how
// much was dropped.
func capSampleString(s string) string {
	runeCount := utf8.RuneCountInString(s)
	if runeCount <= schemaCacheSampleValueMaxRunes {
		return s
	}
	// Walk by rune so the byte slice ends on a codepoint boundary.
	byteEnd := len(s)
	runesSeen := 0
	for i := range s {
		if runesSeen >= schemaCacheSampleValueMaxRunes {
			byteEnd = i
			break
		}
		runesSeen++
	}
	return s[:byteEnd] + fmt.Sprintf("…(truncated, original %d chars)", runeCount)
}

// capSampleBinary returns a base64 representation of b's prefix, with
// a marker noting the original byte length. Used when raw bytes are
// not valid UTF-8 (typical for VARBINARY / BLOB / FILESTREAM columns)
// since BSON strings must be valid UTF-8 and []byte stored as Binary
// would not survive the LLM-facing string serialization downstream.
func capSampleBinary(b []byte) string {
	origLen := len(b)
	prefix := b
	if origLen > schemaCacheSampleBinaryMaxBytes {
		prefix = b[:schemaCacheSampleBinaryMaxBytes]
	}
	encoded := base64.StdEncoding.EncodeToString(prefix)
	if origLen <= schemaCacheSampleBinaryMaxBytes {
		return encoded
	}
	return encoded + fmt.Sprintf("…(truncated binary, original %d bytes, base64)", origLen)
}

// Invalidate drops every cache row for a project. Exposed for the API's
// "reindex from scratch" button and for tests.
func (r *SchemaCacheRepository) Invalidate(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if _, err := r.col().DeleteMany(ctx, bson.M{"project_id": projectID}); err != nil {
		return fmt.Errorf("schema cache invalidate: %w", err)
	}
	return nil
}

// CatalogCacheEntry is the on-disk shape for a catalog source's item refs.
//
// One doc per (project, warehouse, hash) rather than one per item: a catalog
// is a few hundred short strings, nowhere near the BSON cap that made the
// table cache one-doc-per-table, and reading it back as a single document
// keeps the "is this datasource indexed?" question a single lookup.
//
// It lives in the same collection as the table entries, distinguished by
// EntryKind, so it inherits the same TTL and the same invalidation-by-hash
// behaviour without a second collection to provision.
type CatalogCacheEntry struct {
	ProjectID     string    `bson:"project_id"`
	WarehouseID   string    `bson:"warehouse_id"`
	WarehouseHash string    `bson:"warehouse_hash"`
	EntryKind     string    `bson:"entry_kind"`
	Refs          []string  `bson:"refs"`
	CachedAt      time.Time `bson:"cached_at"`
}

// catalogEntryKind marks a doc as a catalog ref list. Table entries carry no
// entry_kind, so they are matched by its absence and are unaffected.
const catalogEntryKind = "catalog"

// SaveCatalog records the refs a catalog source currently offers.
//
// The refs are what makes a catalog datasource usable downstream: they are
// the authority for "this datasource, at this config, offers exactly these
// items". Without them a consumer cannot tell a live catalog point from one
// left in the shared vector collection by a datasource that has since been
// removed — the index is not self-cleaning, and only a cache entry keyed by
// the current config hash can say what is current.
func (r *SchemaCacheRepository) SaveCatalog(ctx context.Context, projectID, warehouseID, warehouseHash string, refs []string) error {
	if projectID == "" {
		return errors.New("projectID is required")
	}
	if warehouseHash == "" {
		return errors.New("warehouseHash is required")
	}
	if len(refs) == 0 {
		return nil
	}
	if warehouseID == "" {
		warehouseID = models.DefaultWarehouseID
	}

	filter := bson.M{
		"project_id":   projectID,
		"warehouse_id": warehouseIDCond(warehouseID),
		"entry_kind":   catalogEntryKind,
	}
	if _, err := r.col().DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("catalog cache clear prior: %w", err)
	}

	_, err := r.col().InsertOne(ctx, CatalogCacheEntry{
		ProjectID:     projectID,
		WarehouseID:   warehouseID,
		WarehouseHash: warehouseHash,
		EntryKind:     catalogEntryKind,
		Refs:          refs,
		CachedAt:      time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("catalog cache save: %w", err)
	}
	return nil
}

// FindCatalog returns the refs a catalog source offered at this config hash,
// or nil when there is no entry — a cold cache, a config change that moved the
// hash, or a source that is not catalog-shaped. All three are the same answer
// to the caller: nothing is known, so treat the datasource as unindexed.
func (r *SchemaCacheRepository) FindCatalog(ctx context.Context, projectID, warehouseID, warehouseHash string) ([]string, error) {
	if projectID == "" || warehouseHash == "" {
		return nil, nil
	}
	if warehouseID == "" {
		warehouseID = models.DefaultWarehouseID
	}

	var entry CatalogCacheEntry
	err := r.col().FindOne(ctx, bson.M{
		"project_id":     projectID,
		"warehouse_id":   warehouseIDCond(warehouseID),
		"warehouse_hash": warehouseHash,
		"entry_kind":     catalogEntryKind,
	}).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("catalog cache find: %w", err)
	}
	return entry.Refs, nil
}
