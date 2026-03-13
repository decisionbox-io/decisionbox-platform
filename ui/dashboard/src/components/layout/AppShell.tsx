'use client';

import { ReactNode, useEffect, useState } from 'react';
import Link from 'next/link';
import { usePathname, useParams } from 'next/navigation';
import {
  IconBook2, IconDatabase, IconSearch, IconSettings, IconStack2, IconTool,
} from '@tabler/icons-react';
import { api, Project } from '@/lib/api';

interface ShellProps {
  children: ReactNode;
  breadcrumb?: { label: string; href?: string }[];
  actions?: ReactNode;
}

export default function Shell({ children, breadcrumb, actions }: ShellProps) {
  const pathname = usePathname();
  const params = useParams<{ id?: string }>();
  const projectId = params?.id;

  const [project, setProject] = useState<Project | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);

  useEffect(() => {
    api.listProjects().then(setProjects).catch(() => {});
  }, []);

  useEffect(() => {
    if (projectId) {
      api.getProject(projectId).then(setProject).catch(() => {});
    } else {
      setProject(null);
    }
  }, [projectId]);

  const isActive = (path: string) => pathname === path;
  const isActivePrefix = (prefix: string) => pathname.startsWith(prefix);

  // Build initials from project name
  const initials = project
    ? project.name.split(' ').map(w => w[0]).join('').toUpperCase().slice(0, 2)
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
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M12 2a8 8 0 0 0-8 8c0 3.4 2.1 6.3 5 7.5V20h6v-2.5c2.9-1.2 5-4.1 5-7.5a8 8 0 0 0-8-8z" />
            <line x1="12" y1="20" x2="12" y2="22" />
            <line x1="9" y1="22" x2="15" y2="22" />
          </svg>
          <span style={{ fontSize: 14, fontWeight: 600, letterSpacing: '-0.3px' }}>
            DecisionBox
          </span>
        </div>

        {/* Project selector */}
        {project && (
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
                    {project.name}
                  </div>
                  <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                    {project.domain}{project.category ? ` · ${project.category}` : ''}
                  </div>
                </div>
                <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>▾</span>
              </div>
            </Link>
          </div>
        )}

        {/* Navigation */}
        {projectId && (
          <nav style={{ padding: '8px 12px', flex: 1, overflowY: 'auto' }}>
            {/* Discover section */}
            <div style={{
              fontSize: 10,
              fontWeight: 600,
              textTransform: 'uppercase',
              letterSpacing: '0.8px',
              color: 'var(--db-text-tertiary)',
              padding: '12px 10px 6px',
            }}>
              Discover
            </div>

            <NavItem
              href={`/projects/${projectId}`}
              icon={<IconSearch size={16} />}
              label="Discovery runs"
              active={isActive(`/projects/${projectId}`)}
            />
            <NavItem
              href={`/projects/${projectId}`}
              icon={<IconBook2 size={16} />}
              label="Insights"
              active={false}
            />
            <NavItem
              href={`/projects/${projectId}`}
              icon={<IconStack2 size={16} />}
              label="Recommendations"
              active={false}
            />

            {/* Configure section */}
            <div style={{
              fontSize: 10,
              fontWeight: 600,
              textTransform: 'uppercase',
              letterSpacing: '0.8px',
              color: 'var(--db-text-tertiary)',
              padding: '12px 10px 6px',
            }}>
              Configure
            </div>

            <NavItem
              href={`/projects/${projectId}/settings`}
              icon={<IconSettings size={16} />}
              label="Settings"
              active={isActive(`/projects/${projectId}/settings`)}
            />
            <NavItem
              href={`/projects/${projectId}/prompts`}
              icon={<IconTool size={16} />}
              label="Prompts"
              active={isActive(`/projects/${projectId}/prompts`)}
            />
          </nav>
        )}

        {/* No project selected — show project list link */}
        {!projectId && (
          <nav style={{ padding: '8px 12px', flex: 1 }}>
            <NavItem
              href="/"
              icon={<IconSearch size={16} />}
              label="Projects"
              active={isActive('/')}
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
            {breadcrumb ? breadcrumb.map((item, i) => (
              <span key={i} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                {i > 0 && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>/</span>}
                {item.href ? (
                  <Link href={item.href} style={{
                    color: 'var(--db-text-tertiary)',
                    textDecoration: 'none',
                    transition: 'color 120ms ease',
                  }}
                  onMouseEnter={e => (e.currentTarget.style.color = 'var(--db-text-secondary)')}
                  onMouseLeave={e => (e.currentTarget.style.color = 'var(--db-text-tertiary)')}
                  >
                    {item.label}
                  </Link>
                ) : (
                  <span style={{ fontWeight: 500, color: 'var(--db-text-primary)' }}>{item.label}</span>
                )}
              </span>
            )) : (
              <span style={{ fontWeight: 500, color: 'var(--db-text-primary)' }}>Dashboard</span>
            )}
          </div>

          {/* Actions */}
          {actions && (
            <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              {actions}
            </div>
          )}
        </header>

        {/* Content */}
        <main style={{
          maxWidth: 'var(--db-content-max-width)',
          padding: 'var(--db-content-padding)',
          width: '100%',
        }}>
          {children}
        </main>
      </div>
    </div>
  );
}

/* --- Nav Item Component --- */

function NavItem({ href, icon, label, active, count }: {
  href: string;
  icon: ReactNode;
  label: string;
  active: boolean;
  count?: number;
}) {
  return (
    <Link href={href} style={{ textDecoration: 'none', color: 'inherit' }}>
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        padding: '7px 10px',
        borderRadius: 6,
        marginBottom: 1,
        fontSize: 13,
        cursor: 'pointer',
        transition: 'background 120ms ease, color 120ms ease',
        background: active ? 'var(--db-bg-muted)' : 'transparent',
        color: active ? 'var(--db-text-primary)' : 'var(--db-text-secondary)',
        fontWeight: active ? 500 : 400,
      }}
      onMouseEnter={e => {
        if (!active) {
          e.currentTarget.style.background = 'var(--db-bg-muted)';
          e.currentTarget.style.color = 'var(--db-text-primary)';
        }
      }}
      onMouseLeave={e => {
        if (!active) {
          e.currentTarget.style.background = 'transparent';
          e.currentTarget.style.color = 'var(--db-text-secondary)';
        }
      }}
      >
        <span style={{ opacity: active ? 0.85 : 0.55, flexShrink: 0, display: 'flex' }}>{icon}</span>
        <span style={{ flex: 1 }}>{label}</span>
        {count !== undefined && count > 0 && (
          <span style={{
            marginLeft: 'auto',
            fontSize: 11,
            fontWeight: 500,
            background: 'var(--db-blue-bg)',
            color: 'var(--db-blue-text)',
            padding: '0 6px',
            borderRadius: 10,
            lineHeight: '18px',
          }}>
            {count}
          </span>
        )}
      </div>
    </Link>
  );
}
