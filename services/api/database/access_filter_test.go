package database

import (
	"context"
	"testing"

	goauth "github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"go.mongodb.org/mongo-driver/bson"
)

// andClauses returns the $and clause list of a filter, or nil if the filter is
// the empty (match-all) filter.
func andClauses(t *testing.T, f bson.M) []bson.M {
	t.Helper()
	if len(f) == 0 {
		return nil
	}
	raw, ok := f["$and"]
	if !ok {
		t.Fatalf("expected $and filter, got %+v", f)
	}
	clauses, ok := raw.([]bson.M)
	if !ok {
		t.Fatalf("expected []bson.M $and, got %T", raw)
	}
	return clauses
}

// hasKeyInOr reports whether any bson.M in the clause's $or references the
// given key.
func hasKeyInOr(clause bson.M, key string) bool {
	raw, ok := clause["$or"]
	if !ok {
		return false
	}
	ors, ok := raw.([]bson.M)
	if !ok {
		return false
	}
	for _, o := range ors {
		if _, ok := o[key]; ok {
			return true
		}
	}
	return false
}

func TestProjectAccessFilter(t *testing.T) {
	t.Run("no principal is match-all", func(t *testing.T) {
		if got := projectAccessFilter(context.Background()); len(got) != 0 {
			t.Errorf("expected empty filter, got %+v", got)
		}
	})

	t.Run("admin is match-all (no org, no role clause)", func(t *testing.T) {
		ctx := goauth.WithUser(context.Background(), &goauth.UserPrincipal{Roles: []string{"admin"}})
		if got := projectAccessFilter(ctx); len(got) != 0 {
			t.Errorf("expected empty filter for admin, got %+v", got)
		}
	})

	t.Run("viewer without org gets a role clause only", func(t *testing.T) {
		ctx := goauth.WithUser(context.Background(), &goauth.UserPrincipal{Roles: []string{"viewer"}})
		clauses := andClauses(t, projectAccessFilter(ctx))
		if len(clauses) != 1 {
			t.Fatalf("expected 1 clause (role), got %d: %+v", len(clauses), clauses)
		}
		if !hasKeyInOr(clauses[0], "allowed_roles") {
			t.Errorf("expected an allowed_roles clause, got %+v", clauses[0])
		}
	})

	t.Run("viewer with org gets org + role clauses", func(t *testing.T) {
		ctx := goauth.WithUser(context.Background(), &goauth.UserPrincipal{Roles: []string{"viewer"}, OrgID: "acme"})
		clauses := andClauses(t, projectAccessFilter(ctx))
		if len(clauses) != 2 {
			t.Fatalf("expected 2 clauses (org + role), got %d: %+v", len(clauses), clauses)
		}
		var sawOrg, sawRole bool
		for _, c := range clauses {
			if hasKeyInOr(c, "org_id") {
				sawOrg = true
			}
			if hasKeyInOr(c, "allowed_roles") {
				sawRole = true
			}
		}
		if !sawOrg || !sawRole {
			t.Errorf("expected both org and role clauses, org=%v role=%v", sawOrg, sawRole)
		}
	})

	t.Run("admin with org gets org clause only", func(t *testing.T) {
		ctx := goauth.WithUser(context.Background(), &goauth.UserPrincipal{Roles: []string{"admin"}, OrgID: "acme"})
		clauses := andClauses(t, projectAccessFilter(ctx))
		if len(clauses) != 1 {
			t.Fatalf("expected 1 clause (org only), got %d: %+v", len(clauses), clauses)
		}
		if !hasKeyInOr(clauses[0], "org_id") {
			t.Errorf("expected an org_id clause, got %+v", clauses[0])
		}
	})

	t.Run("custom role produces a role $in with that role", func(t *testing.T) {
		ctx := goauth.WithUser(context.Background(), &goauth.UserPrincipal{Roles: []string{"hr-analyst"}})
		clauses := andClauses(t, projectAccessFilter(ctx))
		if len(clauses) != 1 {
			t.Fatalf("expected 1 clause, got %d", len(clauses))
		}
		ors, _ := clauses[0]["$or"].([]bson.M)
		found := false
		for _, o := range ors {
			if v, ok := o["allowed_roles"]; ok {
				if m, ok := v.(bson.M); ok {
					if in, ok := m["$in"].([]string); ok {
						for _, s := range in {
							if s == "hr-analyst" {
								found = true
							}
						}
					}
				}
			}
		}
		if !found {
			t.Errorf("expected allowed_roles $in to contain hr-analyst, got %+v", clauses[0])
		}
	})
}
