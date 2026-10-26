package llmlayer

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// Reserved provider-entry ids. Both map back to the secret keys a
// single-provider project already uses, so configuring per-layer models
// moves no secret.
const (
	// DefaultProviderID is the reserved id of a project's default provider
	// entry — the one every layer uses unless it names another.
	//
	// It is reserved, not a pointer: "make this the default" rewrites this
	// slot rather than re-pointing a primary_llm_provider_id field. An older
	// agent, sources-worker or fileingestion-worker resolves its credential
	// from the fixed key "llm-credentials"; if any entry could be the
	// default, promoting a secondary would leave those images reading the
	// wrong key — or none — and failing rather than degrading.
	DefaultProviderID = "default"

	// BlurbProviderID is the reserved id of the entry mirrored to the legacy
	// project.blurb_llm block, whose credential is blurb-llm-credentials.
	BlurbProviderID = "blurb"
)

// Secret keys and environment fallbacks. The two reserved ids keep exactly
// the chains they have today.
const (
	// LegacyCredentialsKey holds the default entry's credential. Unchanged
	// from the single-provider world, so no secret migration is needed.
	LegacyCredentialsKey = "llm-credentials" //nolint:gosec // G101: a secret-store key name, not a credential
	// LegacyCredentialsEnv is the default entry's process-environment
	// fallback. Managed-inference (gateway) deployments store no per-project
	// AI secret and depend on this.
	LegacyCredentialsEnv = "LLM_API_KEY" //nolint:gosec // G101: an env var name, not a credential

	// BlurbCredentialsKey holds the table-description entry's credential.
	BlurbCredentialsKey = "blurb-llm-credentials" //nolint:gosec // G101: a secret-store key name, not a credential
	// BlurbCredentialsEnv is the table-description entry's environment
	// fallback.
	BlurbCredentialsEnv = "BLURB_LLM_API_KEY" //nolint:gosec // G101: an env var name, not a credential
)

// EnvVarConfigKey lets an operator name an explicit environment-variable
// fallback on a non-reserved provider entry.
//
// It exists because non-reserved entries get NO shared env fallback (see
// CredentialChain), and a pod-level credential is still a legitimate way to
// configure one — it just has to be named rather than inherited.
const EnvVarConfigKey = "env_var"

// MaxProviderIDLen caps a provider-entry id at 32 characters.
//
// The limit comes from Azure Key Vault, which validates the COMPOSED secret
// name "<namespace>-<projectID>-<key>" at 127 characters
// (providers/secrets/azure). "decisionbox" + a 24-hex project id +
// "llm-credentials" already spends 53, and CredentialsKey hex-encodes the id
// at two characters each, so 32 characters of id is 64 of key.
const MaxProviderIDLen = 32

// CredentialsKey returns the secret-provider key holding a provider entry's
// credential.
//
// The reserved ids map to the legacy keys — "default" (and the empty-id case)
// to llm-credentials, "blurb" to blurb-llm-credentials — so an existing
// project's secrets stay exactly where they are. Any other entry maps to a
// namespaced "llm-credentials-<hex(id)>" key.
//
// The id is hex-encoded, not appended raw, because the cloud secret backends
// compose this key into the provider-side secret name and restrict that name
// to a limited charset: GCP allows [A-Za-z0-9_-], Azure Key Vault only
// [A-Za-z0-9-]. A raw id like "fast_small" (underscore) is rejected by those
// backends, so Set/Get for a secondary entry would fail. Hex ([0-9a-f]) plus
// the hyphen delimiter is accepted by every supported backend (GCP, Azure,
// AWS, Mongo), is deterministic, and is collision-free across distinct ids.
//
// Transcribed from warehouse.CredentialsKey, which solved the same problem
// for multi-warehouse; the two are deliberately the same shape.
func CredentialsKey(providerID string) string {
	switch NormalizeProviderID(providerID) {
	case DefaultProviderID:
		return LegacyCredentialsKey
	case BlurbProviderID:
		return BlurbCredentialsKey
	default:
		return LegacyCredentialsKey + "-" + hex.EncodeToString([]byte(providerID))
	}
}

// NormalizeProviderID maps the empty id to the reserved default, so a stored
// entry with no id and an explicit "default" are one entry everywhere.
func NormalizeProviderID(id string) string {
	if id == "" {
		return DefaultProviderID
	}
	return id
}

// ValidateProviderID checks a provider-entry id against what the secret
// backends can store and what the reserved ids mean.
//
// Returns an error a handler can hand to the user verbatim.
func ValidateProviderID(id string) error {
	if id == "" {
		// Normalised to the default by every reader; an explicit empty id in
		// a request body is still accepted and means the default entry.
		return nil
	}
	if len(id) > MaxProviderIDLen {
		return fmt.Errorf("provider id %q is too long (%d characters, maximum %d)", id, len(id), MaxProviderIDLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return fmt.Errorf("provider id %q may contain only letters, digits, underscores and hyphens", id)
		}
	}
	return nil
}

// CredentialRef is one (secret key, environment variable) pair to try when
// resolving a credential. An empty EnvVar means "no environment fallback for
// this step".
type CredentialRef struct {
	SecretKey string
	EnvVar    string
}

// CredentialChain returns the ordered credential lookups for one layer's
// resolved provider entry. The first step that yields a non-empty value wins.
//
// Three cases, and the third is a deliberate narrowing:
//
//	default (or "")          llm-credentials → LLM_API_KEY
//	blurb                    blurb-llm-credentials → BLURB_LLM_API_KEY →
//	                         llm-credentials → LLM_API_KEY
//	anything else            llm-credentials-<hex(id)> → cfg["env_var"], if named
//
// The table-description layer keeps the four-step blurb chain whenever it
// resolves to the default or the blurb entry, including when the project has
// no blurb override at all. That is today's behaviour and it is
// unconditional: a project with no override but a stored
// blurb-llm-credentials secret IS using that key right now, so dropping the
// chain would break it.
//
// Non-reserved entries get NO shared environment fallback. secrets.Resolve
// Credential falls back to the env var when the secret is missing, empty OR
// THE BACKEND READ FAILS, and an OpenAI-compatible entry sends whatever
// credential it is given as a bearer token to its own base_url — so a
// transient secret-store blip could ship the default entry's key to a
// third-party endpoint. An operator who wants a pod-level credential for a
// secondary entry names it explicitly with EnvVarConfigKey.
func CredentialChain(layer, providerID string, cfg map[string]string) []CredentialRef {
	id := NormalizeProviderID(providerID)

	blurbChain := []CredentialRef{
		{SecretKey: BlurbCredentialsKey, EnvVar: BlurbCredentialsEnv},
		{SecretKey: LegacyCredentialsKey, EnvVar: LegacyCredentialsEnv},
	}

	switch {
	case id == BlurbProviderID:
		return blurbChain
	case id == DefaultProviderID && layer == LayerTableDescriptions:
		// Today's resolveBlurbLLM tries the blurb secret first whether or not
		// a blurb override exists and whether or not the provider slugs match.
		return blurbChain
	case id == DefaultProviderID:
		return []CredentialRef{{SecretKey: LegacyCredentialsKey, EnvVar: LegacyCredentialsEnv}}
	default:
		return []CredentialRef{{
			SecretKey: CredentialsKey(id),
			EnvVar:    strings.TrimSpace(cfg[EnvVarConfigKey]),
		}}
	}
}

// IsReservedProviderID reports whether id names one of the two reserved
// entries, whose credentials live under the legacy keys.
func IsReservedProviderID(id string) bool {
	switch NormalizeProviderID(id) {
	case DefaultProviderID, BlurbProviderID:
		return true
	}
	return false
}
