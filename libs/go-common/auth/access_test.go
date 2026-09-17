package auth

import (
	"context"
	"testing"
)

func TestCanAccessProject(t *testing.T) {
	tests := []struct {
		name         string
		principal    *UserPrincipal
		projectOrg   string
		allowedRoles []string
		want         bool
	}{
		{"nil principal denied", nil, "", nil, false},
		{"open project, any role", &UserPrincipal{Roles: []string{"viewer"}}, "", nil, true},
		{"open project, empty roles", &UserPrincipal{Roles: nil}, "", []string{}, true},
		{"restricted, matching role", &UserPrincipal{Roles: []string{"hr-analyst"}}, "", []string{"hr-analyst"}, true},
		{"restricted, one of many roles matches", &UserPrincipal{Roles: []string{"viewer", "hr-analyst"}}, "", []string{"hr-analyst"}, true},
		{"restricted, no matching role", &UserPrincipal{Roles: []string{"viewer"}}, "", []string{"hr-analyst"}, false},
		{"restricted, admin bypass", &UserPrincipal{Roles: []string{"admin"}}, "", []string{"hr-analyst"}, true},
		{"restricted, owner bypass", &UserPrincipal{Roles: []string{"owner"}}, "", []string{"hr-analyst"}, true},
		{"restricted, member is not a bypass", &UserPrincipal{Roles: []string{"member"}}, "", []string{"hr-analyst"}, false},
		{"blank project org matches any", &UserPrincipal{Roles: []string{"viewer"}, OrgID: "acme"}, "", nil, true},
		{"blank principal org matches any project", &UserPrincipal{Roles: []string{"viewer"}}, "acme", nil, true},
		{"same org allowed", &UserPrincipal{Roles: []string{"viewer"}, OrgID: "acme"}, "acme", nil, true},
		{"different org denied even for open project", &UserPrincipal{Roles: []string{"viewer"}, OrgID: "acme"}, "globex", nil, false},
		{"different org denied even for admin", &UserPrincipal{Roles: []string{"admin"}, OrgID: "acme"}, "globex", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanAccessProject(tt.principal, tt.projectOrg, tt.allowedRoles); got != tt.want {
				t.Errorf("CanAccessProject() = %v, want %v", got, tt.want)
			}
		})
	}
}

type captureAuditor struct{ decisions []AccessDecision }

func (c *captureAuditor) RecordAccessDecision(_ context.Context, d AccessDecision) {
	c.decisions = append(c.decisions, d)
}

func TestRecordAccessDecision_NoisePolicy(t *testing.T) {
	t.Cleanup(func() { RegisterAccessAuditor(nil) })
	cap := &captureAuditor{}
	RegisterAccessAuditor(cap)

	ctx := context.Background()
	// Grant on an OPEN project: suppressed (ordinary read).
	RecordAccessDecision(ctx, AccessDecision{ProjectID: "p1", Allowed: true, AllowedRoles: nil})
	// Grant on a RESTRICTED project: recorded.
	RecordAccessDecision(ctx, AccessDecision{ProjectID: "p2", Allowed: true, AllowedRoles: []string{"hr"}})
	// Denial on an open project: recorded (denials always).
	RecordAccessDecision(ctx, AccessDecision{ProjectID: "p3", Allowed: false, AllowedRoles: nil})
	// Denial on a restricted project: recorded.
	RecordAccessDecision(ctx, AccessDecision{ProjectID: "p4", Allowed: false, AllowedRoles: []string{"hr"}})

	if len(cap.decisions) != 3 {
		t.Fatalf("expected 3 recorded decisions, got %d: %+v", len(cap.decisions), cap.decisions)
	}
	got := map[string]bool{}
	for _, d := range cap.decisions {
		got[d.ProjectID] = true
	}
	if got["p1"] {
		t.Error("open-project grant should be suppressed")
	}
	for _, id := range []string{"p2", "p3", "p4"} {
		if !got[id] {
			t.Errorf("expected decision for %s to be recorded", id)
		}
	}
}

func TestRecordAccessDecision_NoAuditor_NoPanic(t *testing.T) {
	RegisterAccessAuditor(nil)
	// Must not panic when no auditor is registered.
	RecordAccessDecision(context.Background(), AccessDecision{ProjectID: "p", Allowed: false})
}

func TestGetPermissionResolver_NoopDefault(t *testing.T) {
	t.Cleanup(func() { RegisterPermissionResolver(nil) })
	RegisterPermissionResolver(nil)

	p := &UserPrincipal{Roles: []string{"member"}, Permissions: []string{"x"}}
	perms, roles, err := GetPermissionResolver().Resolve(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The no-op resolver returns nil effective roles ("no change"), so the
	// middleware leaves any provider-set EffectiveRoles intact.
	if roles != nil {
		t.Errorf("no-op resolver should return nil effective roles (no change), got %v", roles)
	}
	if len(perms) != 1 || perms[0] != "x" {
		t.Errorf("no-op resolver should return permissions unchanged, got %v", perms)
	}
}

type fakeRoleObserver struct {
	orgID string
	roles []string
	calls int
}

func (f *fakeRoleObserver) ObserveRoles(_ context.Context, orgID string, roles []string) {
	f.orgID = orgID
	f.roles = roles
	f.calls++
}

func TestObserveRoles(t *testing.T) {
	t.Cleanup(func() { RegisterRoleObserver(nil) })

	// No observer registered: no-op, no panic.
	ObserveRoles(context.Background(), "acme", []string{"hr"})

	obs := &fakeRoleObserver{}
	RegisterRoleObserver(obs)
	ObserveRoles(context.Background(), "acme", []string{"hr-analyst"})
	// Empty roles are ignored.
	ObserveRoles(context.Background(), "acme", nil)

	if obs.calls != 1 {
		t.Fatalf("expected 1 observe call (empty roles ignored), got %d", obs.calls)
	}
	if obs.orgID != "acme" || len(obs.roles) != 1 || obs.roles[0] != "hr-analyst" {
		t.Errorf("observer got orgID=%q roles=%v", obs.orgID, obs.roles)
	}
}
