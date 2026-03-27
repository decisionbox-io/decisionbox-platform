package azure

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

func TestAzureProvider_ImplementsInterface(t *testing.T) {
	var _ secrets.Provider = (*AzureProvider)(nil)
}

func TestAzureProvider_Registered(t *testing.T) {
	registered := secrets.RegisteredProviders()
	found := false
	for _, name := range registered {
		if name == "azure" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("azure not registered in secrets provider registry, got %v", registered)
	}
}

func TestSecretName(t *testing.T) {
	p := &AzureProvider{namespace: "decisionbox"}

	name := p.secretName("proj-123", "llm-api-key")
	if name != "decisionbox-proj-123-llm-api-key" {
		t.Errorf("name = %q", name)
	}
}

func TestSecretName_CustomNamespace(t *testing.T) {
	p := &AzureProvider{namespace: "myapp-prod"}

	name := p.secretName("proj-456", "warehouse-creds")
	if name != "myapp-prod-proj-456-warehouse-creds" {
		t.Errorf("name = %q", name)
	}
}

func TestExtractSecretName(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"https://myvault.vault.azure.net/secrets/my-secret", "my-secret"},
		{"https://myvault.vault.azure.net/secrets/my-secret/abc123", "my-secret"},
		{"https://myvault.vault.azure.net/secrets/decisionbox-proj-1-llm-api-key", "decisionbox-proj-1-llm-api-key"},
		{"", ""},
		{"no-secrets-marker", ""},
	}
	for _, tt := range tests {
		id := azsecrets.ID(tt.id)
		got := extractSecretName(&id)
		if got != tt.want {
			t.Errorf("extractSecretName(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestExtractSecretName_Nil(t *testing.T) {
	got := extractSecretName(nil)
	if got != "" {
		t.Errorf("extractSecretName(nil) = %q, want empty", got)
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "404 response error",
			err:  &azcore.ResponseError{StatusCode: http.StatusNotFound},
			want: true,
		},
		{
			name: "403 response error",
			err:  &azcore.ResponseError{StatusCode: http.StatusForbidden},
			want: false,
		},
		{
			name: "generic error",
			err:  fmt.Errorf("some error"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFound(tt.err); got != tt.want {
				t.Errorf("isNotFound() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureProvider_Get_Success(t *testing.T) {
	mock := &mockKVClient{
		getSecretFn: func(_ context.Context, name string, version string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			wantName := "decisionbox-proj-1-llm-api-key"
			if name != wantName {
				t.Errorf("name = %q, want %q", name, wantName)
			}
			if version != "" {
				t.Errorf("version = %q, want empty (latest)", version)
			}
			value := "sk-ant-secret-value-12345"
			return azsecrets.GetSecretResponse{
				Secret: azsecrets.Secret{
					Value: &value,
				},
			}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	val, err := p.Get(context.Background(), "proj-1", "llm-api-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "sk-ant-secret-value-12345" {
		t.Errorf("Get() = %q, want %q", val, "sk-ant-secret-value-12345")
	}
}

func TestAzureProvider_Get_NotFound(t *testing.T) {
	mock := &mockKVClient{
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			return azsecrets.GetSecretResponse{}, &azcore.ResponseError{StatusCode: http.StatusNotFound}
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "nonexistent")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestAzureProvider_Get_OtherError(t *testing.T) {
	mock := &mockKVClient{
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			return azsecrets.GetSecretResponse{}, &azcore.ResponseError{StatusCode: http.StatusForbidden}
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err == nil {
		t.Fatal("Get() should have returned an error")
	}
	if err == secrets.ErrNotFound {
		t.Error("Get() should not return ErrNotFound for forbidden")
	}
}

func TestAzureProvider_Get_NilValue(t *testing.T) {
	mock := &mockKVClient{
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			return azsecrets.GetSecretResponse{
				Secret: azsecrets.Secret{
					Value: nil,
				},
			}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	_, err := p.Get(context.Background(), "proj-1", "key")
	if err != secrets.ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound for nil Value", err)
	}
}

func TestAzureProvider_Set_Success(t *testing.T) {
	setCalled := false
	mock := &mockKVClient{
		setSecretFn: func(_ context.Context, name string, params azsecrets.SetSecretParameters, _ *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error) {
			setCalled = true
			wantName := "decisionbox-proj-1-llm-api-key"
			if name != wantName {
				t.Errorf("SetSecret name = %q, want %q", name, wantName)
			}
			if params.Value == nil || *params.Value != "new-secret-value" {
				t.Errorf("SetSecret value = %v, want %q", params.Value, "new-secret-value")
			}
			// Verify tags
			if len(params.Tags) != 3 {
				t.Errorf("SetSecret tags count = %d, want 3", len(params.Tags))
			}
			if v, ok := params.Tags["managed-by"]; !ok || *v != "decisionbox" {
				t.Errorf("missing or wrong managed-by tag")
			}
			if v, ok := params.Tags["namespace"]; !ok || *v != "decisionbox" {
				t.Errorf("missing or wrong namespace tag")
			}
			if v, ok := params.Tags["project-id"]; !ok || *v != "proj-1" {
				t.Errorf("missing or wrong project-id tag")
			}
			return azsecrets.SetSecretResponse{}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "llm-api-key", "new-secret-value")
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !setCalled {
		t.Error("SetSecret was not called")
	}
}

func TestAzureProvider_Set_Error(t *testing.T) {
	mock := &mockKVClient{
		setSecretFn: func(_ context.Context, _ string, _ azsecrets.SetSecretParameters, _ *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error) {
			return azsecrets.SetSecretResponse{}, fmt.Errorf("InternalServerError: service unavailable")
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should have returned an error")
	}
}

func TestAzureProvider_List_Success(t *testing.T) {
	now := time.Now()
	id1 := azsecrets.ID("https://myvault.vault.azure.net/secrets/decisionbox-proj-1-llm-api-key")
	id2 := azsecrets.ID("https://myvault.vault.azure.net/secrets/decisionbox-proj-1-warehouse-creds")

	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newSinglePagePager([]*azsecrets.SecretProperties{
				{
					ID: &id1,
					Attributes: &azsecrets.SecretAttributes{
						Created: &now,
						Updated: &now,
					},
				},
				{
					ID: &id2,
					Attributes: &azsecrets.SecretAttributes{
						Created: &now,
					},
				},
			})
		},
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			value := "sk-ant-api03-very-secret-key-12345"
			return azsecrets.GetSecretResponse{
				Secret: azsecrets.Secret{
					Value: &value,
				},
			}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

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

	// Entry without Updated should use Created
	if entries[1].UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero when Created is set")
	}
}

func TestAzureProvider_List_Empty(t *testing.T) {
	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newEmptyPager()
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List() returned %d entries, want 0", len(entries))
	}
}

func TestAzureProvider_List_FiltersNamespace(t *testing.T) {
	now := time.Now()
	id1 := azsecrets.ID("https://myvault.vault.azure.net/secrets/decisionbox-proj-1-llm-api-key")
	id2 := azsecrets.ID("https://myvault.vault.azure.net/secrets/other-ns-proj-1-some-key")

	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newSinglePagePager([]*azsecrets.SecretProperties{
				{
					ID:         &id1,
					Attributes: &azsecrets.SecretAttributes{Created: &now},
				},
				{
					ID:         &id2,
					Attributes: &azsecrets.SecretAttributes{Created: &now},
				},
			})
		},
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			value := "sk-ant-api03-very-secret-key-12345"
			return azsecrets.GetSecretResponse{
				Secret: azsecrets.Secret{Value: &value},
			}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

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

func TestAzureProvider_List_GetValueError(t *testing.T) {
	now := time.Now()
	id1 := azsecrets.ID("https://myvault.vault.azure.net/secrets/decisionbox-proj-1-key")

	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newSinglePagePager([]*azsecrets.SecretProperties{
				{
					ID:         &id1,
					Attributes: &azsecrets.SecretAttributes{Created: &now},
				},
			})
		},
		getSecretFn: func(_ context.Context, _ string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			return azsecrets.GetSecretResponse{}, &azcore.ResponseError{StatusCode: http.StatusForbidden}
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(entries))
	}
	if entries[0].Warning == "" {
		t.Error("entry should have warning when GetSecret fails")
	}
	if entries[0].Masked != "***" {
		t.Errorf("masked should be *** when value can't be read, got %q", entries[0].Masked)
	}
}

func TestAzureProvider_List_PagerError(t *testing.T) {
	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newErrorPager(fmt.Errorf("InternalServerError: service unavailable"))
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	_, err := p.List(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("List() should return error when pager fails")
	}
}

func TestAzureProvider_CustomNamespace_Get(t *testing.T) {
	mock := &mockKVClient{
		getSecretFn: func(_ context.Context, name string, _ string, _ *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
			wantName := "myapp-prod-proj-1-key"
			if name != wantName {
				t.Errorf("name = %q, want %q", name, wantName)
			}
			value := "value"
			return azsecrets.GetSecretResponse{
				Secret: azsecrets.Secret{Value: &value},
			}, nil
		},
	}

	p := NewAzureProviderWithClient(mock, "myapp-prod")

	val, err := p.Get(context.Background(), "proj-1", "key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if val != "value" {
		t.Errorf("Get() = %q, want %q", val, "value")
	}
}

func TestNewAzureProviderWithClient_DefaultNamespace(t *testing.T) {
	p := NewAzureProviderWithClient(&mockKVClient{}, "")
	if p.namespace != "decisionbox" {
		t.Errorf("namespace = %q, want %q", p.namespace, "decisionbox")
	}
}

func TestValidateSecretName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"decisionbox-proj-1-llm-api-key", false},
		{"myapp-prod-proj-456-warehouse-creds", false},
		{"a", false},
		{"ABC-123", false},
		{"has_underscore", true},
		{"has.dot", true},
		{"has space", true},
		{"has/slash", true},
		{"", true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 127), false},
	}
	for _, tt := range tests {
		err := validateSecretName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateSecretName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestAzureProvider_Get_InvalidName(t *testing.T) {
	p := NewAzureProviderWithClient(&mockKVClient{}, "decisionbox")

	_, err := p.Get(context.Background(), "proj_with_underscore", "key")
	if err == nil {
		t.Fatal("Get() should reject names with underscores")
	}
	if !strings.Contains(err.Error(), "invalid secret name") {
		t.Errorf("error = %q, should mention invalid secret name", err.Error())
	}
}

func TestAzureProvider_Set_InvalidName(t *testing.T) {
	p := NewAzureProviderWithClient(&mockKVClient{}, "decisionbox")

	err := p.Set(context.Background(), "proj.with.dots", "key", "value")
	if err == nil {
		t.Fatal("Set() should reject names with dots")
	}
	if !strings.Contains(err.Error(), "invalid secret name") {
		t.Errorf("error = %q, should mention invalid secret name", err.Error())
	}
}

func TestIsConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"409 conflict", &azcore.ResponseError{StatusCode: http.StatusConflict}, true},
		{"404 not found", &azcore.ResponseError{StatusCode: http.StatusNotFound}, false},
		{"generic error", fmt.Errorf("some error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConflict(tt.err); got != tt.want {
				t.Errorf("isConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureProvider_Set_SoftDeleteConflict(t *testing.T) {
	mock := &mockKVClient{
		setSecretFn: func(_ context.Context, _ string, _ azsecrets.SetSecretParameters, _ *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error) {
			return azsecrets.SetSecretResponse{}, &azcore.ResponseError{StatusCode: http.StatusConflict}
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	err := p.Set(context.Background(), "proj-1", "key", "value")
	if err == nil {
		t.Fatal("Set() should return error for 409 Conflict")
	}
	if !strings.Contains(err.Error(), "soft-deleted") {
		t.Errorf("error = %q, should mention soft-deleted state", err.Error())
	}
	if !strings.Contains(err.Error(), "purge") {
		t.Errorf("error = %q, should mention purge action", err.Error())
	}
}

func TestAzureProvider_List_NilProperties(t *testing.T) {
	mock := &mockKVClient{
		newListSecretPropertiesPagerFn: func(_ *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
			return newSinglePagePager([]*azsecrets.SecretProperties{
				nil,
				{ID: nil},
			})
		},
	}

	p := NewAzureProviderWithClient(mock, "decisionbox")

	entries, err := p.List(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List() returned %d entries, want 0 (nil props should be skipped)", len(entries))
	}
}
