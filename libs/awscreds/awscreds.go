// Package awscreds resolves an aws.Config from a generic AuthMethod-based
// project configuration, shared by every AWS-backed DecisionBox provider
// (warehouse Redshift, LLM Bedrock, embedding Bedrock, secrets AWS Secrets
// Manager).
//
// Resolution order, per project:
//   - access_keys / assume_role with non-empty project credential blob:
//     use the project value.
//   - access_keys / assume_role with empty project blob: fall through to
//     LoadDefaultConfig — the AWS SDK default chain picks up AWS_*
//     environment variables, shared profile, or pod IAM.
//   - iam_role (or empty method): LoadDefaultConfig only.
//
// The SDK's default chain already honours environment variables, so the
// env-fallback layer is implicit for iam_role and for the empty-blob path.
package awscreds

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Method identifiers stored in a provider's ProviderConfig under
// "auth_method". Each provider declares its supported methods in its
// own ProviderMeta.AuthMethods slice using these constants so the spelling
// stays in sync with Load().
const (
	MethodIAMRole    = "iam_role"
	MethodAccessKeys = "access_keys"
	MethodAssumeRole = "assume_role"
)

// Field keys read by Load from a provider's ProviderConfig map.
// Provider AuthMethod.Fields[].Key entries must match these.
const (
	FieldCredentials = "credentials_json"
	FieldRoleARN     = "role_arn"
	FieldExternalID  = "external_id"
)

// Config carries the inputs Load needs. Providers populate it from their
// generic config map and the resolved credential blob from secret/env.
type Config struct {
	Method      string
	Region      string
	Credentials string
	RoleARN     string
	ExternalID  string
	SessionName string
}

// Load resolves an aws.Config from c. See the package comment for
// resolution order.
func Load(ctx context.Context, c Config) (aws.Config, error) {
	method := c.Method
	if method == "" {
		method = MethodIAMRole
	}

	switch method {
	case MethodAccessKeys:
		if c.Credentials == "" {
			return loadDefault(ctx, c.Region)
		}
		parts := strings.SplitN(c.Credentials, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return aws.Config{}, fmt.Errorf("awscreds: invalid access key format, expected ACCESS_KEY_ID:SECRET_ACCESS_KEY")
		}
		return awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(c.Region),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(parts[0], parts[1], "")),
		)

	case MethodAssumeRole:
		if c.RoleARN == "" {
			return aws.Config{}, fmt.Errorf("awscreds: role_arn is required for assume_role auth")
		}
		baseCfg, err := loadDefault(ctx, c.Region)
		if err != nil {
			return aws.Config{}, fmt.Errorf("awscreds: failed to load base AWS config: %w", err)
		}
		stsClient := sts.NewFromConfig(baseCfg)
		sessionName := c.SessionName
		if sessionName == "" {
			sessionName = "decisionbox-agent"
		}
		extID := c.ExternalID
		provider := stscreds.NewAssumeRoleProvider(stsClient, c.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = sessionName
			if extID != "" {
				o.ExternalID = &extID
			}
		})
		baseCfg.Credentials = aws.NewCredentialsCache(provider)
		return baseCfg, nil

	case MethodIAMRole:
		return loadDefault(ctx, c.Region)

	default:
		return aws.Config{}, fmt.Errorf("awscreds: unsupported auth method %q", method)
	}
}

func loadDefault(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awscreds: failed to load default AWS config: %w", err)
	}
	return cfg, nil
}
