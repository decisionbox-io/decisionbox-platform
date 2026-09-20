'use client';

import { ReactNode, useEffect, useState } from 'react';
import Image from 'next/image';
import Link from 'next/link';
import { usePathname, useParams } from 'next/navigation';
import { api, Project } from '@/lib/api';
import SpotlightSearch from '@/components/common/SpotlightSearch';
import { NavSections, COMMUNITY_NAV, passthroughGate } from '@/components/layout/nav';

interface ShellProps {
  children: ReactNode;
  breadcrumb?: { label: string; href?: string }[];
  actions?: ReactNode;
  fullWidth?: boolean;
}

export default function Shell({ children, breadcrumb, actions, fullWidth }: ShellProps) {
  const pathname = usePathname();
  const params = useParams<{ id?: string }>();
  const projectId = params?.id;

  const [project, setProject] = useState<Project | null>(null);

  useEffect(() => {
    if (!projectId) return;
    api.getProject(projectId).then(setProject).catch(() => {});
  }, [projectId]);

  // Derive null project from absent projectId (avoids setState in effect)
  const activeProject = projectId ? project : null;

  // Build initials from project name
  const initials = activeProject
    ? activeProject.name.split(' ').map(w => w[0]).join('').toUpperCase().slice(0, 2)
    : 'DB';

  return (
    <div style={{ display: 'flex', minHeight: '100vh' }}>
      {/* Sidebar */}
      <aside style={{
        position: 'fixed',
        left: 0,
        top: 0,
        bottom: 0,
        width: 'var(--db-sidebar-width)',
        background: 'var(--db-bg-white)',
        borderRight: '1px solid var(--db-border-default)',
        display: 'flex',
        flexDirection: 'column',
        zIndex: 10,
      }}>
        {/* Logo */}
        <div style={{
          padding: '16px 20px',
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          borderBottom: '1px solid var(--db-border-default)',
        }}>
          <Image src="/logo-icon.png" alt="DecisionBox" width={22} height={22} style={{ flexShrink: 0 }} />
          <span style={{ fontSize: 14, fontWeight: 600, letterSpacing: '-0.3px' }}>
            DecisionBox
          </span>
        </div>

        {/* Project selector */}
        {activeProject && (
          <div style={{ padding: '12px 12px 8px' }}>
            <Link href="/" style={{ textDecoration: 'none', color: 'inherit' }}>
              <div style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                background: 'var(--db-bg-muted)',
                borderRadius: 'var(--db-radius)',
                padding: '8px 10px',
                cursor: 'pointer',
                transition: 'background 120ms ease',
              }}
              onMouseEnter={e => (e.currentTarget.style.background = 'var(--db-bg-hover)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'var(--db-bg-muted)')}
              >
                <div style={{
                  width: 28,
                  height: 28,
                  borderRadius: 6,
                  background: 'linear-gradient(135deg, #1a1a1a, #444)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: '#fff',
                  fontSize: 12,
                  fontWeight: 600,
                  flexShrink: 0,
                }}>
                  {initials}
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {activeProject.name}
                  </div>
                  <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                    {activeProject.domain}{activeProject.category ? ` · ${activeProject.category}` : ''}
                  </div>
                </div>
                <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>▾</span>
              </div>
            </Link>
          </div>
        )}

        {/* Navigation — data-driven from COMMUNITY_NAV (see nav.tsx). */}
        {projectId ? (
          <nav style={{ padding: '8px 12px', flex: 1, overflowY: 'auto' }}>
            <NavSections
              sections={COMMUNITY_NAV.project}
              projectId={projectId}
              pathname={pathname ?? ''}
              gate={passthroughGate}
            />
          </nav>
        ) : (
          <nav style={{ padding: '8px 12px', flex: 1 }}>
            <NavSections
              sections={COMMUNITY_NAV.root}
              pathname={pathname ?? ''}
              gate={passthroughGate}
            />
          </nav>
        )}
      </aside>

      {/* Main area */}
      <div style={{ marginLeft: 'var(--db-sidebar-width)', flex: 1, display: 'flex', flexDirection: 'column' }}>
        {/* Top bar */}
        <header style={{
          height: 'var(--db-topbar-height)',
          background: 'var(--db-bg-white)',
          borderBottom: '1px solid var(--db-border-default)',
          position: 'sticky',
          top: 0,
          zIndex: 5,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '0 24px',
        }}>
          {/* Breadcrumb */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, flex: '1 1 0', minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap' }}>
            {breadcrumb ? breadcrumb.map((item, i) => (
              <span key={i} style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, flexShrink: i === breadcrumb.length - 1 ? 1 : 0 }}>
                {i > 0 && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)', flexShrink: 0 }}>/</span>}
                {item.href ? (
                  <Link href={item.href} style={{
                    color: 'var(--db-text-tertiary)',
                    textDecoration: 'none',
                    transition: 'color 120ms ease',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    maxWidth: 200,
                  }}
                  onMouseEnter={e => (e.currentTarget.style.color = 'var(--db-text-secondary)')}
                  onMouseLeave={e => (e.currentTarget.style.color = 'var(--db-text-tertiary)')}
                  title={item.label}
                  >
                    {item.label}
                  </Link>
                ) : (
                  <span style={{ fontWeight: 500, color: 'var(--db-text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{item.label}</span>
                )}
              </span>
            )) : (
              <span style={{ fontWeight: 500, color: 'var(--db-text-primary)' }}>Dashboard</span>
            )}
          </div>

          {/* Spotlight search — project-scoped only. It early-returns with no
              results when there is no projectId, so it's hidden on the root. */}
          {projectId && <SpotlightSearch />}

          {/* Actions */}
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flex: '1 1 0', justifyContent: 'flex-end' }}>
            {actions}
          </div>
        </header>

        {/* Content */}
        <main style={{
          maxWidth: fullWidth ? '100%' : 'var(--db-content-max-width)',
          padding: 'var(--db-content-padding)',
          width: '100%',
        }}>
          {children}
        </main>
      </div>
    </div>
  );
}
