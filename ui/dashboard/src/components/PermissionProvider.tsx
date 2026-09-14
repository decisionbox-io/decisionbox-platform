'use client';

import { createContext, useContext, useEffect, useState, ReactNode } from 'react';
import { api, Me } from '@/lib/api';
import { hasPermission, hasMinRole } from '@/lib/rbac';

// PermissionContextValue exposes the authenticated principal's roles + resolved
// permissions and the gate helpers the UI uses to show/hide capabilities.
export interface PermissionContextValue {
  loading: boolean;
  // roles is the GENUINE role set; effectiveRoles is the RESOLVED tier set that
  // role-tier gates (hasRole / useRole) must read so a custom role resolved to a
  // built-in tier isn't hidden from UI the API would allow (advanced RBAC #321).
  roles: string[];
  effectiveRoles: string[];
  permissions: string[];
  can: (perm: string) => boolean;
  hasRole: (minRole: string) => boolean;
}

const PermissionContext = createContext<PermissionContextValue>({
  loading: true,
  roles: [],
  effectiveRoles: [],
  permissions: [],
  can: () => false,
  hasRole: () => false,
});

// PermissionProvider fetches GET /api/v1/me once and provides the principal's
// roles + resolved permissions to the tree. On a fetch failure it fails OPEN to
// an admin-equivalent principal, matching the community NoAuth default (where
// everyone is admin) so a deployment without auth is never locked out of its
// own dashboard by a transient /me error.
export function PermissionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ loading: boolean; me: Me | null }>({
    loading: true,
    me: null,
  });

  useEffect(() => {
    let cancelled = false;
    api
      .getMe()
      .then((me) => {
        if (!cancelled) setState({ loading: false, me });
      })
      .catch(() => {
        if (!cancelled) setState({ loading: false, me: null });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Fail open to admin when /me is unavailable (community/no-auth default).
  const roles = state.me ? state.me.roles : ['admin'];
  // effective_roles carries a custom role's resolved built-in tier; fall back to
  // the genuine roles when the server didn't send it (older API / community).
  const effectiveRoles = state.me
    ? (state.me.effective_roles && state.me.effective_roles.length ? state.me.effective_roles : state.me.roles)
    : ['admin'];
  const permissions = state.me?.permissions ?? [];

  const value: PermissionContextValue = {
    loading: state.loading,
    roles,
    effectiveRoles,
    permissions,
    can: (perm: string) => hasPermission(permissions, roles, perm),
    // Tier checks read the RESOLVED roles so a custom role granted a member/admin
    // tier passes the same gate the API enforces.
    hasRole: (minRole: string) => hasMinRole(effectiveRoles, minRole),
  };

  return <PermissionContext.Provider value={value}>{children}</PermissionContext.Provider>;
}

// usePermissions returns the current principal's roles/permissions + gate
// helpers. Safe to call anywhere under PermissionProvider.
export function usePermissions(): PermissionContextValue {
  return useContext(PermissionContext);
}
