package auth

import (
	"context"
	"sync"
)

// PermissionResolver enriches an authenticated principal with the permissions
// its roles grant, and returns the EFFECTIVE roles the principal should carry
// downstream.
//
// The community platform ships a no-op resolver (see GetPermissionResolver):
// it returns the principal's permissions and roles unchanged, so the built-in
// viewer/member/admin behaviour is preserved with zero configuration. The
// enterprise RBAC plugin registers a Mongo-backed resolver that reads per-org
// role→permission grants and appends the built-in-equivalent tier for custom
// roles — so the linear RequireRole hierarchy still admits a custom role the
// customer has granted access to (the mapControlPlaneRole pattern, generalised).
//
// effectiveRoles MUST include every role the principal already had; a resolver
// only ever adds (a tier alias), never drops, so an open project keyed on a
// built-in role is never hidden by resolution.
type PermissionResolver interface {
	Resolve(ctx context.Context, p *UserPrincipal) (permissions []string, effectiveRoles []string, err error)
}

var (
	resolverMu         sync.RWMutex
	registeredResolver PermissionResolver
)

// RegisterPermissionResolver registers the resolver used to enrich principals
// with permissions. Typically called from an enterprise plugin's init(). The
// last registration wins; passing nil clears it (back to the no-op default).
func RegisterPermissionResolver(r PermissionResolver) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	registeredResolver = r
}

// GetPermissionResolver returns the registered resolver, or a no-op resolver
// (permissions/roles unchanged) when none is registered — the community
// default that keeps the 3-role behaviour intact.
func GetPermissionResolver() PermissionResolver {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	if registeredResolver != nil {
		return registeredResolver
	}
	return noopPermissionResolver{}
}

type noopPermissionResolver struct{}

func (noopPermissionResolver) Resolve(_ context.Context, p *UserPrincipal) ([]string, []string, error) {
	if p == nil {
		return nil, nil, nil
	}
	return p.Permissions, p.Roles, nil
}
