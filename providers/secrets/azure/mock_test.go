package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// mockKVClient implements kvClient for unit testing.
type mockKVClient struct {
	getSecretFn                    func(ctx context.Context, name string, version string, options *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error)
	setSecretFn                    func(ctx context.Context, name string, parameters azsecrets.SetSecretParameters, options *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error)
	newListSecretPropertiesPagerFn func(options *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse]
}

func (m *mockKVClient) GetSecret(ctx context.Context, name string, version string, options *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error) {
	if m.getSecretFn != nil {
		return m.getSecretFn(ctx, name, version, options)
	}
	return azsecrets.GetSecretResponse{}, fmt.Errorf("GetSecret not implemented")
}

func (m *mockKVClient) SetSecret(ctx context.Context, name string, parameters azsecrets.SetSecretParameters, options *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error) {
	if m.setSecretFn != nil {
		return m.setSecretFn(ctx, name, parameters, options)
	}
	return azsecrets.SetSecretResponse{}, fmt.Errorf("SetSecret not implemented")
}

func (m *mockKVClient) NewListSecretPropertiesPager(options *azsecrets.ListSecretPropertiesOptions) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
	if m.newListSecretPropertiesPagerFn != nil {
		return m.newListSecretPropertiesPagerFn(options)
	}
	return newEmptyPager()
}

// Compile-time check that mockKVClient satisfies kvClient.
var _ kvClient = (*mockKVClient)(nil)

// newSinglePagePager creates a pager that returns a single page of results.
func newSinglePagePager(props []*azsecrets.SecretProperties) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
	called := false
	return runtime.NewPager(runtime.PagingHandler[azsecrets.ListSecretPropertiesResponse]{
		More: func(_ azsecrets.ListSecretPropertiesResponse) bool {
			return !called
		},
		Fetcher: func(_ context.Context, _ *azsecrets.ListSecretPropertiesResponse) (azsecrets.ListSecretPropertiesResponse, error) {
			called = true
			return azsecrets.ListSecretPropertiesResponse{
				SecretPropertiesListResult: azsecrets.SecretPropertiesListResult{
					Value: props,
				},
			}, nil
		},
	})
}

// newEmptyPager creates a pager that returns an empty page.
func newEmptyPager() *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
	return newSinglePagePager(nil)
}

// newErrorPager creates a pager that returns an error.
func newErrorPager(err error) *runtime.Pager[azsecrets.ListSecretPropertiesResponse] {
	called := false
	return runtime.NewPager(runtime.PagingHandler[azsecrets.ListSecretPropertiesResponse]{
		More: func(_ azsecrets.ListSecretPropertiesResponse) bool {
			return !called
		},
		Fetcher: func(_ context.Context, _ *azsecrets.ListSecretPropertiesResponse) (azsecrets.ListSecretPropertiesResponse, error) {
			called = true
			return azsecrets.ListSecretPropertiesResponse{}, err
		},
	})
}
