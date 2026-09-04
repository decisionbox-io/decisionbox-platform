'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { Loader, Select, Button, Group, Stack, Text, Card, Badge, Progress, Tooltip, Collapse, UnstyledButton } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconBook2, IconChevronRight } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { SectionHeader, EmptyState, StatCard, SeverityBadge, AreaBadge, Th } from '@/components/common/UIComponents';
import {
  api, ApiError, LedgerView, EvolutionSettings, PackProposal, Project,
  EvolutionMode, FrontierPolicy,
} from '@/lib/api';

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
  };
}

// LedgerPage is the investigation view for compounding discovery. Layout is
// ordered by what an analyst acts on first: the convergence trend and the open
// investigation threads sit up top (is discovery still finding new things, and
// what should the next run chase), then the actionable domain-pack proposals and
// the coverage summary. The raw finding-level table is advanced detail, so it is
// collapsed by default (technical users expand it). The evolution / frontier
// controls are configuration, so they live at the bottom. Rendered full-width —
// the finding table + convergence bars want the horizontal room.
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

  return (
    <Shell fullWidth>
      <Stack gap="lg">
        <SectionHeader title="Discovery Ledger" />
        <Text size="sm" c="dimmed">
          {project?.name ? `${project.name} — ` : ''}the accumulated investigation state. Each run builds on this instead of starting fresh.
        </Text>

        {/* At-a-glance KPIs */}
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

        {/* Convergence trend — is discovery still finding new things? (top) */}
        {ledger.convergence.length > 0 && (
          <Card withBorder padding="md" radius="md">
            <Text size="sm" fw={600} mb="xs">Convergence — new findings per run</Text>
            <Stack gap={6}>
              {ledger.convergence.slice(-12).map((c) => (
                <Group key={c.run_id} gap="sm" wrap="nowrap">
                  <Text size="xs" c="dimmed" w={90} style={{ flexShrink: 0 }}>{new Date(c.date).toLocaleDateString()}</Text>
                  <Progress value={Math.round(c.marginal_ratio * 100)} size="sm" style={{ flex: 1 }} />
                  <Text size="xs" c="dimmed" w={110} ta="right" style={{ flexShrink: 0 }}>{c.new_findings} new / {c.total_findings}</Text>
                </Group>
              ))}
            </Stack>
          </Card>
        )}

        {/* Open investigation threads — what the next run should chase (top) */}
        {ledger.tasks.length > 0 && (
          <div>
            <SectionHeader title="Open investigation threads" count={ledger.tasks.length} />
            <Stack gap="xs" mt="xs">
              {ledger.tasks.map((t) => (
                <Group key={t.id} gap="xs" wrap="nowrap" align="flex-start">
                  <Badge size="xs" variant="light" color={t.kind === 'hypothesis' ? 'grape' : 'blue'}>{t.kind.replace('_', ' ')}</Badge>
                  <Text size="sm">{t.text}</Text>
                </Group>
              ))}
            </Stack>
          </div>
        )}

        {/* Pending domain-pack proposals — needs a decision */}
        {pending.length > 0 && (
          <Card withBorder padding="md" radius="md">
            <Text size="sm" fw={600} mb="xs">Proposed domain-pack changes ({pending.length})</Text>
            <Stack gap="sm">
              {pending.map((p) => (
                <Group key={p.id} justify="space-between" wrap="nowrap" align="flex-start">
                  <div style={{ minWidth: 0 }}>
                    <Group gap="xs" mb={2}>
                      <Badge size="sm" variant="light">{p.action.replace('_', ' ')}</Badge>
                      <Text size="sm" fw={600}>{p.area_name || p.area_id}</Text>
                    </Group>
                    <Text size="sm" c="dimmed">{p.rationale}</Text>
                  </div>
                  <Group gap="xs" wrap="nowrap">
                    <Button size="xs" color="teal" loading={deciding === p.id} onClick={() => decide(p.id, 'approve')}>Approve</Button>
                    <Button size="xs" variant="default" loading={deciding === p.id} onClick={() => decide(p.id, 'reject')}>Reject</Button>
                  </Group>
                </Group>
              ))}
            </Stack>
          </Card>
        )}

        {/* Coverage summary */}
        {ledger.coverage.summary && (
          <Card withBorder padding="md" radius="md">
            <Text size="sm" fw={600} mb={4}>Coverage</Text>
            <Text size="sm" c="dimmed">{ledger.coverage.summary}</Text>
          </Card>
        )}

        {/* Findings — advanced finding-level detail, collapsed by default */}
        <div>
          <UnstyledButton onClick={() => setShowFindings((v) => !v)} style={{ width: '100%' }}>
            <Group justify="space-between" wrap="nowrap">
              <Group gap="xs" wrap="nowrap">
                <IconChevronRight
                  size={18}
                  style={{ transform: showFindings ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }}
                />
                <Text fw={600} size="md">Findings</Text>
                <Badge size="sm" variant="light" color="gray">{ledger.findings.length}</Badge>
              </Group>
              <Text size="xs" c="dimmed">{showFindings ? 'Hide detail' : 'Show detail'}</Text>
            </Group>
          </UnstyledButton>
          <Text size="xs" c="dimmed" mt={4} ml={26}>
            The full finding-level table behind the ledger — severity, status, the metric and how often it has recurred.
          </Text>
          <Collapse in={showFindings}>
            <div style={{ marginTop: 12 }}>
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
        </div>

        {/* Decided-proposal history */}
        {decided.length > 0 && (
          <div>
            <SectionHeader title="Change history" count={decided.length} />
            <Stack gap="xs" mt="xs">
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
          </div>
        )}

        {/* Evolution controls — configuration, at the bottom */}
        {settings && (
          <Card withBorder padding="md" radius="md">
            <Text size="sm" fw={600} mb="xs">Evolution</Text>
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
          </Card>
        )}
      </Stack>
    </Shell>
  );
}
