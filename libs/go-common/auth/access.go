package auth

import (
	"context"
	"sync"
)

// CanAccessProject reports whether the principal may access a project carrying
// the given org and allowed-roles ACL. It is a pure function of the principal's
// roles + the project's ACL fields, so the community API and every enterprise
// plugin route-group that loads a project share one enforcement rule.
//
// Backward-compatible defaults (so nothing breaks the day this ships):
//   - empty allowedRoles ⇒ the project is OPEN to every role;
//   - blank projectOrgID (legacy / single-org self-hosted) matches any
//     principal, and a principal with no org matches any project — org scoping
//     is enforced only when BOTH sides carry a non-empty org and they differ;
//   - admin and owner always pass (they administer the deployment / org),
//     within the org scope above.
func CanAccessProject(p *UserPrincipal, projectOrgID string, allowedRoles []string) bool {
	if p == nil {
		return false
	}
	// Org isolation is inert unless both the project and the principal carry a
	// non-empty org id (forward-looking multi-org hygiene; on cloud one tenant
	// is one org, and legacy projects have no org id).
	if projectOrgID != "" && p.OrgID != "" && projectOrgID != p.OrgID {
		return false
	}
	for _, role := range p.Roles {
		if role == RoleAdmin || role == RoleOwner {
			return true
		}
	}
	if len(allowedRoles) == 0 {
		return true
	}
	allow := make(map[string]struct{}, len(allowedRoles))
	for _, r := range allowedRoles {
		allow[r] = struct{}{}
	}
	for _, role := range p.Roles {
		if _, ok := allow[role]; ok {
			return true
		}
	}
	return false
}

// Built-in role names understood by the community platform. Enterprise custom
// roles are arbitrary strings resolved to permissions by the registered
// PermissionResolver; these constants name only the deployment-wide tiers that
// always bypass a project ACL.
const (
	RoleViewer = "viewer"
	RoleMember = "member"
	RoleAdmin  = "admin"
	// RoleOwner is the cloud control-plane's top tier; it has no place in the
	// self-hosted hierarchy but is treated as an admin-equivalent bypass here
	// so an org owner is never locked out of a restricted project.
	RoleOwner = "owner"
)

// AccessDecision is one project-access evaluation, handed to a registered
// AccessAuditor for the "who accessed project X" trail. Roles is the principal's
// resolved role set; AllowedRoles is the project's ACL at decision time.
type AccessDecision struct {
	ProjectID    string
	UserEmail    string
	OrgID        string
	Roles        []string
	AllowedRoles []string
	Action       string // permission/action attempted, e.g. "project.view", "discovery.run"
	Allowed      bool
}

// AccessAuditor persists project-access decisions. The community platform ships
// none (RecordAccessDecision is a no-op); the enterprise audit plugin registers
// one that writes an audit event.
type AccessAuditor interface {
	RecordAccessDecision(ctx context.Context, d AccessDecision)
}

var (
	auditorMu         sync.RWMutex
	registeredAuditor AccessAuditor
)

// RegisterAccessAuditor registers the sink for project-access decisions.
// Typically called from the enterprise audit plugin's init(). Passing nil
// clears it.
func RegisterAccessAuditor(a AccessAuditor) {
	auditorMu.Lock()
	defer auditorMu.Unlock()
	registeredAuditor = a
}

// RecordAccessDecision forwards a decision to the registered auditor, applying
// the shared noise policy so every caller logs the same thing: every DENIAL is
// recorded, but a GRANT is recorded only for a RESTRICTED project (non-empty
// AllowedRoles). Grants on open projects are ordinary reads and are not logged
// (they already appear as generic request rows in the audit middleware). No-op
// when no auditor is registered.
func RecordAccessDecision(ctx context.Context, d AccessDecision) {
	auditorMu.RLock()
	a := registeredAuditor
	auditorMu.RUnlock()
	if a == nil {
		return
	}
	if d.Allowed && len(d.AllowedRoles) == 0 {
		return
	}
	a.RecordAccessDecision(ctx, d)
}

// RoleObserver is notified of the role strings seen on authenticated logins so
// an admin UI can surface the exact strings an IdP emits (the "seen roles"
// helper that mitigates the exact-string-match footgun). It records role
// STRINGS only, never user identity. The community platform ships none
// (ObserveRoles is a no-op); the enterprise RBAC plugin registers a
// Mongo-backed observer with an optional prefix filter.
type RoleObserver interface {
	ObserveRoles(ctx context.Context, orgID string, roles []string)
}

var (
	roleObserverMu         sync.RWMutex
	registeredRoleObserver RoleObserver
)

// RegisterRoleObserver registers the sink for observed role strings. Passing
// nil clears it.
func RegisterRoleObserver(o RoleObserver) {
	roleObserverMu.Lock()
	defer roleObserverMu.Unlock()
	registeredRoleObserver = o
}

// ObserveRoles forwards observed role strings to the registered observer.
// No-op when none is registered.
func ObserveRoles(ctx context.Context, orgID string, roles []string) {
	roleObserverMu.RLock()
	o := registeredRoleObserver
	roleObserverMu.RUnlock()
	if o == nil || len(roles) == 0 {
		return
	}
	o.ObserveRoles(ctx, orgID, roles)
}
