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

// CredentialRefKey is the datasource-config key naming the shared credential a
// datasource reads.
//
// It exists because a credential can be shared. A grant obtained once and used
// by several datasources cannot live under a key derived from any one of them,
// and derivation is exactly what CredentialsKey does. When this is set the agent
// reads the shared slot it names; when it is not — which is every datasource
// that does not share one — nothing about the read changes.
//
// Its value is an opaque id, NOT a secret key. See SharedCredentialKey for why
// that distinction is the whole of the access control here.
const CredentialRefKey = "credential_ref" //nolint:gosec // G101: the name of a config field, not a credential

// sharedCredentialPrefix namespaces credentials that several datasources read.
// Nothing else in a project's secrets lives under it.
const sharedCredentialPrefix = "warehouse-shared-credential"

// maxSharedCredentialID bounds the id so the composed key stays inside the
// 127-character limit Azure Key Vault imposes on a secret name: the prefix and
// separator are 28 characters and hex doubles the id.
const maxSharedCredentialID = 48

// SharedCredentialKey returns the secret key holding a credential that several
// datasources read, named by an opaque id. Reports false for an id no key can be
// formed from.
//
// The id is composed INTO a key rather than being one, and that is the whole of
// the access control. A datasource's config is writable by anyone who can edit
// the project, so a ref that was a raw key would let a member point a warehouse
// provider at any other secret the project holds — its LLM or embedding
// credential among them — and have the agent hand it over as credentials_json
// to a host they chose. Composed into a namespace nothing else writes, the worst
// a forged id can name is a shared credential that does not exist.
//
// Hex-encoded for the same reason CredentialsKey encodes a warehouse id: the
// cloud backends compose this key straight into the provider-side secret name
// and restrict its charset, and hex plus a hyphen is accepted by all of them.
func SharedCredentialKey(id string) (string, bool) {
	if id == "" || len(id) > maxSharedCredentialID {
		return "", false
	}
	return sharedCredentialPrefix + "-" + hex.EncodeToString([]byte(id)), true
}
