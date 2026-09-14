// Shared RBAC helpers for the dashboard (advanced RBAC, #321).
//
// The community platform is a simple viewer/member/admin system; these helpers
// are the base primitives the enterprise overlay extends with its custom-role
// UI. `hasPermission` gates on the resolved permission set from GET /api/v1/me;
// the built-in role hierarchy is kept for the coarse role checks the base UI
// still uses.

export const ROLE_HIERARCHY: Record<string, number> = {
  viewer: 1,
  member: 2,
  admin: 3,
  // owner (cloud) outranks admin; treat it as admin-equivalent for the coarse
  // hierarchy check so an org owner is never under-privileged in the UI.
  owner: 3,
};

// hasMinRole reports whether any of the user's roles meets or exceeds minRole.
export function hasMinRole(userRoles: string[], minRole: string): boolean {
  const minLevel = ROLE_HIERARCHY[minRole] ?? 0;
  return userRoles.some((r) => (ROLE_HIERARCHY[r] ?? 0) >= minLevel);
}

// hasPermission reports whether the RESOLVED permission set includes perm. It
// does not short-circuit on the admin/owner role: the server resolves those to
// the full permission set (so /me carries every permission), and honoring the
// resolved set keeps the UI consistent with what the API enforces even when an
// org edits a built-in role's grants. `roles` is kept in the signature for
// call-site symmetry with hasMinRole.
export function hasPermission(
  permissions: string[] | undefined,
  _roles: string[] | undefined,
  perm: string,
): boolean {
  return !!permissions && permissions.includes(perm);
}
