package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

func reqWithPrincipal(p *auth.UserPrincipal) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	if p != nil {
		req = req.WithContext(auth.WithUser(context.Background(), p))
	}
	return req
}

func TestCallerSub(t *testing.T) {
	if got := callerSub(reqWithPrincipal(nil)); got != "anonymous" {
		t.Errorf("no principal: callerSub = %q, want anonymous", got)
	}
	if got := callerSub(reqWithPrincipal(&auth.UserPrincipal{Sub: ""})); got != "anonymous" {
		t.Errorf("empty sub: callerSub = %q, want anonymous", got)
	}
	if got := callerSub(reqWithPrincipal(&auth.UserPrincipal{Sub: "auth0|u1"})); got != "auth0|u1" {
		t.Errorf("callerSub = %q, want auth0|u1", got)
	}
}

func TestCallerIsAdmin(t *testing.T) {
	if callerIsAdmin(reqWithPrincipal(nil)) {
		t.Error("no principal: callerIsAdmin = true, want false")
	}
	if !callerIsAdmin(reqWithPrincipal(&auth.UserPrincipal{Sub: "anonymous", Roles: []string{"admin"}})) {
		t.Error("NoAuth principal: callerIsAdmin = false, want true")
	}
	if callerIsAdmin(reqWithPrincipal(&auth.UserPrincipal{Sub: "u1", Roles: []string{"viewer", "member"}})) {
		t.Error("viewer/member: callerIsAdmin = true, want false")
	}
}

func TestCanAccessSession(t *testing.T) {
	sess := &commonmodels.AskSession{ID: "s1", UserID: "owner"}

	// Owner (non-admin) can access.
	if !canAccessSession(reqWithPrincipal(&auth.UserPrincipal{Sub: "owner", Roles: []string{"viewer"}}), sess) {
		t.Error("owner denied access, want allowed")
	}
	// Admin (non-owner) can access.
	if !canAccessSession(reqWithPrincipal(&auth.UserPrincipal{Sub: "someone", Roles: []string{"admin"}}), sess) {
		t.Error("admin denied access, want allowed")
	}
	// Stranger (non-owner, non-admin) is denied.
	if canAccessSession(reqWithPrincipal(&auth.UserPrincipal{Sub: "stranger", Roles: []string{"member"}}), sess) {
		t.Error("stranger granted access, want denied")
	}
	// NoAuth (anonymous admin) can access an anonymous-owned session.
	anon := &commonmodels.AskSession{ID: "s2", UserID: "anonymous"}
	if !canAccessSession(reqWithPrincipal(&auth.UserPrincipal{Sub: "anonymous", Roles: []string{"admin"}}), anon) {
		t.Error("NoAuth admin denied its own session, want allowed")
	}
	// nil session is never accessible.
	if canAccessSession(reqWithPrincipal(&auth.UserPrincipal{Sub: "x", Roles: []string{"admin"}}), nil) {
		t.Error("nil session granted access, want denied")
	}
}
