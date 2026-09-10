'use client';

import { useEffect, useState } from 'react';
import { Card, Group, Stack, Text, Badge, Anchor, Collapse, UnstyledButton } from '@mantine/core';
import { IconTimeline, IconArrowRight, IconChevronRight } from '@tabler/icons-react';
import Link from 'next/link';
import { api, LedgerTask, PackProposal } from '@/lib/api';

const MAX_ITEMS = 4;
// Persisted (per-browser) collapsed preference for the home-page "What happens
// next" card. Global — the user's choice is remembered across projects.
const STORAGE_KEY = 'dbx:whats-next:collapsed';

// UpcomingInvestigation is a compact "what happens next" preview for the project
// home page: the open investigation threads (next-tasks + hypotheses) the
// Discovery Ledger will carry into the next run, plus any pending playbook
// changes awaiting review. It reuses the ledger view's own vocabulary (kind /
// action badges) and links out to the full Ledger. The card is collapsible and
// the choice is remembered in the browser (localStorage).
//
// It renders nothing when there's nothing upcoming, or when the feature isn't
// enabled (community builds 404 the ledger route) — so the home page stays clean
// until the ledger has something to show.
export function UpcomingInvestigation({ projectId }: { projectId: string }) {
  const [tasks, setTasks] = useState<LedgerTask[]>([]);
  const [pending, setPending] = useState<PackProposal[]>([]);
  const [ready, setReady] = useState(false);
  // Restore the persisted collapsed/expanded choice via a lazy initializer. The
  // window guard keeps SSR safe, and there's no hydration mismatch because the
  // card renders nothing (ready=false) on the server and the first client paint.
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return typeof window !== 'undefined' && localStorage.getItem(STORAGE_KEY) === '1';
    } catch {
      return false;
    }
  });

  const toggleCollapsed = () => setCollapsed((c) => {
    const next = !c;
    try { localStorage.setItem(STORAGE_KEY, next ? '1' : '0'); } catch { /* ignore */ }
    return next;
  });

  useEffect(() => {
    let alive = true;
    Promise.all([
      api.getLedger(projectId).catch(() => null),
      api.listPackProposals(projectId).catch(() => [] as PackProposal[]),
    ]).then(([lv, props]) => {
      if (!alive) return;
      setTasks((lv?.tasks ?? []).filter((t) => (t.status || 'open') === 'open'));
      setPending((props ?? []).filter((p) => p.status === 'proposed'));
      setReady(true);
    });
    return () => { alive = false; };
  }, [projectId]);

  if (!ready || (tasks.length === 0 && pending.length === 0)) return null;

  const shown = tasks.slice(0, MAX_ITEMS);
  const moreTasks = tasks.length - shown.length;
  const ledgerHref = `/projects/${projectId}/ledger`;
  const collapsedSummary = [
    pending.length > 0 ? `${pending.length} change${pending.length > 1 ? 's' : ''}` : null,
    tasks.length > 0 ? `${tasks.length} thread${tasks.length > 1 ? 's' : ''}` : null,
  ].filter(Boolean).join(' · ');

  return (
    <Card withBorder radius="md" padding="lg" mb="md">
      <Group justify="space-between" align="center" wrap="nowrap">
        <UnstyledButton onClick={toggleCollapsed} aria-expanded={!collapsed} style={{ flex: 1, minWidth: 0 }}>
          <Group gap="xs" wrap="nowrap">
            <IconChevronRight
              size={18}
              style={{ flexShrink: 0, transform: collapsed ? 'none' : 'rotate(90deg)', transition: 'transform 150ms ease' }}
            />
            <IconTimeline size={18} style={{ flexShrink: 0 }} />
            <Text fw={600} size="md">What happens next</Text>
            {collapsed && collapsedSummary && (
              <Text size="xs" c="dimmed">{collapsedSummary}</Text>
            )}
          </Group>
        </UnstyledButton>
        <Anchor component={Link} href={ledgerHref} size="sm" style={{ flexShrink: 0 }}>
          <Group gap={4} wrap="nowrap">Open ledger <IconArrowRight size={14} /></Group>
        </Anchor>
      </Group>

      <Collapse in={!collapsed}>
        <Text size="xs" c="dimmed" mt="xs" mb="md">
          Carried into your next discovery run from the Discovery Ledger.
        </Text>

        {pending.length > 0 && (
          <Group gap="xs" wrap="nowrap" mb={shown.length ? 'sm' : 0}>
            <Badge size="sm" variant="light" color="yellow">{pending.length}</Badge>
            <Text size="sm">
              Proposed playbook change{pending.length > 1 ? 's' : ''} awaiting your review
            </Text>
          </Group>
        )}

        <Stack gap="xs">
          {shown.map((t) => (
            <Group key={t.id} gap="sm" wrap="nowrap" align="flex-start">
              <Badge size="xs" variant="light" color={t.kind === 'hypothesis' ? 'grape' : 'blue'} style={{ flexShrink: 0 }}>
                {t.kind.replace('_', ' ')}
              </Badge>
              <Text size="sm" lineClamp={1}>{t.title || t.text}</Text>
            </Group>
          ))}
        </Stack>

        {moreTasks > 0 && (
          <Text size="xs" c="dimmed" mt="xs">
            +{moreTasks} more open thread{moreTasks > 1 ? 's' : ''} in the ledger
          </Text>
        )}
      </Collapse>
    </Card>
  );
}
