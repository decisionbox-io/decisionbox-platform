'use client';

import Link from 'next/link';
import { Badge, Box, ScrollArea, Stack, Text } from '@mantine/core';

export interface RelatedItem {
  id: string;
  title: string;
  href: string;
  badge?: { label: string; color?: string };
  subtitle?: string;
}

interface Props {
  relatedLabel: string;
  related: RelatedItem[];
  similarLabel?: string;
  similar?: RelatedItem[];
}

// RelatedSidebar renders a TOC-style quick-jump list for a detail page.
// On ≥ lg viewports it's meant to be placed in a sticky right column.
// On < lg viewports, use RelatedChipStrip (exported separately) at the top of
// the content column instead — it's the same data flattened to a horizontal
// chip strip for mobile and narrow windows.
export default function RelatedSidebar({ relatedLabel, related, similarLabel, similar }: Props) {
  if (related.length === 0 && (!similar || similar.length === 0)) return null;
  return (
    <Stack gap="md">
      {related.length > 0 && (
        <Section title={relatedLabel} items={related} />
      )}
      {similar && similar.length > 0 && similarLabel && (
        <Section title={similarLabel} items={similar} muted />
      )}
    </Stack>
  );
}

// RelatedChipStrip is the mobile counterpart: a horizontally-scrollable row
// of chips that mirrors the items in the sidebar. Use at the top of the
// content column on narrow screens.
export function RelatedChipStrip({ relatedLabel, related, similar }: Props) {
  const all = [
    ...related.map(it => ({ ...it, group: relatedLabel })),
    ...(similar || []).map(it => ({ ...it, group: 'similar' })),
  ];
  if (all.length === 0) return null;
  return (
    <ScrollArea.Autosize scrollbars="x" mah={60} type="auto">
      <div style={{ display: 'flex', gap: 6, padding: '4px 0' }}>
        {all.map(it => (
          <Link key={it.id} href={it.href} style={{ textDecoration: 'none', flexShrink: 0 }}>
            <div style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              padding: '6px 10px',
              borderRadius: 'var(--db-radius)',
              border: '1px solid var(--db-border-default)',
              background: 'var(--db-bg-white)',
              fontSize: 12,
              color: 'var(--db-text-primary)',
              maxWidth: 220,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              cursor: 'pointer',
            }}>
              {it.badge && (
                <Badge size="xs" color={it.badge.color} variant="light">{it.badge.label}</Badge>
              )}
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{it.title}</span>
            </div>
          </Link>
        ))}
      </div>
    </ScrollArea.Autosize>
  );
}

function Section({ title, items, muted }: { title: string; items: RelatedItem[]; muted?: boolean }) {
  return (
    <Box>
      <Text size="xs" fw={600} c="dimmed" tt="uppercase" mb={6} style={{ letterSpacing: '0.5px' }}>
        {title}
      </Text>
      <Stack gap={4}>
        {items.map(it => (
          <Link
            key={it.id}
            href={it.href}
            style={{ textDecoration: 'none', color: 'inherit' }}
          >
            <div
              style={{
                padding: '8px 10px',
                borderRadius: 'var(--db-radius)',
                border: '1px solid var(--db-border-default)',
                background: 'var(--db-bg-white)',
                transition: 'border-color 120ms ease, background-color 120ms ease',
                opacity: muted ? 0.85 : 1,
              }}
              onMouseEnter={e => { e.currentTarget.style.borderColor = 'var(--db-border-strong)'; }}
              onMouseLeave={e => { e.currentTarget.style.borderColor = 'var(--db-border-default)'; }}
            >
              <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 6 }}>
                <Text size="xs" fw={500} lineClamp={2} style={{ flex: 1, minWidth: 0 }}>
                  {it.title}
                </Text>
                {it.badge && (
                  <Badge size="xs" color={it.badge.color} variant="light" style={{ flexShrink: 0 }}>
                    {it.badge.label}
                  </Badge>
                )}
              </div>
              {it.subtitle && (
                <Text size="xs" c="dimmed" mt={2} lineClamp={1}>{it.subtitle}</Text>
              )}
            </div>
          </Link>
        ))}
      </Stack>
    </Box>
  );
}
