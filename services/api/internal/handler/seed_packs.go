package handler

import (
	"context"
	"embed"
	"encoding/json"
	"strings"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

//go:embed seed/*.json
var seedFS embed.FS

// portableFormat is the envelope for portable domain pack JSON files.
type portableFormat struct {
	Format        string           `json:"format"`
	FormatVersion int              `json:"format_version"`
	Pack          models.DomainPack `json:"pack"`
}

// SeedBuiltInPacks loads built-in domain packs from embedded JSON files
// into MongoDB if they don't already exist. Safe to call on every startup.
func SeedBuiltInPacks(ctx context.Context, repo database.DomainPackRepo) {
	seedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx = seedCtx

	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		apilog.WithError(err).Warn("Failed to read seed directory")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := seedFS.ReadFile("seed/" + entry.Name())
		if err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "file": entry.Name()}).Warn("Failed to read seed file")
			continue
		}

		var portable portableFormat
		if err := json.Unmarshal(data, &portable); err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "file": entry.Name()}).Warn("Failed to parse seed file")
			continue
		}

		if portable.Format != "decisionbox-domain-pack" {
			apilog.WithFields(apilog.Fields{"file": entry.Name()}).Warn("Unknown seed format, skipping")
			continue
		}

		pack := &portable.Pack

		// Check if pack already exists — don't overwrite user modifications
		existing, err := repo.GetBySlug(ctx, pack.Slug)
		if err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "slug": pack.Slug}).Warn("Failed to check existing pack")
			continue
		}
		if existing != nil {
			continue
		}

		if err := repo.Create(ctx, pack); err != nil {
			apilog.WithFields(apilog.Fields{"error": err, "slug": pack.Slug}).Warn("Failed to seed domain pack")
			continue
		}

		apilog.WithField("slug", pack.Slug).Info("Seeded built-in domain pack")
	}
}

