import { hasMinRole, hasPermission } from '@/lib/rbac';

describe('hasMinRole', () => {
  it('admin meets every tier', () => {
    expect(hasMinRole(['admin'], 'viewer')).toBe(true);
    expect(hasMinRole(['admin'], 'admin')).toBe(true);
  });
  it('viewer does not meet admin', () => {
    expect(hasMinRole(['viewer'], 'admin')).toBe(false);
  });
  it('owner is admin-equivalent', () => {
    expect(hasMinRole(['owner'], 'admin')).toBe(true);
  });
  it('highest of multiple roles wins', () => {
    expect(hasMinRole(['viewer', 'admin'], 'member')).toBe(true);
  });
  it('unknown role scores zero', () => {
    expect(hasMinRole(['hr-analyst'], 'viewer')).toBe(false);
  });
});

describe('hasPermission', () => {
  it('grants when the resolved set includes the permission', () => {
    expect(hasPermission(['project.view', 'ask.query'], ['hr-analyst'], 'ask.query')).toBe(true);
    // admin resolves to the full permission set server-side, so /me carries it.
    expect(hasPermission(['roles.manage', 'project.delete'], ['admin'], 'roles.manage')).toBe(true);
  });
  it('denies when the permission is absent (no admin-role short-circuit)', () => {
    expect(hasPermission(['project.view'], ['hr-analyst'], 'roles.manage')).toBe(false);
    // honors an edited built-in: an admin whose /me omits roles.manage is denied.
    expect(hasPermission(['project.view'], ['admin'], 'roles.manage')).toBe(false);
  });
  it('denies with no permissions resolved', () => {
    expect(hasPermission(undefined, ['viewer'], 'project.view')).toBe(false);
  });
});
