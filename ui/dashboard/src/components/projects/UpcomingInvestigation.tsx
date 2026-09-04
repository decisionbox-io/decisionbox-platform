'use client';

import { useEffect, useState } from 'react';
import { Card, Group, Stack, Text, Badge, Anchor } from '@mantine/core';
import { IconTimeline, IconArrowRight } from '@tabler/icons-react';
import Link from 'next/link';
import { api, LedgerTask, PackProposal } from '@/lib/api';

const MAX_ITEMS = 4;

// UpcomingInvestigation is a compact "what happens next" preview for the project
// home page: the open investigation threads (next-tasks + hypotheses) the
// Discovery Ledger will carry into the next run, plus any pending playbook
// changes awaiting review. It reuses the ledger view's own vocabulary (kind /
// action badges) and links out to the full Ledger.
//
// It renders nothing when there's nothing upcoming, or when the feature isn't
// enabled (community builds 404 the ledger route) — so the home page stays clean
// until the ledger has something to show.
export function UpcomingInvestigation({ projectId }: { projectId: string }) {
  const [tasks, setTasks] = useState<LedgerTask[]>([]);
  const [pending, setPending] = useState<PackProposal[]>([]);
  const [ready, setReady] = useState(false);

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

  return (
    <Card withBorder radius="md" padding="lg" mb="md">
      <Group justify="space-between" align="center" wrap="nowrap" mb="xs">
        <Group gap="xs" wrap="nowrap">
          <IconTimeline size={18} />
          <Text fw={600} size="md">What happens next</Text>
        </Group>
        <Anchor component={Link} href={ledgerHref} size="sm">
          <Group gap={4} wrap="nowrap">Open ledger <IconArrowRight size={14} /></Group>
        </Anchor>
      </Group>
      <Text size="xs" c="dimmed" mb="md">
        Carried into your next discovery run from the Discovery Ledger.
      </Text>

      {pending.length > 0 && (
        <Group gap="xs" wrap="nowrap" mb={shown.length ? 'sm' : 0}>
          <Badge size="sm" variant="light" color="yellow">{pending.length}</Badge>
          <Text size="sm">
            proposed playbook change{pending.length > 1 ? 's' : ''} awaiting your review
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
    </Card>
  );
}
