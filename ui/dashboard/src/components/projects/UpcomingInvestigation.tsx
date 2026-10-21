'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { Card, Group, Stack, Text, Badge, Anchor, Collapse, Loader, UnstyledButton } from '@mantine/core';
import { IconTimeline, IconArrowRight, IconChevronRight } from '@tabler/icons-react';
import Link from 'next/link';
import { api, LedgerTask, LedgerUpdating, PackProposal } from '@/lib/api';
import { LEDGER_UPDATE_POLL_MS, updateStartedAt } from '@/components/ledger/updating';

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
//
// A run is marked completed BEFORE its findings are folded into the ledger, so
// for a few minutes after a run finishes this card would show the threads from
// before it as though they were current. While the ledger says it is being
// updated, the card says so, checks back, and swaps in the new threads when the
// update is done. `refreshKey` changes when the page sees a run finish, which is
// the moment to look again — the card may have loaded while the run was going.
export function UpcomingInvestigation({ projectId, refreshKey }: { projectId: string; refreshKey?: string }) {
  const [tasks, setTasks] = useState<LedgerTask[]>([]);
  const [pending, setPending] = useState<PackProposal[]>([]);
  const [updating, setUpdating] = useState<LedgerUpdating | null>(null);
  // Set when the last load failed for a reason that may pass. The card then
  // keeps what it shows and asks again — which matters most for the reload a
  // run's finish triggers: fail that once and nothing else would ever ask.
  const [retrying, setRetrying] = useState(false);
  const [ready, setReady] = useState(false);
  // Every load is numbered, and only the latest may change what the card
  // shows. Without it a slow first load could land after the reload a run's
  // finish triggered, and put the pre-run threads back over the new ones.
  const generation = useRef(0);
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

  // What one load found: the ledger answered and is still being updated, it
  // answered and is done, this deployment has no ledger (404), the request
  // failed in a way that may pass, or a newer load superseded it.
  type Outcome = 'updating' | 'done' | 'absent' | 'failed' | 'superseded';

  const load = useCallback((): Promise<Outcome> => {
    const mine = ++generation.current;
    return Promise.all([
      api.getLedger(projectId),
      api.listPackProposals(projectId).catch(() => [] as PackProposal[]),
    ]).then(([lv, props]): Outcome => {
      if (mine !== generation.current) return 'superseded';
      setTasks((lv?.tasks ?? []).filter((t) => (t.status || 'open') === 'open'));
      setPending((props ?? []).filter((p) => p.status === 'proposed'));
      setUpdating(lv?.updating ?? null);
      setRetrying(false);
      setReady(true);
      return lv?.updating ? 'updating' : 'done';
    }, (err: unknown): Outcome => {
      if (mine !== generation.current) return 'superseded';
      setReady(true);
      // No ledger on this deployment (community builds 404 the route): there
      // is nothing to show and nothing to wait for. Whatever an earlier load
      // put on the card goes too — threads, or an Updating badge nothing would
      // ever clear.
      if ((err as { status?: number } | null)?.status === 404) {
        setTasks([]);
        setPending([]);
        setUpdating(null);
        setRetrying(false);
        return 'absent';
      }
      // Anything else may pass: what the card already shows stays, and it
      // asks again.
      setRetrying(true);
      return 'failed';
    });
  }, [projectId]);

  useEffect(() => {
    void load();
    // Unmounting supersedes whatever is still in flight.
    return () => { generation.current += 1; };
  }, [load, refreshKey]);

  // While an update is in progress, check back: a loop that schedules its own
  // next check until an answer says it is done — that same answer is the
  // updated threads. Only a successful answer without the flag ends it: a
  // failed check is a blip, and a superseded one means another load owns the
  // card now, which this loop keeps an eye on rather than abandoning. Keyed on
  // whether there is an update at all, never on the update object, whose
  // identity says nothing about whether anything changed.
  //
  // Also runs while a failed load is being retried, so a refresh that failed
  // once is asked again rather than left showing the old threads as current.
  const waiting = Boolean(updating) || retrying;
  useEffect(() => {
    if (!waiting) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const check = () => {
      timer = setTimeout(() => {
        void load().then((outcome) => {
          if (cancelled || outcome === 'done' || outcome === 'absent') return;
          check();
        });
      }, LEDGER_UPDATE_POLL_MS);
    };
    check();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [waiting, load]);

  // Nothing to show, unless the ledger is being built: a project's first run
  // has no threads yet, and saying they are on their way is the useful thing.
  if (!ready || (tasks.length === 0 && pending.length === 0 && !updating)) return null;
  const startedAt = updateStartedAt(updating?.since);

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
            {updating && (
              <Badge size="sm" variant="light" color="blue" tt="none" leftSection={<Loader size={10} color="blue" />}>
                Updating
              </Badge>
            )}
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
        {updating ? (
          <Text size="xs" c="blue" mt="xs" mb="md" data-testid="ledger-updating">
            {tasks.length > 0 || pending.length > 0
              ? 'The run that just finished is still being added to the ledger, so these are the threads from before it. This updates on its own when it is done'
              : 'The run that just finished is being added to the ledger. Its threads will appear here when that is done'}
            {startedAt ? ` (started ${startedAt}).` : '.'}
          </Text>
        ) : (
          <Text size="xs" c="dimmed" mt="xs" mb="md">
            Carried into your next discovery run from the Discovery Ledger.
          </Text>
        )}

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
