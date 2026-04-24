package schemaindex

import (
	"context"
	"fmt"

	pb "github.com/qdrant/go-client/qdrant"
)

// QdrantDropper is the minimal Qdrant surface the API needs: drop the
// per-project schema collection in response to POST /reindex. We keep
// it here (instead of pulling in the agent-side schema_retrieve package)
// so the API module doesn't grow a dep on `services/agent/internal/*`.
//
// Collection naming MUST stay in lockstep with
// services/agent/internal/ai/schema_retrieve.CollectionPrefix —
// duplicated here with a compile-time constant to make the linkage
// obvious.
const CollectionPrefix = "decisionbox_schema_"

// QdrantDropper opens one long-lived gRPC connection to Qdrant and
// drops per-project collections on demand. Call Close on shutdown.
type QdrantDropper struct {
	client *pb.Client
}

// NewQdrantDropper connects to Qdrant. Pass host, port (default 6334),
// and an optional API key.
func NewQdrantDropper(host string, port int, apiKey string, useTLS bool) (*QdrantDropper, error) {
	if host == "" {
		return nil, fmt.Errorf("schemaindex: QdrantDropper host is required")
	}
	if port == 0 {
		port = 6334
	}
	client, err := pb.NewClient(&pb.Config{
		Host:   host,
		Port:   port,
		APIKey: apiKey,
		UseTLS: useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("schemaindex: qdrant connect: %w", err)
	}
	return &QdrantDropper{client: client}, nil
}

// DropCollection deletes the per-project schema collection. Idempotent —
// missing collection is not an error.
func (d *QdrantDropper) DropCollection(ctx context.Context, projectID string) error {
	if projectID == "" {
		return fmt.Errorf("schemaindex: projectID is required")
	}
	name := CollectionPrefix + projectID
	exists, err := d.client.CollectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("schemaindex: check collection %q: %w", name, err)
	}
	if !exists {
		return nil
	}
	if err := d.client.DeleteCollection(ctx, name); err != nil {
		return fmt.Errorf("schemaindex: delete collection %q: %w", name, err)
	}
	return nil
}

// Close releases the gRPC connection.
func (d *QdrantDropper) Close() error {
	return d.client.Close()
}
