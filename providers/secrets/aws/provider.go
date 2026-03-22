// Package aws provides a secrets.Provider backed by AWS Secrets Manager.
// Secrets are namespaced to avoid conflicts with other secrets in the AWS account.
//
// Configuration:
//
//	SECRET_PROVIDER=aws
//	SECRET_AWS_REGION=us-east-1
//	SECRET_NAMESPACE=decisionbox  (default)
//
// Authentication: AWS credentials (IAM role, env vars, or ~/.aws/credentials).
// Secret naming: {namespace}/{projectID}/{key} (e.g., decisionbox/proj123/llm-api-key)
package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

func init() {
	secrets.Register("aws", func(cfg secrets.Config) (secrets.Provider, error) {
		if cfg.AWSRegion == "" {
			cfg.AWSRegion = "us-east-1"
		}
		return NewAWSProvider(context.Background(), cfg.AWSRegion, cfg.Namespace)
	}, secrets.ProviderMeta{
		Name:        "AWS Secrets Manager",
		Description: "Production secrets via AWS Secrets Manager with IAM auth",
	})
}

// AWSProvider implements secrets.Provider using AWS Secrets Manager.
type AWSProvider struct {
	client    smClient
	namespace string
}

// NewAWSProvider creates an AWS Secrets Manager provider.
func NewAWSProvider(ctx context.Context, region, namespace string) (*AWSProvider, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws secrets: failed to load config: %w", err)
	}
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &AWSProvider{
		client:    secretsmanager.NewFromConfig(cfg),
		namespace: namespace,
	}, nil
}

// NewAWSProviderWithClient creates a provider with a custom client (for testing with LocalStack).
func NewAWSProviderWithClient(client smClient, namespace string) *AWSProvider {
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &AWSProvider{client: client, namespace: namespace}
}

func (p *AWSProvider) secretName(projectID, key string) string {
	return fmt.Sprintf("%s/%s/%s", p.namespace, projectID, key)
}

func (p *AWSProvider) Get(ctx context.Context, projectID, key string) (string, error) {
	name := p.secretName(projectID, key)

	output, err := p.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		// Check if not found
		var notFound *types.ResourceNotFoundException
		if ok := isNotFound(err, notFound); ok {
			return "", secrets.ErrNotFound
		}
		return "", fmt.Errorf("aws secrets get: %w", err)
	}

	if output.SecretString != nil {
		return *output.SecretString, nil
	}
	return "", secrets.ErrNotFound
}

func (p *AWSProvider) Set(ctx context.Context, projectID, key, value string) error {
	name := p.secretName(projectID, key)

	// Try to create first
	_, err := p.client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(value),
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("decisionbox")},
			{Key: aws.String("namespace"), Value: aws.String(p.namespace)},
			{Key: aws.String("project-id"), Value: aws.String(projectID)},
		},
	})
	if err != nil {
		// If already exists, update
		var alreadyExists *types.ResourceExistsException
		if ok := isAlreadyExists(err, alreadyExists); ok {
			_, err = p.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
				SecretId:     aws.String(name),
				SecretString: aws.String(value),
			})
			if err != nil {
				return fmt.Errorf("aws secrets update: %w", err)
			}
			return nil
		}
		return fmt.Errorf("aws secrets create: %w", err)
	}

	return nil
}

func (p *AWSProvider) List(ctx context.Context, projectID string) ([]secrets.SecretEntry, error) {
	prefix := fmt.Sprintf("%s/%s/", p.namespace, projectID)

	input := &secretsmanager.ListSecretsInput{
		Filters: []types.Filter{
			{
				Key:    types.FilterNameStringTypeName,
				Values: []string{prefix},
			},
		},
	}

	entries := make([]secrets.SecretEntry, 0)

	paginator := secretsmanager.NewListSecretsPaginator(p.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws secrets list: %w", err)
		}

		for _, secret := range page.SecretList {
			name := aws.ToString(secret.Name)
			key := strings.TrimPrefix(name, prefix)
			if key == name {
				continue // doesn't match prefix
			}

			// Get masked value
			masked := "***"
			warning := ""
			val, err := p.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
				SecretId: secret.ARN,
			})
			if err == nil && val.SecretString != nil {
				masked = secrets.MaskValue(*val.SecretString)
			} else if err != nil {
				warning = fmt.Sprintf("Failed to read value: %s", err.Error())
			}

			var updatedAt = secret.CreatedDate
			if secret.LastChangedDate != nil {
				updatedAt = secret.LastChangedDate
			}

			entry := secrets.SecretEntry{
				Key:     key,
				Masked:  masked,
				Warning: warning,
			}
			if updatedAt != nil {
				entry.UpdatedAt = *updatedAt
			}
			entries = append(entries, entry)
		}
	}

	return entries, nil
}

// Error type checks — AWS SDK v2 uses errors.As pattern
func isNotFound(err error, target *types.ResourceNotFoundException) bool {
	return strings.Contains(err.Error(), "ResourceNotFoundException") ||
		strings.Contains(err.Error(), "Secrets Manager can't find")
}

func isAlreadyExists(err error, target *types.ResourceExistsException) bool {
	return strings.Contains(err.Error(), "ResourceExistsException") ||
		strings.Contains(err.Error(), "already exists")
}
