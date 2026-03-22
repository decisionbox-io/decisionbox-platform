package aws

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// mockSMClient implements smClient for unit testing.
type mockSMClient struct {
	getSecretValueFn func(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	createSecretFn   func(ctx context.Context, params *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	putSecretValueFn func(ctx context.Context, params *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	listSecretsFn    func(ctx context.Context, params *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

func (m *mockSMClient) GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if m.getSecretValueFn != nil {
		return m.getSecretValueFn(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("GetSecretValue not implemented")
}

func (m *mockSMClient) CreateSecret(ctx context.Context, params *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	if m.createSecretFn != nil {
		return m.createSecretFn(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("CreateSecret not implemented")
}

func (m *mockSMClient) PutSecretValue(ctx context.Context, params *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	if m.putSecretValueFn != nil {
		return m.putSecretValueFn(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("PutSecretValue not implemented")
}

func (m *mockSMClient) ListSecrets(ctx context.Context, params *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	if m.listSecretsFn != nil {
		return m.listSecretsFn(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("ListSecrets not implemented")
}

// Compile-time check that mockSMClient satisfies smClient.
var _ smClient = (*mockSMClient)(nil)

func TestAWSProvider_Get_Success(t *testing.T) {
	mock := &mockSMClient{
		getSecretValueFn: func(_ context.Context, params *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			wantName := "decisionbox/proj-1/llm-api-key"
			if aws.ToString(params.SecretId) != wantName {
				t.Errorf("SecretId = %q, want %q", aws.ToString(params.SecretId), wantName)
			}
			return &secretsmanager.GetSecretValueOutput{
				SecretString: aws.String("sk-ant-secret-value-12345"),
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	val, err := p.Get(context.Background(), "proj-1", "llm-api-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "sk-ant-secret-value-12345" {
		t.Errorf("Get() = %q, want %q", val, "sk-ant-secret-value-12345")
	}
}

func TestAWSProvider_Get_NotFound(t *testing.T) {
	mock := &mockSMClient{
		getSecretValueFn: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: Secrets Manager can't find the specified secret")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "nonexistent")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestAWSProvider_Get_OtherError(t *testing.T) {
	mock := &mockSMClient{
		getSecretValueFn: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: not allowed")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err == nil {
		t.Fatal("Get() should have returned an error")
	}
	if err == secrets.ErrNotFound {
		t.Error("Get() should not return ErrNotFound for access denied")
	}
}

func TestAWSProvider_Get_NilSecretString(t *testing.T) {
	mock := &mockSMClient{
		getSecretValueFn: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{
				SecretString: nil, // binary secret or empty
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound for nil SecretString", err)
	}
}

func TestAWSProvider_Set_CreateNew(t *testing.T) {
	createCalled := false
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, params *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			createCalled = true
			wantName := "decisionbox/proj-1/llm-api-key"
			if aws.ToString(params.Name) != wantName {
				t.Errorf("CreateSecret Name = %q, want %q", aws.ToString(params.Name), wantName)
			}
			if aws.ToString(params.SecretString) != "new-secret-value" {
				t.Errorf("CreateSecret SecretString = %q, want %q", aws.ToString(params.SecretString), "new-secret-value")
			}
			// Verify tags
			if len(params.Tags) != 3 {
				t.Errorf("CreateSecret Tags count = %d, want 3", len(params.Tags))
			}
			return &secretsmanager.CreateSecretOutput{}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "llm-api-key", "new-secret-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !createCalled {
		t.Error("CreateSecret was not called")
	}
}

func TestAWSProvider_Set_UpdateExisting(t *testing.T) {
	createCalled := false
	putCalled := false

	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			createCalled = true
			return nil, fmt.Errorf("ResourceExistsException: the secret already exists")
		},
		putSecretValueFn: func(_ context.Context, params *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			putCalled = true
			wantName := "decisionbox/proj-1/llm-api-key"
			if aws.ToString(params.SecretId) != wantName {
				t.Errorf("PutSecretValue SecretId = %q, want %q", aws.ToString(params.SecretId), wantName)
			}
			if aws.ToString(params.SecretString) != "updated-value" {
				t.Errorf("PutSecretValue SecretString = %q, want %q", aws.ToString(params.SecretString), "updated-value")
			}
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "llm-api-key", "updated-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !createCalled {
		t.Error("CreateSecret was not called")
	}
	if !putCalled {
		t.Error("PutSecretValue was not called")
	}
}

func TestAWSProvider_Set_CreateError(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			return nil, fmt.Errorf("InternalServiceError: unexpected failure")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error")
	}
}

func TestAWSProvider_Set_UpdateError(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			return nil, fmt.Errorf("ResourceExistsException: the secret already exists")
		},
		putSecretValueFn: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			return nil, fmt.Errorf("InternalServiceError: update failed")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error on PutSecretValue failure")
	}
}

func TestAWSProvider_List_Success(t *testing.T) {
	now := time.Now()
	mock := &mockSMClient{
		listSecretsFn: func(_ context.Context, _ *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
			return &secretsmanager.ListSecretsOutput{
				SecretList: []types.SecretListEntry{
					{
						Name:            aws.String("decisionbox/proj-1/llm-api-key"),
						ARN:             aws.String("arn:aws:secretsmanager:us-east-1:123456:secret:decisionbox/proj-1/llm-api-key-AbCdEf"),
						CreatedDate:     &now,
						LastChangedDate: &now,
					},
					{
						Name:            aws.String("decisionbox/proj-1/warehouse-creds"),
						ARN:             aws.String("arn:aws:secretsmanager:us-east-1:123456:secret:decisionbox/proj-1/warehouse-creds-GhIjKl"),
						CreatedDate:     &now,
						LastChangedDate: nil,
					},
				},
				NextToken: nil, // single page
			}, nil
		},
		getSecretValueFn: func(_ context.Context, params *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			// Return masked-length values for each secret
			return &secretsmanager.GetSecretValueOutput{
				SecretString: aws.String("sk-ant-api03-very-secret-key-12345"),
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(entries))
	}

	if entries[0].Key != "llm-api-key" {
		t.Errorf("entries[0].Key = %q, want %q", entries[0].Key, "llm-api-key")
	}
	if entries[1].Key != "warehouse-creds" {
		t.Errorf("entries[1].Key = %q, want %q", entries[1].Key, "warehouse-creds")
	}

	// Masked value should not be full secret
	if entries[0].Masked == "sk-ant-api03-very-secret-key-12345" {
		t.Error("masked value should not be full secret")
	}
	if entries[0].Masked == "***" {
		t.Error("masked value should not be generic *** when value is long enough")
	}

	// Entry without LastChangedDate should use CreatedDate
	if entries[1].UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero when CreatedDate is set")
	}
}

func TestAWSProvider_List_Empty(t *testing.T) {
	mock := &mockSMClient{
		listSecretsFn: func(_ context.Context, _ *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
			return &secretsmanager.ListSecretsOutput{
				SecretList: []types.SecretListEntry{},
				NextToken:  nil,
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List() returned %d entries, want 0", len(entries))
	}
}

func TestAWSProvider_List_FiltersNamespace(t *testing.T) {
	now := time.Now()
	mock := &mockSMClient{
		listSecretsFn: func(_ context.Context, _ *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
			return &secretsmanager.ListSecretsOutput{
				SecretList: []types.SecretListEntry{
					{
						Name:        aws.String("decisionbox/proj-1/llm-api-key"),
						ARN:         aws.String("arn:aws:secretsmanager:us-east-1:123456:secret:key1"),
						CreatedDate: &now,
					},
					{
						// This entry has a different namespace prefix — should be filtered out
						Name:        aws.String("other-ns/proj-1/some-key"),
						ARN:         aws.String("arn:aws:secretsmanager:us-east-1:123456:secret:key2"),
						CreatedDate: &now,
					},
				},
				NextToken: nil,
			}, nil
		},
		getSecretValueFn: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{
				SecretString: aws.String("sk-ant-api03-very-secret-key-12345"),
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List() returned %d entries, want 1 (other namespace should be filtered)", len(entries))
	}
	if entries[0].Key != "llm-api-key" {
		t.Errorf("entries[0].Key = %q, want %q", entries[0].Key, "llm-api-key")
	}
}

func TestAWSProvider_List_GetValueError(t *testing.T) {
	now := time.Now()
	mock := &mockSMClient{
		listSecretsFn: func(_ context.Context, _ *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
			return &secretsmanager.ListSecretsOutput{
				SecretList: []types.SecretListEntry{
					{
						Name:        aws.String("decisionbox/proj-1/key"),
						ARN:         aws.String("arn:aws:secretsmanager:us-east-1:123456:secret:key1"),
						CreatedDate: &now,
					},
				},
				NextToken: nil,
			}, nil
		},
		getSecretValueFn: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: not allowed")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(entries))
	}
	if entries[0].Warning == "" {
		t.Error("entry should have warning when GetSecretValue fails")
	}
	if entries[0].Masked != "***" {
		t.Errorf("masked should be *** when value can't be read, got %q", entries[0].Masked)
	}
}

func TestAWSProvider_List_PaginatorError(t *testing.T) {
	mock := &mockSMClient{
		listSecretsFn: func(_ context.Context, _ *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
			return nil, fmt.Errorf("InternalServiceError: service unavailable")
		},
	}

	p := NewAWSProviderWithClient(mock, "decisionbox")

	_, err := p.List(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("List() should return error when paginator fails")
	}
}

func TestAWSProvider_CustomNamespace_Get(t *testing.T) {
	mock := &mockSMClient{
		getSecretValueFn: func(_ context.Context, params *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			wantName := "myapp-prod/proj-1/key"
			if aws.ToString(params.SecretId) != wantName {
				t.Errorf("SecretId = %q, want %q", aws.ToString(params.SecretId), wantName)
			}
			return &secretsmanager.GetSecretValueOutput{
				SecretString: aws.String("value"),
			}, nil
		},
	}

	p := NewAWSProviderWithClient(mock, "myapp-prod")

	val, err := p.Get(context.Background(), "proj-1", "key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "value" {
		t.Errorf("Get() = %q, want %q", val, "value")
	}
}
