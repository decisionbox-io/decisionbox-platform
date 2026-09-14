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
  it('admin holds every permission implicitly', () => {
    expect(hasPermission([], ['admin'], 'roles.manage')).toBe(true);
    expect(hasPermission(undefined, ['owner'], 'project.delete')).toBe(true);
  });
  it('grants when the resolved set includes the permission', () => {
    expect(hasPermission(['project.view', 'ask.query'], ['hr-analyst'], 'ask.query')).toBe(true);
  });
  it('denies when the permission is absent and the role is not admin', () => {
    expect(hasPermission(['project.view'], ['hr-analyst'], 'roles.manage')).toBe(false);
  });
  it('denies with no permissions and a non-admin role', () => {
    expect(hasPermission(undefined, ['viewer'], 'project.view')).toBe(false);
  });
});
