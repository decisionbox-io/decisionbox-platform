'use client';

import { ReactNode, useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { Loader, Select, Button, Group, Stack, Text, Card, Badge, Progress, Tooltip, Collapse, UnstyledButton, ThemeIcon } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconBook2, IconChevronRight, IconTrendingUp, IconRoute, IconGitPullRequest,
  IconMap2, IconHistory, IconAdjustments, IconListDetails,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { SectionHeader, EmptyState, StatCard, SeverityBadge, AreaBadge, Th } from '@/components/common/UIComponents';
import {
  api, ApiError, LedgerView, LedgerTask, EvolutionSettings, PackProposal, Project,
  EvolutionMode, FrontierPolicy,
} from '@/lib/api';

// Cap the "Next up" list so the ledger never renders an unbounded wall of
// tasks; the rest are summarised as "+N more".
const MAX_NEXT_UP = 12;

const EVOLUTION_MODES: { value: EvolutionMode; label: string }[] = [
  { value: 'off', label: 'Off — record only' },
  { value: 'suggest_only', label: 'Suggest only — show, never apply' },
  { value: 'admin_approval', label: 'Admin approval — queue for review' },
  { value: 'auto', label: 'Auto — apply automatically (audited)' },
];

const FRONTIER_POLICIES: { value: FrontierPolicy; label: string }[] = [
  { value: 'breadth_first', label: 'Breadth first — tile the frontier' },
  { value: 'depth_first', label: 'Depth first — drill the richest seam' },
  { value: 'balanced', label: 'Balanced' },
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
      summary: lv?.coverage?.summary ?? '',
    },
    convergence: lv?.convergence ?? [],
    findings: lv?.findings ?? [],
    tasks: lv?.tasks ?? [],
    ancestors: lv?.ancestors ?? [],
  };
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
  const [openThreads, setOpenThreads] = useState<Set<string>>(new Set()); // expanded follow-up ancestry

  const toggleThread = (id: string) => setOpenThreads((prev) => {
    const next = new Set(prev);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  const loadProposals = () => {
    api.listPackProposals(id)
      .then((p) => setProposals(p || []))
      .catch(() => setProposals([]));
  };

  useEffect(() => {
    api.getProject(id).then(setProject).catch(() => {});
    Promise.all([
      api.getLedger(id),
      api.getEvolutionSettings(id).catch(() => null),
    ])
      .then(([lv, st]) => {
        setLedger(normalizeLedger(lv));
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
  const latest = ledger.convergence.length > 0 ? ledger.convergence[ledger.convergence.length - 1] : null;
  const pending = proposals.filter((p) => p.status === 'proposed');
  const decided = proposals.filter((p) => p.status !== 'proposed');

  // Resolve a follow-up task's parent chain from the embedded ancestors (closed
  // tasks aren't in the live queue). Walks supersedes links, guarding cycles, so
  // a multi-level thread (A → B → C) shows the whole lineage.
  const ancestorById = new Map((ledger.ancestors ?? []).map((a) => [a.id, a]));
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

        {/* 1. Overview — the at-a-glance numbers */}
        <Group grow align="stretch">
          <StatCard label="Tables explored" value={String(explored)} subtitle={total > 0 ? `of ${total} in catalog` : undefined} />
          <StatCard label="Frontier" value={String(frontier)} subtitle="tables not yet touched" />
          <StatCard label="Findings" value={String(ledger.findings.length)} subtitle="carried across runs" />
          <StatCard
            label="New last run"
            value={latest ? `${Math.round(latest.marginal_ratio * 100)}%` : '—'}
            subtitle={latest ? `${latest.new_findings} new of ${latest.total_findings}` : 'no runs yet'}
          />
        </Group>

        {/* 2. Momentum — is discovery still surfacing new things? */}
        {ledger.convergence.length > 0 && (
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
                    <Badge size="sm" variant="light" color={t.kind === 'hypothesis' ? 'grape' : 'blue'} style={{ flexShrink: 0 }}>{t.kind.replace('_', ' ')}</Badge>
                    <div style={{ minWidth: 0, flex: 1 }}>
                      <Group gap="xs" wrap="nowrap">
                        <Text size="sm" fw={600}>{t.title || t.text}</Text>
                        {t.supersedes && (
                          <UnstyledButton onClick={() => toggleThread(t.id)} style={{ flexShrink: 0 }} aria-expanded={expanded}>
                            <Badge
                              size="xs"
                              variant="outline"
                              color="gray"
                              style={{ cursor: 'pointer' }}
                              rightSection={<IconChevronRight size={10} style={{ display: 'block', transform: expanded ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }} />}
                            >
                              follow-up{chain.length > 1 ? ` · ${chain.length}` : ''}
                            </Badge>
                          </UnstyledButton>
                        )}
                      </Group>
                      {t.title && t.title !== t.text && <Text size="xs" c="dimmed" mt={2}>{t.text}</Text>}
                      {t.supersedes && (
                        <Collapse in={expanded}>
                          <div style={{ marginTop: 8, paddingLeft: 12, borderLeft: '2px solid var(--db-border-default)' }}>
                            <Text size="xs" c="dimmed" mb={6}>Grew out of {chain.length > 1 ? 'these resolved threads' : 'this resolved thread'}:</Text>
                            {chain.length === 0 ? (
                              <Text size="xs" c="dimmed">Parent thread is no longer available.</Text>
                            ) : chain.map((a) => (
                              <div key={a.id} style={{ marginBottom: 6 }}>
                                <Group gap="xs" wrap="nowrap" align="center">
                                  <Badge size="xs" variant="light" color={a.status === 'dropped' ? 'gray' : 'teal'} style={{ flexShrink: 0 }}>{a.status}</Badge>
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
                      <Badge size="sm" variant="light">{p.action.replace('_', ' ')}</Badge>
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
        {ledger.coverage.summary && (
          <Section
            icon={<IconMap2 size={16} />}
            title="Coverage"
            description="What the investigation has reached across the warehouse so far."
          >
            <Text size="sm">{ledger.coverage.summary}</Text>
          </Section>
        )}

        {/* 6. Findings — advanced finding-level detail, collapsed by default */}
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
            The full finding-level table behind the ledger — severity, status, the metric and how often each has recurred.
          </Text>
          <Collapse in={showFindings}>
            <div style={{ marginTop: 16 }}>
              {ledger.findings.length === 0 ? (
                <EmptyState icon={<IconBook2 size={32} />} title="No findings yet" description="Findings accumulate here as discovery runs complete." />
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                  <thead>
                    <tr>
                      <Th width="30%">Finding</Th>
                      <Th width="14%">Area</Th>
                      <Th width="9%">Severity</Th>
                      <Th width="9%">Status</Th>
                      <Th width="30%">Metric</Th>
                      <Th width="8%">Seen</Th>
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
                          <Badge size="sm" variant="light" color={FINDING_STATUS_COLOR[f.status] || 'gray'}>{f.status}</Badge>
                        </td>
                        <td style={{ padding: '10px 12px' }}><Text size="xs" c="dimmed" lineClamp={1}>{f.key_metric || '—'}</Text></td>
                        <td style={{ padding: '10px 12px' }}><Text size="sm">{f.seen_count}×</Text></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </Collapse>
        </Card>

        {/* 7. History — applied / rejected playbook changes */}
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
                    <Badge size="sm" variant="light" color={PROPOSAL_STATUS_COLOR[p.status] || 'gray'}>{p.status}</Badge>
                    <Badge size="sm" variant="outline">{p.action.replace('_', ' ')}</Badge>
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
                description="Biases the next run's direction"
                data={FRONTIER_POLICIES}
                value={settings.frontier_policy}
                disabled={savingMode}
                onChange={(v) => v && saveSettings({ frontier_policy: v as FrontierPolicy })}
                w={280}
              />
            </Group>
          </Section>
        )}
      </Stack>
    </Shell>
  );
}
