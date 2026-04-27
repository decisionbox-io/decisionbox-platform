package agentserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/decisionbox-io/decisionbox/libs/go-common/packgen"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// runPackGen executes a single domain-pack generation for the given
// project and exits. Invoked when the agent is launched with
// `--mode pack-gen`; the API's pack-generate handler owns the
// lifecycle status transitions around this call.
//
// Exit contract: 0 on success, non-zero on any error. The handler reads
// the exit code via the agent runner; stdout and stderr carry structured
// logs only.
//
// When no plugin has registered a packgen factory the no-op Provider
// returns ErrNotConfigured here and the agent exits with a clear error
// message — this should not happen in practice because the API also
// gates the feature, but the agent double-checks rather than producing
// a silent success.
func runPackGen(cfg *config.Config, projectID, runID string) error {
	ctx := context.Background()

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)

	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}

	applog.WithFields(applog.Fields{
		"project": project.Name,
		"state":   project.EffectiveState(),
		"run_id":  runID,
	}).Info("Starting pack-generation run")

	if project.GeneratePack == nil || !project.GeneratePack.Enabled {
		return fmt.Errorf("project has no pending pack-generation request")
	}

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	// Qdrant is required for pack-gen — both the schema slice and the
	// knowledge-source retrieval depend on it. Fail fast with a clear
	// message rather than partial-feature silence.
	if cfg.Qdrant.URL == "" {
		return fmt.Errorf("pack generation requires Qdrant — set QDRANT_URL")
	}
	qdrantProvider, closeQdrant, err := initQdrant(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect Qdrant: %w", err)
	}
	defer closeQdrant()

	// Configure sources first — pack-gen retrieves project knowledge
	// chunks via the same Provider exploration / /ask use.
	if err := gosources.Configure(ctx, gosources.Dependencies{
		Mongo:          mongoClient.Database(),
		Vectorstore:    qdrantProvider,
		SecretProvider: secretProvider,
	}); err != nil {
		return fmt.Errorf("configure sources provider: %w", err)
	}

	// Configure pack-gen with the shared deps. When no factory was
	// registered this is a no-op and GetProvider() returns the no-op
	// implementation.
	if err := packgen.Configure(ctx, packgen.Dependencies{
		Mongo:          mongoClient.Database(),
		Vectorstore:    qdrantProvider,
		SecretProvider: secretProvider,
		Sources:        gosources.GetProvider(),
	}); err != nil {
		return fmt.Errorf("configure pack-gen provider: %w", err)
	}

	res, err := packgen.GetProvider().Generate(ctx, packgen.GenerateRequest{
		ProjectID:   project.ID,
		RunID:       runID,
		PackName:    project.GeneratePack.PackName,
		PackSlug:    project.GeneratePack.PackSlug,
		Description: project.GeneratePack.Description,
	})
	if err != nil {
		if errors.Is(err, packgen.ErrNotConfigured) {
			return errors.New("pack generation is not available — no provider has been configured for this build")
		}
		return fmt.Errorf("generate pack: %w", err)
	}

	applog.WithFields(applog.Fields{
		"project_id": project.ID,
		"pack_slug":  res.PackSlug,
		"attempts":   res.Attempts,
	}).Info("Pack generation completed")

	return nil
}
