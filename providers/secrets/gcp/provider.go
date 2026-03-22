// Package gcp provides a secrets.Provider backed by Google Cloud Secret Manager.
// Secrets are namespaced to avoid conflicts with other secrets in the GCP project.
//
// Configuration:
//
//	SECRET_PROVIDER=gcp
//	SECRET_GCP_PROJECT_ID=my-gcp-project
//	SECRET_NAMESPACE=decisionbox           (default)
//
// Authentication: Application Default Credentials (GKE Workload Identity, gcloud auth).
// Secret naming: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)
package gcp

import (
	"context"
	"fmt"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func init() {
	secrets.Register("gcp", func(cfg secrets.Config) (secrets.Provider, error) {
		if cfg.GCPProjectID == "" {
			return nil, fmt.Errorf("gcp secret provider requires SECRET_GCP_PROJECT_ID")
		}
		return NewGCPProvider(context.Background(), cfg.GCPProjectID, cfg.Namespace)
	}, secrets.ProviderMeta{
		Name:        "Google Cloud Secret Manager",
		Description: "Production secrets via GCP Secret Manager with IAM auth",
	})
}

// GCPProvider implements secrets.Provider using Google Cloud Secret Manager.
type GCPProvider struct {
	client     smClient
	gcpProject string
	namespace  string
}

// NewGCPProvider creates a GCP Secret Manager provider.
func NewGCPProvider(ctx context.Context, gcpProject, namespace string) (*GCPProvider, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcp secrets: failed to create client: %w", err)
	}
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &GCPProvider{
		client:     client,
		gcpProject: gcpProject,
		namespace:  namespace,
	}, nil
}

// NewGCPProviderWithClient creates a provider with a custom client (for testing with emulators).
func NewGCPProviderWithClient(client smClient, gcpProject, namespace string) *GCPProvider {
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &GCPProvider{
		client:     client,
		gcpProject: gcpProject,
		namespace:  namespace,
	}
}

func (p *GCPProvider) secretName(projectID, key string) string {
	// GCP secret names: letters, numbers, hyphens, underscores
	return fmt.Sprintf("%s-%s-%s", p.namespace, projectID, key)
}

func (p *GCPProvider) secretPath(projectID, key string) string {
	return fmt.Sprintf("projects/%s/secrets/%s", p.gcpProject, p.secretName(projectID, key))
}

func (p *GCPProvider) versionPath(projectID, key string) string {
	return fmt.Sprintf("projects/%s/secrets/%s/versions/latest", p.gcpProject, p.secretName(projectID, key))
}

func (p *GCPProvider) Get(ctx context.Context, projectID, key string) (string, error) {
	resp, err := p.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: p.versionPath(projectID, key),
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return "", secrets.ErrNotFound
		}
		return "", fmt.Errorf("gcp secrets get: %w", err)
	}
	return string(resp.Payload.Data), nil
}

func (p *GCPProvider) Set(ctx context.Context, projectID, key, value string) error {
	name := p.secretName(projectID, key)
	parent := fmt.Sprintf("projects/%s", p.gcpProject)

	// Try to create the secret first
	_, err := p.client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: name,
		Secret: &secretmanagerpb.Secret{
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
			Labels: map[string]string{
				"managed-by": "decisionbox",
				"namespace":  p.namespace,
				"project-id": projectID,
			},
		},
	})
	if err != nil {
		// Ignore AlreadyExists — secret exists, we'll add a new version
		if st, ok := status.FromError(err); !ok || st.Code() != codes.AlreadyExists {
			return fmt.Errorf("gcp secrets create: %w", err)
		}
	}

	// Add a new version with the value
	_, err = p.client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent: p.secretPath(projectID, key),
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte(value),
		},
	})
	if err != nil {
		return fmt.Errorf("gcp secrets add version: %w", err)
	}

	return nil
}

func (p *GCPProvider) List(ctx context.Context, projectID string) ([]secrets.SecretEntry, error) {
	parent := fmt.Sprintf("projects/%s", p.gcpProject)
	prefix := fmt.Sprintf("%s-%s-", p.namespace, projectID)

	it := p.client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: parent,
		Filter: fmt.Sprintf("labels.managed-by=decisionbox AND labels.namespace=%s AND labels.project-id=%s", p.namespace, projectID),
	})

	entries := make([]secrets.SecretEntry, 0)
	for {
		secret, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcp secrets list: %w", err)
		}

		// Extract key from secret name (remove namespace-projectID- prefix)
		name := secret.Name
		// Name is "projects/{project}/secrets/{name}"
		parts := strings.Split(name, "/")
		secretName := parts[len(parts)-1]

		key := strings.TrimPrefix(secretName, prefix)
		if key == secretName {
			continue // doesn't match our prefix
		}

		// Get masked value
		masked := "***"
		warning := ""
		resp, err := p.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
			Name: name + "/versions/latest",
		})
		if err == nil {
			masked = secrets.MaskValue(string(resp.Payload.Data))
		} else {
			if st, ok := status.FromError(err); ok && st.Code() == codes.PermissionDenied {
				warning = "Access denied — service account needs secretmanager.versions.access permission"
			} else {
				warning = fmt.Sprintf("Failed to read value: %s", err.Error())
			}
		}

		entries = append(entries, secrets.SecretEntry{
			Key:       key,
			Masked:    masked,
			Warning:   warning,
			UpdatedAt: secret.CreateTime.AsTime(),
		})
	}

	return entries, nil
}
