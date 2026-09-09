package warehouse

import (
	"crypto/sha256"
	"encoding/hex"
)

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

// SharedCredentialKey returns the secret key holding a credential obtained FOR
// one consumer, named by an opaque id. Reports false when either half is empty.
//
// # Why the consumer is part of the key
//
// A datasource's config is writable by anyone who can edit the project, and a
// reference in it is just a string. Without the consumer, a member could point
// ANY datasource at a credential obtained for another — and some providers make
// that an exfiltration rather than a failure. The Postgres provider reads
// credentials_json as the password and host from config, so a datasource naming
// an analytics connection's slot and an attacker's host would send that
// connection's Google refresh token straight to it.
//
// Composing the consumer in means a datasource can only ever address a
// credential obtained for its own provider. Several datasources of that provider
// still share one, which is the point; nothing else can reach it. Found in
// review — the earlier version namespaced the key away from other FEATURES'
// secrets and stopped there, which left this open.
//
// # Why it is hashed
//
// The parts are joined with a separator that cannot appear in a secret name, so
// a readable key would have to encode them, and the composed Azure Key Vault
// name — "<namespace>-<projectID>-<key>", capped at 127 characters — has no room
// for that. A hash is fixed-width and unambiguous: the domain separator means no
// pair of (consumer, id) values can be rearranged into another pair's key, which
// a hyphen-joined key could be when one provider slug is a prefix of another.
//
// The cost is that a secret's name no longer says which connection it belongs
// to. That is worth one exfiltration path.
func SharedCredentialKey(consumer, id string) (string, bool) {
	if consumer == "" || id == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(consumer + "\x00" + id))
	return sharedCredentialPrefix + "-" + hex.EncodeToString(sum[:16]), true
}
