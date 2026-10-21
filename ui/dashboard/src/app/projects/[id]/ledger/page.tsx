'use client';

import { ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { useParams } from 'next/navigation';
import { Alert, Loader, Select, Button, Group, Stack, Text, Card, Badge, Progress, Tooltip, Collapse, UnstyledButton, ThemeIcon, SegmentedControl } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { LEDGER_UPDATE_POLL_MS, runInProgress, updateStartedAt } from '@/components/ledger/updating';
import {
  IconBook2, IconChevronRight, IconTrendingUp, IconRoute, IconGitPullRequest,
  IconMap2, IconHistory, IconAdjustments, IconListDetails, IconSitemap,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { SectionHeader, EmptyState, StatCard, SeverityBadge, AreaBadge, Th } from '@/components/common/UIComponents';
import StoryTimeline from '@/components/ledger/StoryTimeline';
import { ThreadMapWithProvider } from '@/components/ledger/ThreadMap';
import { humanise, indexGraph } from '@/components/ledger/graph';
import {
  api, ApiError, LedgerView, LedgerTask, LedgerGraph, EvolutionSettings, PackProposal, Project,
  EvolutionMode, FrontierPolicy,
} from '@/lib/api';

// Cap the "Next up" list so the ledger never renders an unbounded wall of
// tasks; the rest are summarised as "+N more".
const MAX_NEXT_UP = 12;

// The three levels of the investigation view. Story is the default: plain
// language and no graph answers "where did this come from" for most readers.
// Threads is the tree. The third level is not a view — every node in the first
// two links through to the insight page that already renders the narrative, the
// SQL, the rows, the quality caveats and the validation verdict.
type LedgerLevel = 'story' | 'threads';

const LEVELS: { value: LedgerLevel; label: string }[] = [
  { value: 'story', label: 'Story' },
  { value: 'threads', label: 'Map' },
];

const EVOLUTION_MODES: { value: EvolutionMode; label: string }[] = [
  { value: 'off', label: 'Off — record only' },
  { value: 'suggest_only', label: 'Suggest only — show, never apply' },
  { value: 'admin_approval', label: 'Admin approval — queue for review' },
  { value: 'auto', label: 'Auto — apply automatically (audited)' },
];

const FRONTIER_POLICIES: { value: FrontierPolicy; label: string }[] = [
  { value: 'breadth_first', label: 'Go wide — explore new tables & areas' },
  { value: 'depth_first', label: 'Go deep — dig into the richest findings' },
  { value: 'balanced', label: 'Balanced — a mix of new ground and depth' },
];

// Finding-status → badge colour. Kept local so the ledger view owns its own
// vocabulary rather than overloading the insight SeverityBadge's status map.
const FINDING_STATUS_COLOR: Record<string, string> = {
  confirmed: 'teal',
  monitoring: 'blue',
  changed: 'orange',
  resolved: 'gray',
  refuted: 'red',
};

const PROPOSAL_STATUS_COLOR: Record<string, string> = {
  proposed: 'yellow',
  approved: 'teal',
  applied: 'teal',
  rejected: 'gray',
  reverted: 'gray',
};

// normalizeLedger defends against a project that has no ledger yet: the API
// returns a 200 with null slices (Go nil → JSON null) for coverage/convergence/
// findings/tasks, and rendering `.length`/`.map` on null throws. Default every
// array + coverage field so the page shows an honest empty state instead.
function normalizeLedger(lv: LedgerView): LedgerView {
  return {
    coverage: {
      explored_tables: lv?.coverage?.explored_tables ?? [],
      area_depth: lv?.coverage?.area_depth ?? {},
      total_tables: lv?.coverage?.total_tables ?? 0,
      explored_catalog_items: lv?.coverage?.explored_catalog_items ?? [],
      total_catalog_items: lv?.coverage?.total_catalog_items ?? 0,
      summary: lv?.coverage?.summary ?? '',
    },
    convergence: lv?.convergence ?? [],
    findings: lv?.findings ?? [],
    tasks: lv?.tasks ?? [],
    ancestors: lv?.ancestors ?? [],
    updating: lv?.updating ?? null,
  };
}

// cubeSliced states what has been queried on the cube side as a record rather
// than as progress toward a total — a cube's slices are combinatorial, so a
// percentage of its catalog would read as a completion figure for something
// that never completes. The catalog size is omitted rather than printed as
// zero when a run could not read it.
function cubeSliced(explored: number, total: number): string {
  if (explored === 0) {
    return total > 0
      ? `None of its ${total} metrics and dimensions are recorded as queried yet.`
      : 'Nothing is recorded as queried on it yet.';
  }
  return total > 0
    ? `${explored} of its ${total} metrics and dimensions have been queried so far.`
    : `${explored} of its metrics and dimensions have been queried so far.`;
}

// Section is the single, consistent container every block on this page uses: a
// bordered card with an icon chip, a title, an optional count, and a one-line
// purpose. Using it everywhere (instead of a mix of bordered cards and bare
// lists) is what keeps the page from blending together — each block reads as a
// distinct, self-explaining unit.
function Section({
  icon, title, count, description, right, children,
}: {
  icon: ReactNode;
  title: string;
  count?: number;
  description?: string;
  right?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card withBorder padding="lg" radius="md">
      <Group justify="space-between" align="center" wrap="nowrap">
        <Group gap="xs" wrap="nowrap">
          <ThemeIcon variant="light" color="gray" size="md" radius="md">{icon}</ThemeIcon>
          <Text fw={600} size="md">{title}</Text>
          {typeof count === 'number' && <Badge size="sm" variant="light" color="gray">{count}</Badge>}
        </Group>
        {right}
      </Group>
      {description && <Text size="xs" c="dimmed" mt={6}>{description}</Text>}
      <div style={{ marginTop: 16 }}>{children}</div>
    </Card>
  );
}

// LedgerPage is the investigation view for compounding discovery. It reads top
// to bottom as one story, one titled card per step:
//   1. Overview  — the at-a-glance numbers.
//   2. Momentum  — is discovery still surfacing new things (convergence)?
//   3. Next up   — the open investigation threads to chase.
//   4. Decisions — playbook changes the run proposed, awaiting approval.
//   5. Coverage  — how much of the warehouse has been reached.
//   6. Findings  — the full finding-level table (advanced, collapsed).
//   7. History   — applied / rejected playbook changes.
//   8. Settings  — how the ledger is allowed to steer future runs.
// Rendered full-width so the tables + convergence bars use the horizontal room.
//
// The whole feature is enterprise-backed — on a community build the routes 404
// and the page shows an honest "not available" state.
export default function LedgerPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [ledger, setLedger] = useState<LedgerView | null>(null);
  const [settings, setSettings] = useState<EvolutionSettings | null>(null);
  const [proposals, setProposals] = useState<PackProposal[]>([]);
  const [loading, setLoading] = useState(true);
  const [unsupported, setUnsupported] = useState(false);
  const [savingMode, setSavingMode] = useState(false);
  const [deciding, setDeciding] = useState<string>(''); // proposal id currently being decided
  const [showFindings, setShowFindings] = useState(false); // advanced detail, collapsed by default
  const [level, setLevel] = useState<LedgerLevel>('story');
  const [graph, setGraph] = useState<LedgerGraph | null>(null);
  const [graphLoading, setGraphLoading] = useState(false);
  // The graph route is additive: an API that predates it 404s, and an older
  // deployment must still get the page it has always had.
  const [graphUnavailable, setGraphUnavailable] = useState(false);
  // Indexed once per payload — before any early return, because a hook has
  // to run on every render. Rebuilding it on an unrelated state change (the
  // findings panel opening, a follow-up expanding) would hand the map a new
  // object, and the map would read that as a new map and refit the canvas
  // out from under a reader who had panned somewhere.
  const graphIndex = useMemo(() => indexGraph(graph), [graph]);
  const [showFrontierHelp, setShowFrontierHelp] = useState(false); // frontier explainer, hidden by default
  const [openThreads, setOpenThreads] = useState<Set<string>>(new Set()); // expanded follow-up ancestry

  const toggleThread = (id: string) => setOpenThreads((prev) => {
    const next = new Set(prev);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  // Every graph and proposals request is numbered, and only the latest may
  // change the page — its success, its failure and its loading state alike.
  // The first ones can still be out when an update finishes and the page
  // reloads; landing last, they would put the pre-run map and proposals back,
  // or call a graph that just loaded unavailable.
  const graphRequest = useRef(0);
  const loadGraph = () => {
    const mine = ++graphRequest.current;
    setGraphLoading(true);
    api.getLedgerGraph(id)
      .then((g) => {
        if (mine !== graphRequest.current) return;
        setGraph(g);
        setGraphUnavailable(false);
      })
      .catch(() => { if (mine === graphRequest.current) setGraphUnavailable(true); })
      .finally(() => { if (mine === graphRequest.current) setGraphLoading(false); });
  };

  const proposalsRequest = useRef(0);
  const loadProposals = () => {
    const mine = ++proposalsRequest.current;
    api.listPackProposals(id)
      .then((p) => { if (mine === proposalsRequest.current) setProposals(p || []); })
      .catch(() => { if (mine === proposalsRequest.current) setProposals([]); });
  };

  // Fetched once, after the ledger itself has loaded. Deliberately not on
  // first paint: the graph carries a project's whole history and neither the
  // stat cards nor the open-thread queue need it. And never on a deployment
  // where the ledger routes 404 — the page already says the feature is not
  // available, and a second request to the same absent API tells nobody
  // anything.
  useEffect(() => {
    if (loading || unsupported || !ledger) return;
    if (graph || graphLoading || graphUnavailable) return;
    loadGraph();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, loading, unsupported, ledger]);

  // Keeping the page current across a run and its ledger update.
  //
  // A run is marked completed before its findings are folded into the ledger,
  // so for a while after a run finishes this whole page describes the state
  // from before it — and a page opened while the run was going cannot tell
  // from the ledger alone that it has finished. So ONE loop watches both: while
  // a run is in progress or the ledger says it is being updated, it checks the
  // run and the ledger; when neither is true any more, it reloads the whole
  // page once — the map, the story and the proposals are from before the run
  // too — and stops. One loop, because two that handed over to each other
  // dropped the page at every seam between them.
  //
  // Every ledger load is numbered like the graph and proposals loads, and only
  // the latest may set it: a slow first load landing after the loop had seen
  // the update would put the pre-run ledger back and stop the loop mid-update.
  const ledgerRequest = useRef(0);
  const [watching, setWatching] = useState(false);
  useEffect(() => {
    api.getProjectStatus(id)
      .then((s) => { if (runInProgress(s?.run?.status)) setWatching(true); })
      .catch(() => {});
  }, [id]);
  // Keyed on whether there is anything to wait for, never on the update
  // object, whose identity says nothing about whether anything changed.
  const waiting = watching || Boolean(ledger?.updating);
  useEffect(() => {
    if (!waiting) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const check = () => {
      timer = setTimeout(async () => {
        try {
          const mineLedger = ++ledgerRequest.current;
          const [status, lv] = await Promise.all([
            api.getProjectStatus(id).catch(() => null),
            api.getLedger(id),
          ]);
          if (cancelled) return;
          const next = normalizeLedger(lv);
          // Still waiting: a run going, a ledger being updated, or no way to
          // tell whether a run is going because its status did not answer.
          if (status === null || runInProgress(status?.run?.status) || next.updating) {
            if (mineLedger === ledgerRequest.current) setLedger(next);
            check();
            return;
          }
          // Done. Everything comes in together, and the banner goes only when
          // the whole page is current. Anything still out from before is
          // superseded first, so it cannot land on top of this.
          const mineGraph = ++graphRequest.current;
          const mineProposals = ++proposalsRequest.current;
          const [g, props] = await Promise.all([
            graphUnavailable ? Promise.resolve(null) : api.getLedgerGraph(id),
            api.listPackProposals(id),
          ]);
          if (cancelled) return;
          if (g && mineGraph === graphRequest.current) {
            setGraph(g);
            setGraphUnavailable(false);
            setGraphLoading(false);
          }
          if (mineProposals === proposalsRequest.current) setProposals(props || []);
          if (mineLedger === ledgerRequest.current) setLedger(next);
          // Last, so nothing above is lost to this loop's own cleanup.
          setWatching(false);
        } catch {
          // Not an answer: keep waiting and try again, rather than leave
          // half the page from before the run with nothing saying so.
          if (!cancelled) check();
        }
      }, LEDGER_UPDATE_POLL_MS);
    };
    check();
    return () => { cancelled = true; clearTimeout(timer); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, waiting]);

  useEffect(() => {
    api.getProject(id).then(setProject).catch(() => {});
    const mine = ++ledgerRequest.current;
    Promise.all([
      api.getLedger(id),
      api.getEvolutionSettings(id).catch(() => null),
    ])
      .then(([lv, st]) => {
        if (mine === ledgerRequest.current) setLedger(normalizeLedger(lv));
        setSettings(st);
        setUnsupported(false);
        loadProposals();
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setUnsupported(true);
      })
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const saveSettings = async (patch: Partial<Pick<EvolutionSettings, 'evolution_mode' | 'frontier_policy'>>) => {
    if (!settings) return;
    const next = { ...settings, ...patch };
    setSettings(next);
    setSavingMode(true);
    try {
      const saved = await api.updateEvolutionSettings(id, {
        evolution_mode: next.evolution_mode,
        frontier_policy: next.frontier_policy,
      });
      setSettings(saved);
      notifications.show({ title: 'Saved', message: 'Evolution settings updated', color: 'green' });
    } catch (e: unknown) {
      setSettings(settings);
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingMode(false);
    }
  };

  const decide = async (proposalId: string, action: 'approve' | 'reject' | 'revert') => {
    setDeciding(proposalId);
    try {
      await api.decidePackProposal(id, proposalId, action);
      notifications.show({ title: 'Done', message: `Proposal ${action}d`, color: 'green' });
      loadProposals();
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setDeciding('');
    }
  };

  if (loading) {
    return <Shell fullWidth><Group justify="center" p="xl"><Loader /></Group></Shell>;
  }

  if (unsupported || !ledger) {
    return (
      <Shell fullWidth>
        <EmptyState
          icon={<IconBook2 size={40} />}
          title="Discovery Ledger not available"
          description="Compounding discovery is an enterprise feature. Enable it on this deployment (SOURCES_ENABLED + the reflection flag) to accumulate a per-project investigation ledger across runs."
        />
      </Shell>
    );
  }

  const explored = ledger.coverage.explored_tables?.length ?? 0;
  const total = ledger.coverage.total_tables ?? 0;
  const frontier = Math.max(0, total - explored);
  // Cube-shaped datasources have no tables, so every count above is about the
  // table side only. Saying so is what stops "Frontier 0" from reading as
  // "nothing left to look at" on a project that also has a cube.
  const cubeItems = ledger.coverage.total_catalog_items ?? 0;
  const cubeExplored = ledger.coverage.explored_catalog_items?.length ?? 0;
  // Either half is enough, mirroring the agent-side renderer. A run whose cube
  // catalog could not be read records a total of zero while the slices earlier
  // runs recorded are still carried — testing only the total would hide them.
  const hasCube = cubeItems > 0 || cubeExplored > 0;
  const latest = ledger.convergence.length > 0 ? ledger.convergence[ledger.convergence.length - 1] : null;
  const pending = proposals.filter((p) => p.status === 'proposed');
  const decided = proposals.filter((p) => p.status !== 'proposed');

  // Resolve a follow-up task's parent chain. The parent can be in EITHER list:
  // `ancestors` carries the closed ones, which are absent from the live queue,
  // but a thread often continues one that is still open — and that one is in
  // `tasks`. Looking only at `ancestors` reported every such parent as "no
  // longer available", which on a project whose follow-ups all continue open
  // threads meant every one of them.
  const ancestorById = new Map<string, LedgerTask>(
    [...(ledger.ancestors ?? []), ...ledger.tasks].map((a) => [a.id, a]),
  );
  const chainOf = (task: LedgerTask): LedgerTask[] => {
    const chain: LedgerTask[] = [];
    const guard = new Set<string>();
    let cursor = task.supersedes;
    while (cursor && ancestorById.has(cursor) && !guard.has(cursor)) {
      guard.add(cursor);
      const parent = ancestorById.get(cursor)!;
      chain.push(parent);
      cursor = parent.supersedes;
    }
    return chain;
  };

  return (
    <Shell fullWidth>
      <Stack gap="lg">
        {/* Page header */}
        <div>
          <SectionHeader title="Discovery Ledger" />
          <Text size="sm" c="dimmed" mt={4}>
            {project?.name ? `${project.name} — ` : ''}the accumulated investigation state. Each run builds on this instead of starting fresh.
          </Text>
        </div>

        {ledger.updating && (
          <Alert
            color="blue"
            variant="light"
            icon={<Loader size={16} color="blue" />}
            title="Updating from the run that just finished"
          >
            Everything below is the ledger from before that run. This page refreshes on its own when
            the update is done, usually within a few minutes
            {updateStartedAt(ledger.updating.since) ? ` (started ${updateStartedAt(ledger.updating.since)})` : ''}.
          </Alert>
        )}

        {/* 1. Overview — the at-a-glance numbers */}
        <Group grow align="stretch">
          <StatCard label="Tables explored" value={String(explored)} subtitle={total > 0 ? `of ${total} in catalog` : undefined} />
          <Tooltip
            label={hasCube
              ? "The frontier is the part of your data the investigation hasn't looked at yet — tables it has not explored. It counts tables only: this project also has a cube-shaped datasource, which has none and is never finished."
              : "The frontier is the part of your data the investigation hasn't looked at yet — tables it has not explored."}
            multiline w={280} openDelay={200}
          >
            <div><StatCard label="Frontier" value={String(frontier)} subtitle="tables not yet explored" /></div>
          </Tooltip>
          <StatCard label="Findings" value={String(ledger.findings.length)} subtitle="carried across runs" />
          <StatCard
            label="New last run"
            value={latest ? `${Math.round(latest.marginal_ratio * 100)}%` : '—'}
            subtitle={latest ? `${latest.new_findings} new of ${latest.total_findings}` : 'no runs yet'}
          />
        </Group>

        {/* The level picker. Story is the default; Threads is the tree. */}
        <Group justify="space-between" align="center" wrap="wrap">
          <SegmentedControl
            value={level}
            onChange={(v) => setLevel(v as LedgerLevel)}
            data={LEVELS}
            size="sm"
          />
          <Text size="xs" c="dimmed">
            {level === 'story'
              ? 'What each run found, in plain language. Every name links to its evidence.'
              : 'The investigation as a map. Scroll to move around, pinch or Ctrl/⌘ + scroll to zoom, click a card for its detail.'}
          </Text>
        </Group>

        {/* Levels 1 and 2. Level 3 is not a view: every node here links through
            to the insight page that already renders the narrative, the SQL, the
            rows, the quality caveats and the validation verdict. */}
        <Section
          icon={<IconSitemap size={16} />}
          title={level === 'story' ? 'What happened' : 'Investigation map'}
          description={level === 'story'
            ? 'One card per run, newest first — what it found, which threads it closed and why.'
            : 'How the investigation was worked out, in the order it happened: one row per run, oldest at the top, each read left to right — the threads it picked up, what it found, what that drove, and the threads it left for the next run. A dashed card is earlier work a later run took up again. Archived findings are dimmed, not hidden.'}
        >
          {/* A labelled region: the same finding can legitimately appear here
              and in the findings table below, and this is what tells a screen
              reader (and a test) which one it is looking at. */}
          <div role="region" aria-label="Investigation view">
            {graphLoading && <Group justify="center" p="md"><Loader size="sm" /></Group>}
            {!graphLoading && graphUnavailable && (
              <Text size="sm" c="dimmed">
                This deployment&apos;s API does not serve the investigation graph yet. The
                sections below are unaffected.
              </Text>
            )}
            {!graphLoading && !graphUnavailable && graph && (
              level === 'story'
                ? <StoryTimeline projectId={id} graph={graph} index={graphIndex} />
                : <ThreadMapWithProvider projectId={id} graph={graph} index={graphIndex} />
            )}
          </div>
        </Section>

        {/* 2. Momentum — is discovery still surfacing new things? The Story
            timeline folds the same signal into each run's card, so this stays
            for the Threads view and for a project whose graph is unavailable. */}
        {ledger.convergence.length > 0 && (level !== 'story' || graphUnavailable) && (
          <Section
            icon={<IconTrendingUp size={16} />}
            title="Momentum"
            description="New findings per run. A falling line means discovery is converging — the project is well understood."
          >
            <Stack gap={8}>
              {ledger.convergence.slice(-12).map((c) => (
                <Group key={c.run_id} gap="sm" wrap="nowrap">
                  <Text size="xs" c="dimmed" w={90} style={{ flexShrink: 0 }}>{new Date(c.date).toLocaleDateString()}</Text>
                  <Progress value={Math.round(c.marginal_ratio * 100)} size="md" radius="sm" style={{ flex: 1 }} />
                  <Text size="xs" c="dimmed" w={120} ta="right" style={{ flexShrink: 0 }}>{c.new_findings} new / {c.total_findings}</Text>
                </Group>
              ))}
            </Stack>
          </Section>
        )}

        {/* 3. Next up — the open investigation threads */}
        {ledger.tasks.length > 0 && (
          <Section
            icon={<IconRoute size={16} />}
            title="Next up"
            count={ledger.tasks.length}
            description="Open investigation threads the next run should pick up first."
          >
            <Stack gap="md">
              {ledger.tasks.slice(0, MAX_NEXT_UP).map((t) => {
                const chain = t.supersedes ? chainOf(t) : [];
                const expanded = openThreads.has(t.id);
                return (
                  <Group key={t.id} gap="sm" wrap="nowrap" align="flex-start">
                    <Badge size="sm" variant="light" tt="none" color={t.kind === 'hypothesis' ? 'grape' : 'blue'} style={{ flexShrink: 0 }}>{humanise(t.kind)}</Badge>
                    <div style={{ minWidth: 0, flex: 1 }}>
                      <Group gap="xs" wrap="nowrap">
                        <Text size="sm" fw={600}>{t.title || t.text}</Text>
                        {t.supersedes && (
                          <UnstyledButton onClick={() => toggleThread(t.id)} style={{ flexShrink: 0 }} aria-expanded={expanded}>
                            <Badge
                              size="xs"
                              variant="outline"
                              color="gray"
                              tt="none"
                              style={{ cursor: 'pointer' }}
                              rightSection={<IconChevronRight size={10} style={{ display: 'block', transform: expanded ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }} />}
                            >
                              Follow-up{chain.length > 1 ? ` · ${chain.length}` : ''}
                            </Badge>
                          </UnstyledButton>
                        )}
                      </Group>
                      {t.title && t.title !== t.text && <Text size="xs" c="dimmed" mt={2}>{t.text}</Text>}
                      {t.supersedes && (
                        <Collapse in={expanded}>
                          <div style={{ marginTop: 8, paddingLeft: 12, borderLeft: '2px solid var(--db-border-default)' }}>
                            <Text size="xs" c="dimmed" mb={6}>Grew out of {chain.length > 1 ? 'these earlier threads' : 'this earlier thread'}:</Text>
                            {chain.length === 0 ? (
                              <Text size="xs" c="dimmed">Parent thread is no longer available.</Text>
                            ) : chain.map((a) => (
                              <div key={a.id} style={{ marginBottom: 6 }}>
                                <Group gap="xs" wrap="nowrap" align="center">
                                  <Badge size="xs" variant="light" tt="none" color={a.status === 'dropped' ? 'gray' : 'teal'} style={{ flexShrink: 0 }}>{humanise(a.status)}</Badge>
                                  <Text size="xs" fw={500}>{a.title || a.text}</Text>
                                </Group>
                                {a.title && a.title !== a.text && <Text size="xs" c="dimmed" ml={4}>{a.text}</Text>}
                              </div>
                            ))}
                          </div>
                        </Collapse>
                      )}
                    </div>
                  </Group>
                );
              })}
            </Stack>
            {ledger.tasks.length > MAX_NEXT_UP && (
              <Text size="xs" c="dimmed" mt="sm">
                +{ledger.tasks.length - MAX_NEXT_UP} more open thread{ledger.tasks.length - MAX_NEXT_UP > 1 ? 's' : ''}
              </Text>
            )}
          </Section>
        )}

        {/* 4. Decisions — playbook changes awaiting approval */}
        {pending.length > 0 && (
          <Section
            icon={<IconGitPullRequest size={16} />}
            title="Playbook changes to review"
            count={pending.length}
            description="Analysis-area changes the run proposed. Approve to apply them to this project's playbook, or reject."
          >
            <Stack gap="md">
              {pending.map((p) => (
                <Group key={p.id} justify="space-between" wrap="nowrap" align="flex-start">
                  <div style={{ minWidth: 0 }}>
                    <Group gap="xs" mb={2}>
                      <Badge size="sm" variant="light" tt="none">{humanise(p.action)}</Badge>
                      <Text size="sm" fw={600}>{p.area_name || p.area_id}</Text>
                    </Group>
                    <Text size="sm" c="dimmed">{p.rationale}</Text>
                  </div>
                  <Group gap="xs" wrap="nowrap" style={{ flexShrink: 0 }}>
                    <Button size="xs" color="teal" loading={deciding === p.id} onClick={() => decide(p.id, 'approve')}>Approve</Button>
                    <Button size="xs" variant="default" loading={deciding === p.id} onClick={() => decide(p.id, 'reject')}>Reject</Button>
                  </Group>
                </Group>
              ))}
            </Stack>
          </Section>
        )}

        {/* 5. Coverage — how much of the warehouse has been reached */}
        {(ledger.coverage.summary || hasCube) && (
          <Section
            icon={<IconMap2 size={16} />}
            title="Coverage"
            description="What the investigation has reached across the warehouse so far."
          >
            <Stack gap="xs">
              {ledger.coverage.summary && <Text size="sm">{ledger.coverage.summary}</Text>}
              {hasCube && (
                <Text size="sm" c="dimmed">
                  This project also has a cube-shaped datasource, which has no tables — the counts above are the
                  warehouse side only. {cubeSliced(cubeExplored, cubeItems)} A cube is never fully explored: its
                  slices are combinatorial, so it is judged by whether a new one still turns up something, not by
                  how much of it is left.
                </Text>
              )}
            </Stack>
          </Section>
        )}

        {/* 6. History — applied / rejected playbook changes */}
        {decided.length > 0 && (
          <Section
            icon={<IconHistory size={16} />}
            title="Change history"
            count={decided.length}
            description="Playbook changes that have already been applied, rejected, or reverted."
          >
            <Stack gap="sm">
              {decided.map((p) => (
                <Group key={p.id} justify="space-between" wrap="nowrap" align="center">
                  <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
                    <Badge size="sm" variant="light" tt="none" color={PROPOSAL_STATUS_COLOR[p.status] || 'gray'}>{humanise(p.status)}</Badge>
                    <Badge size="sm" variant="outline" tt="none">{humanise(p.action)}</Badge>
                    <Text size="sm" lineClamp={1}>{p.area_name || p.area_id}</Text>
                    {p.decided_by && <Text size="xs" c="dimmed">by {p.decided_by}</Text>}
                  </Group>
                  {p.status === 'applied' && (
                    <Button size="xs" variant="default" loading={deciding === p.id} onClick={() => decide(p.id, 'revert')}>Revert</Button>
                  )}
                </Group>
              ))}
            </Stack>
          </Section>
        )}

        {/* 8. Settings — how the ledger steers future runs */}
        {settings && (
          <Section
            icon={<IconAdjustments size={16} />}
            title="Evolution settings"
            description="How the ledger is allowed to steer future runs — whether proposed playbook changes apply automatically, and which direction the next run favours."
          >
            <Group align="flex-end" gap="md">
              <Select
                label="Mode"
                description="Governs proposed analysis-area changes + self-directed next-tasks"
                data={EVOLUTION_MODES}
                value={settings.evolution_mode}
                disabled={savingMode}
                onChange={(v) => v && saveSettings({ evolution_mode: v as EvolutionMode })}
                w={320}
              />
              <Select
                label="Frontier policy"
                description="How the next run spends its effort"
                data={FRONTIER_POLICIES}
                value={settings.frontier_policy}
                disabled={savingMode}
                onChange={(v) => v && saveSettings({ frontier_policy: v as FrontierPolicy })}
                w={340}
              />
            </Group>
            <div style={{ marginTop: 12 }}>
              <UnstyledButton onClick={() => setShowFrontierHelp((v) => !v)} aria-expanded={showFrontierHelp}>
                <Group gap={4} wrap="nowrap">
                  <IconChevronRight size={14} style={{ transform: showFrontierHelp ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }} />
                  <Text size="xs" c="dimmed">What do frontier, breadth and depth mean?</Text>
                </Group>
              </UnstyledButton>
              <Collapse in={showFrontierHelp}>
                <Card withBorder radius="sm" padding="sm" mt="xs">
                  <Text size="xs" c="dimmed">
                    <b>Frontier</b> — the parts of your data the investigation has not looked at yet (tables and areas it has not explored). The <b>frontier policy</b> tells the next run where to spend its time:
                  </Text>
                  <Stack gap={4} mt={8}>
                    <Text size="xs" c="dimmed"><b>Go wide (breadth)</b> — cover more ground: explore new tables and areas it has not touched yet, for a broad picture of what is there.</Text>
                    <Text size="xs" c="dimmed"><b>Go deep (depth)</b> — keep digging into the areas already showing the strongest signals, instead of spreading out.</Text>
                    <Text size="xs" c="dimmed"><b>Balanced</b> — a mix of both: some new ground, some deeper digging. A sensible default.</Text>
                  </Stack>
                </Card>
              </Collapse>
            </div>
          </Section>
        )}

        {/* 8. Findings — the full finding-level detail, collapsed by default, at the bottom */}
        <Card withBorder padding="lg" radius="md">
          <UnstyledButton onClick={() => setShowFindings((v) => !v)} style={{ width: '100%' }}>
            <Group justify="space-between" align="center" wrap="nowrap">
              <Group gap="xs" wrap="nowrap">
                <ThemeIcon variant="light" color="gray" size="md" radius="md"><IconListDetails size={16} /></ThemeIcon>
                <Text fw={600} size="md">Findings</Text>
                <Badge size="sm" variant="light" color="gray">{ledger.findings.length}</Badge>
              </Group>
              <Group gap={6} wrap="nowrap">
                <Text size="xs" c="dimmed">{showFindings ? 'Hide detail' : 'Show detail'}</Text>
                <IconChevronRight size={16} style={{ transform: showFindings ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }} />
              </Group>
            </Group>
          </UnstyledButton>
          <Text size="xs" c="dimmed" mt={6}>
            The full finding-level table behind the ledger — severity, status, and how often each has recurred.
          </Text>
          <Collapse in={showFindings}>
            <div style={{ marginTop: 16 }}>
              {ledger.findings.length === 0 ? (
                <EmptyState icon={<IconBook2 size={32} />} title="No findings yet" description="Findings accumulate here as discovery runs complete." />
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                  <thead>
                    <tr>
                      <Th width="48%">Finding</Th>
                      <Th width="18%">Area</Th>
                      <Th width="12%">Severity</Th>
                      <Th width="12%">Status</Th>
                      <Th width="10%">Seen</Th>
                    </tr>
                  </thead>
                  <tbody>
                    {ledger.findings.map((f) => (
                      <tr key={f.id} style={{ borderBottom: '1px solid var(--db-border-default)' }}>
                        <td style={{ padding: '10px 12px' }}>
                          <Tooltip label={f.description || f.name} multiline w={360} disabled={!f.description} openDelay={300}>
                            <Text size="sm" fw={600} lineClamp={1}>{f.name}</Text>
                          </Tooltip>
                        </td>
                        <td style={{ padding: '10px 12px' }}>{f.area ? <AreaBadge area={f.area} /> : <Text size="xs" c="dimmed">—</Text>}</td>
                        <td style={{ padding: '10px 12px' }}><SeverityBadge severity={f.severity} type="severity" /></td>
                        <td style={{ padding: '10px 12px' }}>
                          <Badge size="sm" variant="light" tt="none" color={FINDING_STATUS_COLOR[f.status] || 'gray'}>{humanise(f.status)}</Badge>
                        </td>
                        <td style={{ padding: '10px 12px' }}><Text size="sm">{f.seen_count}×</Text></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </Collapse>
        </Card>
      </Stack>
    </Shell>
  );
}
