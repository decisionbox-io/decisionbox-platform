'use client';

// Data-driven sidebar navigation.
//
// The nav used to be a wall of hard-coded <NavItem> JSX inside AppShell, and the
// enterprise / cloud dashboards each replace AppShell wholesale via the Docker
// file-overlay (last-writer-wins `COPY`). Expressing the nav as data — an array
// of sections → items — lets each layer describe its nav as a config and reuse
// ONE renderer (`NavSections`) and ONE item component (`NavItem`) instead of
// forking the markup. Overlays reorder/regroup by editing data, and can filter
// items with flags (e.g. `hideOnCloud`) instead of duplicating the whole file.
//
// This module owns the shared types + renderer + the COMMUNITY nav content. The
// enterprise overlay ships its own superset config (`nav-config.tsx`) and a gate
// that wires its role/capability hooks, but renders through the same primitives.

import type { ComponentType, ReactNode } from 'react';
import Link from 'next/link';
import {
  IconBook2, IconBookmark, IconHelpCircle, IconLock, IconMessageCircle, IconNotebook,
  IconPackages, IconSearch, IconServer, IconSettings, IconSparkles, IconStack2, IconTimeline,
} from '@tabler/icons-react';

// NavItemDef describes a single nav entry as data. The optional gate flags
// (adminOnly / permission / featureKey / hideOnCloud) are inert in the community
// build — its passthroughGate ignores them — and interpreted by the enterprise
// gate. Keeping them on the shared type lets both layers share this renderer.
export interface NavItemDef {
  key: string;
  label: string;
  icon: ComponentType<{ size?: number }>;
  // href builds the destination; projectId is undefined for root-nav items.
  href: (projectId?: string) => string;
  // active-state matching against the current pathname (default 'exact').
  match?: 'exact' | 'prefix';
  // activeAlso adds an extra active condition for aliased routes (rare).
  activeAlso?: (pathname: string) => boolean;
  // --- optional gates (community items set none) ---
  adminOnly?: boolean;
  permission?: string;
  featureKey?: string;
  hideOnCloud?: boolean;
}

export interface NavSection {
  title: string;
  items: NavItemDef[];
}

// NavGateResult is what a gate returns per item: whether to show it, the link
// target (real route, or an upgrade page for a locked feature), an optional lock
// badge, and an optional icon override (e.g. the file-vs-warehouse Data Sources
// icon).
export interface NavGateResult {
  visible: boolean;
  href: string;
  locked?: boolean;
  icon?: ComponentType<{ size?: number }>;
}

// NavGate resolves an item for the current deployment/role/capability context.
export type NavGate = (item: NavItemDef, projectId?: string) => NavGateResult;

// passthroughGate shows every item and links to its real route — the community
// default (no role/capability/cloud gating).
export const passthroughGate: NavGate = (item, projectId) => ({
  visible: true,
  href: item.href(projectId),
});

function itemActive(item: NavItemDef, pathname: string, href: string): boolean {
  const base = item.match === 'prefix' ? pathname.startsWith(href) : pathname === href;
  return base || (item.activeAlso?.(pathname) ?? false);
}

const sectionHeaderStyle = {
  fontSize: 10,
  fontWeight: 600,
  textTransform: 'uppercase' as const,
  letterSpacing: '0.8px',
  color: 'var(--db-text-tertiary)',
  padding: '12px 10px 6px',
};

// NavSections renders sections → items through the given gate. A section whose
// items are all filtered out (by role/permission/cloud) is dropped entirely, so
// there are never empty category headers.
export function NavSections({ sections, projectId, pathname, gate }: {
  sections: NavSection[];
  projectId?: string;
  pathname: string;
  gate: NavGate;
}) {
  return (
    <>
      {sections.map((section) => {
        const rendered = section.items
          .map((item) => ({ item, res: gate(item, projectId) }))
          .filter(({ res }) => res.visible);
        if (rendered.length === 0) return null;
        return (
          <div key={section.title}>
            <div style={sectionHeaderStyle}>{section.title}</div>
            {rendered.map(({ item, res }) => {
              const Icon = res.icon ?? item.icon;
              return (
                <NavItem
                  key={item.key}
                  href={res.href}
                  icon={<Icon size={16} />}
                  label={item.label}
                  active={itemActive(item, pathname, item.href(projectId))}
                  locked={res.locked}
                />
              );
            })}
          </div>
        );
      })}
    </>
  );
}

// COMMUNITY_NAV is the community dashboard's nav content. The enterprise overlay
// ships its own superset (see enterprise `nav-config.tsx`) — keep the two in
// sync when the community nav gains an item (the overlay replaces AppShell, so a
// community-only addition is otherwise silently lost on the enterprise build).
export const COMMUNITY_NAV: { root: NavSection[]; project: NavSection[] } = {
  root: [
    {
      title: 'Workspace',
      items: [
        { key: 'projects', label: 'Projects', icon: IconSearch, href: () => '/', match: 'exact' },
        { key: 'playbooks', label: 'Playbooks', icon: IconPackages, href: () => '/domain-packs', match: 'prefix' },
      ],
    },
    {
      title: 'Administration',
      items: [
        { key: 'system', label: 'System', icon: IconServer, href: () => '/system', match: 'prefix' },
      ],
    },
  ],
  project: [
    {
      title: 'Discover',
      items: [
        { key: 'runs', label: 'Discovery runs', icon: IconSearch, href: (id) => `/projects/${id}`, match: 'exact' },
        { key: 'insights', label: 'Insights', icon: IconBook2, href: (id) => `/projects/${id}/insights`, match: 'exact' },
        { key: 'recommendations', label: 'Recommendations', icon: IconStack2, href: (id) => `/projects/${id}/recommendations`, match: 'exact' },
        { key: 'questions', label: 'Questions', icon: IconHelpCircle, href: (id) => `/projects/${id}/questions`, match: 'exact' },
        { key: 'ledger', label: 'Ledger', icon: IconTimeline, href: (id) => `/projects/${id}/ledger`, match: 'exact' },
        { key: 'lists', label: 'Lists', icon: IconBookmark, href: (id) => `/projects/${id}/lists`, match: 'exact' },
      ],
    },
    {
      title: 'Intelligence',
      items: [
        { key: 'search', label: 'Search', icon: IconSparkles, href: (id) => `/projects/${id}/search`, match: 'exact' },
        { key: 'ask', label: 'Ask Insights', icon: IconMessageCircle, href: (id) => `/projects/${id}/ask`, match: 'exact' },
      ],
    },
    {
      title: 'Configure',
      items: [
        { key: 'settings', label: 'Settings', icon: IconSettings, href: (id) => `/projects/${id}/settings`, match: 'exact' },
        { key: 'playbook', label: 'Playbook', icon: IconNotebook, href: (id) => `/projects/${id}/prompts`, match: 'exact' },
      ],
    },
  ],
};

/* --- Nav Item Component --- */

export function NavItem({ href, icon, label, active, locked }: {
  href: string;
  icon: ReactNode;
  label: string;
  active: boolean;
  locked?: boolean;
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
        {locked && (
          <span
            title="Not on your plan"
            style={{ flexShrink: 0, display: 'flex', opacity: 0.4, color: 'var(--db-text-tertiary)' }}
          >
            <IconLock size={13} />
          </span>
        )}
      </div>
    </Link>
  );
}
