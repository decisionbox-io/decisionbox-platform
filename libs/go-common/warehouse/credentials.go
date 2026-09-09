package warehouse

import "encoding/hex"

// LegacyCredentialsKey is the secret key under which every project's
// warehouse credentials were stored before multi-warehouse. It remains
// the key for the primary/"default" warehouse so existing single-warehouse
// projects keep working with no secret migration.
const LegacyCredentialsKey = "warehouse-credentials"

// DefaultWarehouseID is the reserved id of the primary warehouse for
// legacy / single-warehouse projects. Mirrors models.DefaultWarehouseID
// (kept here so this package has no dependency on the models package).
const DefaultWarehouseID = "default"

// CredentialsKey returns the secret-provider key that holds the
// credentials for a given warehouse id.
//
// The primary/"default" warehouse (and the empty-id case) maps to the
// legacy "warehouse-credentials" key — so existing projects need no
// secret migration. Any additional warehouse maps to a namespaced
// "warehouse-credentials-<hex(id)>" key.
//
// The id is hex-encoded, not appended raw, because cloud secret backends
// compose this key into the provider-side secret name and restrict that name
// to a limited charset: GCP allows [A-Za-z0-9_-], Azure Key Vault only
// [A-Za-z0-9-]. A raw id like "wh_b" (underscore) or an earlier ":" delimiter
// is rejected by those backends, so secondary-warehouse Set/Get would fail.
// Hex ([0-9a-f]) plus the hyphen delimiter is accepted by every supported
// backend (GCP, Azure, AWS, Mongo), is deterministic, and is collision-free
// across distinct ids. The Mongo secrets backend's unique index on
// (namespace, project_id, key) supports one value per key, so N warehouses =
// N distinct secret rows per project.
func CredentialsKey(warehouseID string) string {
	if warehouseID == "" || warehouseID == DefaultWarehouseID {
		return LegacyCredentialsKey
	}
	return LegacyCredentialsKey + "-" + hex.EncodeToString([]byte(warehouseID))
}

// CredentialRefKey is the datasource-config key naming the secret slot a
// credential lives in, when it is not the one derived from the datasource id.
//
// It exists because a credential can be shared. A grant obtained once and used
// by several datasources cannot live under a key derived from any one of them,
// and derivation is exactly what CredentialsKey does. When this is set the agent
// reads it instead; when it is not — which is every datasource that does not
// share one — nothing about the read changes.
const CredentialRefKey = "credential_ref" //nolint:gosec // G101: the name of a config field, not a credential

// ValidCredentialRef reports whether a ref could be a key this system wrote.
//
// It is a shape check rather than an authorization one, and deliberately so:
// the read it feeds is already scoped to the datasource's own project, so a ref
// can only ever name a secret that project already owns. What this stops is a
// value that is not a key at all — the cloud secret backends compose their
// secret's name straight from it, and one outside their alphabet fails in a way
// nobody can diagnose. Azure Key Vault is the strictest at [A-Za-z0-9-] and
// every key this system produces is lowercase, so that is the alphabet here.
func ValidCredentialRef(ref string) bool {
	if ref == "" || len(ref) > 127 {
		return false
	}
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
