package warehouse

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ConfigHash produces a stable SHA-256 over everything a datasource needs to
// list its contents and describe them — provider, project_id / catalog /
// location, dataset list, filter column+value, and the provider-specific
// config map (region, workgroup, auth_method, etc.). Credentials are NOT in
// the config map (they live in the secret provider), so they do not and should
// not affect the hash.
//
// Any edit that could change what discovery returns changes the hash, so a
// cache keyed by it self-invalidates.
//
// # Why this is here rather than beside its caller
//
// The hash is written into a collection that outlives the process that wrote
// it, and it is the ONLY thing distinguishing a row describing the datasource
// as it is now from a row describing the datasource as it used to be. Reading
// that collection without it means reading a previous configuration's answer
// and presenting it as current — confidently, and with nothing to notice.
//
// So every reader needs it, and a second implementation of it somewhere else
// would be a second chance to disagree about which rows are current. It lives
// in the package that owns datasource identity, takes the fields rather than
// any one caller's config struct, and is called through a thin method wherever
// a config struct is at hand.
//
// The byte layout is a persisted format, not an implementation detail. Every
// cache in every deployment is keyed by it, so a change here invalidates all
// of them at once and forces a full re-index — a cost paid deliberately, by
// bumping the version prefix, never by accident.
func ConfigHash(provider, projectID, location string, datasets []string, filterField, filterValue string, config map[string]string) string {
	// Canonicalise before hashing so map iteration order does not introduce
	// spurious cache misses.
	sorted := append([]string(nil), datasets...)
	sort.Strings(sorted)

	configKeys := make([]string, 0, len(config))
	for k := range config {
		configKeys = append(configKeys, k)
	}
	sort.Strings(configKeys)

	var b strings.Builder
	b.WriteString("v1|")
	b.WriteString(provider)
	b.WriteString("|pid=")
	b.WriteString(projectID)
	b.WriteString("|loc=")
	b.WriteString(location)
	b.WriteString("|ds=")
	b.WriteString(strings.Join(sorted, ","))
	b.WriteString("|filter=")
	b.WriteString(filterField)
	b.WriteByte('=')
	b.WriteString(filterValue)
	b.WriteString("|cfg=")
	for _, k := range configKeys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(config[k])
		b.WriteByte(';')
	}

	// Cross-project reads (a warehouse whose data lives in a different project
	// than the one running queries — BigQuery's data_project_id differing from
	// project_id) render table refs as three-part
	// `dataproject.dataset.table` in the catalog and as the schema-cache key,
	// instead of the two-part `dataset.table` form. That shape is a code-level
	// property the inputs above don't otherwise capture, so stamp it
	// explicitly: this invalidates exactly the cross-project caches that need
	// rediscovery after the shape changed, while single-project caches stay
	// valid (no needless re-index). Bump the marker version if the
	// cross-project ref shape ever changes again.
	if dp := config["data_project_id"]; dp != "" && dp != projectID {
		b.WriteString("|xproj-refshape=v2")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
