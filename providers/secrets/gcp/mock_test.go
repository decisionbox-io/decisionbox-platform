package gcp

import (
	"context"
	"fmt"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockSMClient implements smClient for unit testing.
type mockSMClient struct {
	accessSecretVersionFn func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	createSecretFn        func(ctx context.Context, req *secretmanagerpb.CreateSecretRequest, opts ...gax.CallOption) (*secretmanagerpb.Secret, error)
	addSecretVersionFn    func(ctx context.Context, req *secretmanagerpb.AddSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.SecretVersion, error)
	listSecretsFn         func(ctx context.Context, req *secretmanagerpb.ListSecretsRequest, opts ...gax.CallOption) *secretmanager.SecretIterator
}

func (m *mockSMClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if m.accessSecretVersionFn != nil {
		return m.accessSecretVersionFn(ctx, req, opts...)
	}
	return nil, fmt.Errorf("AccessSecretVersion not implemented")
}

func (m *mockSMClient) CreateSecret(ctx context.Context, req *secretmanagerpb.CreateSecretRequest, opts ...gax.CallOption) (*secretmanagerpb.Secret, error) {
	if m.createSecretFn != nil {
		return m.createSecretFn(ctx, req, opts...)
	}
	return nil, fmt.Errorf("CreateSecret not implemented")
}

func (m *mockSMClient) AddSecretVersion(ctx context.Context, req *secretmanagerpb.AddSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
	if m.addSecretVersionFn != nil {
		return m.addSecretVersionFn(ctx, req, opts...)
	}
	return nil, fmt.Errorf("AddSecretVersion not implemented")
}

func (m *mockSMClient) ListSecrets(ctx context.Context, req *secretmanagerpb.ListSecretsRequest, opts ...gax.CallOption) *secretmanager.SecretIterator {
	if m.listSecretsFn != nil {
		return m.listSecretsFn(ctx, req, opts...)
	}
	return nil
}

// Compile-time check that mockSMClient satisfies smClient.
var _ smClient = (*mockSMClient)(nil)

func TestGCPProvider_Get_Success(t *testing.T) {
	mock := &mockSMClient{
		accessSecretVersionFn: func(_ context.Context, req *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
			wantPath := "projects/my-gcp-project/secrets/decisionbox-proj-1-llm-api-key/versions/latest"
			if req.Name != wantPath {
				t.Errorf("AccessSecretVersion Name = %q, want %q", req.Name, wantPath)
			}
			return &secretmanagerpb.AccessSecretVersionResponse{
				Name: "projects/my-gcp-project/secrets/decisionbox-proj-1-llm-api-key/versions/1",
				Payload: &secretmanagerpb.SecretPayload{
					Data: []byte("sk-ant-secret-value-12345"),
				},
			}, nil
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	val, err := p.Get(context.Background(), "proj-1", "llm-api-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "sk-ant-secret-value-12345" {
		t.Errorf("Get() = %q, want %q", val, "sk-ant-secret-value-12345")
	}
}

func TestGCPProvider_Get_NotFound(t *testing.T) {
	mock := &mockSMClient{
		accessSecretVersionFn: func(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
			return nil, status.Error(codes.NotFound, "Secret [projects/my-gcp-project/secrets/decisionbox-proj-1-nonexistent] not found")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "nonexistent")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestGCPProvider_Get_OtherError(t *testing.T) {
	mock := &mockSMClient{
		accessSecretVersionFn: func(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "caller does not have permission")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err == nil {
		t.Fatal("Get() should have returned an error")
	}
	if err == secrets.ErrNotFound {
		t.Error("Get() should not return ErrNotFound for permission denied")
	}
}

func TestGCPProvider_Get_NonGRPCError(t *testing.T) {
	mock := &mockSMClient{
		accessSecretVersionFn: func(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err == nil {
		t.Fatal("Get() should have returned an error")
	}
	if err == secrets.ErrNotFound {
		t.Error("Get() should not return ErrNotFound for connection error")
	}
}

func TestGCPProvider_Set_CreateNew(t *testing.T) {
	createCalled := false
	addVersionCalled := false

	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, req *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			createCalled = true
			wantParent := "projects/my-gcp-project"
			if req.Parent != wantParent {
				t.Errorf("CreateSecret Parent = %q, want %q", req.Parent, wantParent)
			}
			wantID := "decisionbox-proj-1-llm-api-key"
			if req.SecretId != wantID {
				t.Errorf("CreateSecret SecretId = %q, want %q", req.SecretId, wantID)
			}
			// Verify labels
			if req.Secret.Labels["managed-by"] != "decisionbox" {
				t.Errorf("label managed-by = %q, want decisionbox", req.Secret.Labels["managed-by"])
			}
			if req.Secret.Labels["namespace"] != "decisionbox" {
				t.Errorf("label namespace = %q, want decisionbox", req.Secret.Labels["namespace"])
			}
			if req.Secret.Labels["project-id"] != "proj-1" {
				t.Errorf("label project-id = %q, want proj-1", req.Secret.Labels["project-id"])
			}
			return &secretmanagerpb.Secret{
				Name: "projects/my-gcp-project/secrets/decisionbox-proj-1-llm-api-key",
			}, nil
		},
		addSecretVersionFn: func(_ context.Context, req *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
			addVersionCalled = true
			wantParent := "projects/my-gcp-project/secrets/decisionbox-proj-1-llm-api-key"
			if req.Parent != wantParent {
				t.Errorf("AddSecretVersion Parent = %q, want %q", req.Parent, wantParent)
			}
			if string(req.Payload.Data) != "new-secret-value" {
				t.Errorf("AddSecretVersion Payload.Data = %q, want %q", string(req.Payload.Data), "new-secret-value")
			}
			return &secretmanagerpb.SecretVersion{
				Name: "projects/my-gcp-project/secrets/decisionbox-proj-1-llm-api-key/versions/1",
			}, nil
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	err := p.Set(context.Background(), "proj-1", "llm-api-key", "new-secret-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !createCalled {
		t.Error("CreateSecret was not called")
	}
	if !addVersionCalled {
		t.Error("AddSecretVersion was not called")
	}
}

func TestGCPProvider_Set_UpdateExisting(t *testing.T) {
	createCalled := false
	addVersionCalled := false

	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			createCalled = true
			return nil, status.Error(codes.AlreadyExists, "Secret [projects/my-gcp-project/secrets/decisionbox-proj-1-key] already exists")
		},
		addSecretVersionFn: func(_ context.Context, req *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
			addVersionCalled = true
			if string(req.Payload.Data) != "updated-value" {
				t.Errorf("AddSecretVersion Payload.Data = %q, want %q", string(req.Payload.Data), "updated-value")
			}
			return &secretmanagerpb.SecretVersion{
				Name: "projects/my-gcp-project/secrets/decisionbox-proj-1-key/versions/2",
			}, nil
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "updated-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !createCalled {
		t.Error("CreateSecret was not called")
	}
	if !addVersionCalled {
		t.Error("AddSecretVersion was not called")
	}
}

func TestGCPProvider_Set_CreateError(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			return nil, status.Error(codes.PermissionDenied, "caller does not have permission")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error")
	}
}

func TestGCPProvider_Set_AddVersionError(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			return &secretmanagerpb.Secret{}, nil
		},
		addSecretVersionFn: func(_ context.Context, _ *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
			return nil, status.Error(codes.Internal, "internal error")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error when AddSecretVersion fails")
	}
}

func TestGCPProvider_Set_CreateNonGRPCError(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, _ *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			return nil, fmt.Errorf("network error: connection refused")
		},
	}

	p := NewGCPProviderWithClient(mock, "my-gcp-project", "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error for non-gRPC errors")
	}
}

func TestGCPProvider_CustomNamespace_Get(t *testing.T) {
	mock := &mockSMClient{
		accessSecretVersionFn: func(_ context.Context, req *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
			wantPath := "projects/prod-project/secrets/myapp-prod-proj-1-key/versions/latest"
			if req.Name != wantPath {
				t.Errorf("AccessSecretVersion Name = %q, want %q", req.Name, wantPath)
			}
			return &secretmanagerpb.AccessSecretVersionResponse{
				Payload: &secretmanagerpb.SecretPayload{
					Data: []byte("value"),
				},
			}, nil
		},
	}

	p := NewGCPProviderWithClient(mock, "prod-project", "myapp-prod")

	val, err := p.Get(context.Background(), "proj-1", "key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "value" {
		t.Errorf("Get() = %q, want %q", val, "value")
	}
}

func TestGCPProvider_CustomNamespace_Set(t *testing.T) {
	mock := &mockSMClient{
		createSecretFn: func(_ context.Context, req *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
			wantID := "myapp-prod-proj-1-key"
			if req.SecretId != wantID {
				t.Errorf("CreateSecret SecretId = %q, want %q", req.SecretId, wantID)
			}
			wantParent := "projects/prod-project"
			if req.Parent != wantParent {
				t.Errorf("CreateSecret Parent = %q, want %q", req.Parent, wantParent)
			}
			return &secretmanagerpb.Secret{}, nil
		},
		addSecretVersionFn: func(_ context.Context, req *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
			wantParent := "projects/prod-project/secrets/myapp-prod-proj-1-key"
			if req.Parent != wantParent {
				t.Errorf("AddSecretVersion Parent = %q, want %q", req.Parent, wantParent)
			}
			return &secretmanagerpb.SecretVersion{}, nil
		},
	}

	p := NewGCPProviderWithClient(mock, "prod-project", "myapp-prod")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
}
