'use client';

import { useEffect, useState, useCallback, useRef } from 'react';
import { useParams } from 'next/navigation';
import {
  Badge, Button, Card, Checkbox, Code, Collapse, Grid, Group, Loader, Menu, NumberInput,
  Progress, ScrollArea, Stack, Text, Title,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { notifications } from '@mantine/notifications';
import {
  IconAlertTriangle, IconBrain, IconBulb, IconCheck, IconChevronDown, IconChevronRight,
  IconDatabase, IconEdit, IconPlayerPlay, IconSearch, IconSettings,
  IconShieldCheck, IconX,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { api, DiscoveryResult, DiscoveryRunStatus, Project, RunStep } from '@/lib/api';

export default function ProjectPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [discoveries, setDiscoveries] = useState<DiscoveryResult[]>([]);
  const [run, setRun] = useState<DiscoveryRunStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [triggering, setTriggering] = useState(false);
  const [analysisAreas, setAnalysisAreas] = useState<{ id: string; name: string }[]>([]);
  const [selectedAreas, setSelectedAreas] = useState<string[]>([]);
  const [maxSteps, setMaxSteps] = useState(100);
  const dismissedRunId = useRef<string | null>(null);

  useEffect(() => {
    Promise.all([
      api.getProject(id).then((p) => {
        setProject(p);
        return api.getAnalysisAreas(p.domain, p.category)
          .then((areas) => setAnalysisAreas(areas.map((a) => ({ id: a.id, name: a.name }))));
      }),
      api.listDiscoveries(id).then((d) => setDiscoveries(d || [])).catch(() => setDiscoveries([])),
    ])
      .catch((e) => notifications.show({ title: 'Error', message: e.message, color: 'red' }))
      .finally(() => setLoading(false));
  }, [id]);

  const pollStatus = useCallback(async () => {
    try {
      const status = await api.getProjectStatus(id);
      if (status?.run) {
        const newRun = status.run as unknown as DiscoveryRunStatus;
        // Don't bring back a dismissed run
        if (dismissedRunId.current === newRun.id) return;
        const wasRunning = run && (run.status === 'running' || run.status === 'pending');
        const nowDone = newRun.status === 'completed' || newRun.status === 'failed';
        setRun(newRun);
        // Refresh discoveries list when run finishes
        if (wasRunning && nowDone) {
          api.listDiscoveries(id).then((d) => setDiscoveries(d || [])).catch(() => {});
        }
      }
    } catch { /* ignore */ }
  }, [id, run]);

  useEffect(() => {
    if (!run) return;
    if (run.status !== 'running' && run.status !== 'pending') return;
    const interval = setInterval(pollStatus, 2000);
    return () => clearInterval(interval);
  }, [run, pollStatus]);

  // Initial poll on mount
  useEffect(() => { pollStatus(); }, []);

  const handleTrigger = async (areas?: string[]) => {
    setTriggering(true);
    try {
      const opts: { areas?: string[]; max_steps?: number } = {};
      if (areas && areas.length > 0) opts.areas = areas;
      if (maxSteps !== 100) opts.max_steps = maxSteps;

      const result = await api.triggerDiscovery(id, Object.keys(opts).length > 0 ? opts : undefined);
      if (result.run_id) {
        const newRun = await api.getRun(result.run_id);
        setRun(newRun);
      }
      notifications.show({ title: 'Discovery started', message: `${maxSteps} steps`, color: 'blue' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setTriggering(false);
      setSelectedAreas([]);
    }
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  const isRunning = run && (run.status === 'running' || run.status === 'pending');
  const justFinished = run && (run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled');
  const showRunCard = isRunning || justFinished;
  const latestDiscovery = discoveries.length > 0 ? discoveries[0] : null;

  return (
    <Shell>
      <Stack gap="lg">
        {/* Header */}
        <Group justify="space-between">
          <div>
            <Title order={2}>{project.name}</Title>
            <Group gap="xs" mt={4}>
              <Badge variant="light">{project.domain}</Badge>
              <Badge variant="light" color="blue">{project.category}</Badge>
              {project.description && <Text size="xs" c="dimmed">{project.description}</Text>}
            </Group>
          </div>
          <Group>
            <Button variant="subtle" component={Link} href={`/projects/${id}/prompts`}
              leftSection={<IconEdit size={16} />} size="sm">Prompts</Button>
            <Button variant="subtle" component={Link} href={`/projects/${id}/settings`}
              leftSection={<IconSettings size={16} />} size="sm">Settings</Button>

            <Menu shadow="md" width={280} disabled={!!isRunning}>
              <Menu.Target>
                <Button leftSection={<IconPlayerPlay size={16} />}
                  rightSection={<IconChevronDown size={14} />}
                  loading={triggering} disabled={!!isRunning}>
                  {isRunning ? 'Running...' : 'Run Discovery'}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>Exploration steps</Menu.Label>
                <div style={{ padding: '4px 12px 8px' }}>
                  <NumberInput size="xs" value={maxSteps} onChange={(v) => setMaxSteps(Number(v) || 100)}
                    min={5} max={500} step={5} description="More steps = more comprehensive" />
                </div>
                <Menu.Divider />
                <Menu.Item onClick={() => handleTrigger()}>Run All Areas</Menu.Item>
                <Menu.Divider />
                <Menu.Label>Select areas</Menu.Label>
                {analysisAreas.map((area) => (
                  <Menu.Item key={area.id} closeMenuOnClick={false}>
                    <Checkbox label={area.name} checked={selectedAreas.includes(area.id)}
                      onChange={(e) => {
                        if (e.currentTarget.checked) setSelectedAreas([...selectedAreas, area.id]);
                        else setSelectedAreas(selectedAreas.filter((a) => a !== area.id));
                      }} />
                  </Menu.Item>
                ))}
                {selectedAreas.length > 0 && (
                  <>
                    <Menu.Divider />
                    <Menu.Item color="blue" onClick={() => handleTrigger(selectedAreas)}>
                      Run Selected ({selectedAreas.length})
                    </Menu.Item>
                  </>
                )}
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>

        {/* Live Run Status */}
        {showRunCard && run && (
          <LiveRunStatus run={run} onCancel={async () => {
            if (justFinished) {
              dismissedRunId.current = run.id;
              setRun(null);
              return;
            }
            try {
              await api.cancelRun(run.id);
              setRun({ ...run, status: 'cancelled' });
              notifications.show({ title: 'Cancelled', message: 'Discovery cancelled', color: 'orange' });
            } catch (e: unknown) {
              notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
            }
          }} />
        )}

        {/* Quick Stats */}
        {latestDiscovery && (
          <Grid>
            <Grid.Col span={{ base: 6, md: 3 }}>
              <Card withBorder p="md" ta="center">
                <Text size="xl" fw={700} c="blue">{discoveries.length}</Text>
                <Text size="sm" c="dimmed">Total Runs</Text>
              </Card>
            </Grid.Col>
            <Grid.Col span={{ base: 6, md: 3 }}>
              <Card withBorder p="md" ta="center">
                <Text size="xl" fw={700} c="violet">
                  {discoveries.reduce((sum, d) => sum + (d.summary?.total_insights || 0), 0)}
                </Text>
                <Text size="sm" c="dimmed">Total Insights</Text>
              </Card>
            </Grid.Col>
            <Grid.Col span={{ base: 6, md: 3 }}>
              <Card withBorder p="md" ta="center">
                <Text size="xl" fw={700} c="green">{latestDiscovery.summary?.total_insights || 0}</Text>
                <Text size="sm" c="dimmed">Latest Insights</Text>
              </Card>
            </Grid.Col>
            <Grid.Col span={{ base: 6, md: 3 }}>
              <Card withBorder p="md" ta="center">
                <Text size="xl" fw={700}>{latestDiscovery.total_steps}</Text>
                <Text size="sm" c="dimmed">Latest Steps</Text>
              </Card>
            </Grid.Col>
          </Grid>
        )}

        {/* Empty State */}
        {!latestDiscovery && !isRunning && (
          <Card withBorder p="xl" ta="center">
            <Stack align="center" gap="md">
              <IconSearch size={48} color="var(--mantine-color-gray-4)" />
              <Title order={3} c="dimmed">No discoveries yet</Title>
              <Text c="dimmed">Run your first discovery to start finding insights.</Text>
            </Stack>
          </Card>
        )}

        {/* Discovery History */}
        {discoveries.length > 0 && (
          <>
            <Title order={3}>Discoveries</Title>
            <Stack gap="sm">
              {discoveries.map((d) => (
                <Card key={d.id} withBorder p="md" radius="md" component={Link}
                  href={`/projects/${id}/discoveries/${d.id}`}
                  style={{ textDecoration: 'none', cursor: 'pointer' }}>
                  <Group justify="space-between">
                    <Group gap="sm">
                      <Text size="sm" fw={600}>
                        {new Date(d.discovery_date).toLocaleDateString('en-US', {
                          month: 'short', day: 'numeric', year: 'numeric',
                          hour: '2-digit', minute: '2-digit',
                        })}
                      </Text>
                      <Badge size="sm" variant="light"
                        color={d.run_type === 'partial' ? 'violet' : 'blue'}>
                        {d.run_type || 'full'}
                      </Badge>
                      {d.areas_requested && d.areas_requested.length > 0 && (
                        <Text size="xs" c="dimmed">{d.areas_requested.join(', ')}</Text>
                      )}
                    </Group>
                    <Group gap="sm">
                      <Badge size="sm" variant="outline" color="teal">
                        {d.summary?.total_insights || 0} insights
                      </Badge>
                      <Badge size="sm" variant="outline" color="gray">
                        {d.total_steps} steps
                      </Badge>
                    </Group>
                  </Group>
                </Card>
              ))}
            </Stack>
          </>
        )}
      </Stack>
    </Shell>
  );
}

function LiveRunStatus({ run, onCancel }: { run: DiscoveryRunStatus; onCancel: () => void }) {
  const steps = run.steps || [];
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const userScrolledUp = useRef(false);
  const prevStepCount = useRef(0);

  // Auto-scroll only when new steps arrive and user hasn't scrolled up
  useEffect(() => {
    if (steps.length > prevStepCount.current && !userScrolledUp.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
    prevStepCount.current = steps.length;
  }, [steps.length]);
  const phaseLabel: Record<string, string> = {
    init: 'Initializing', schema_discovery: 'Discovering Schema',
    exploration: 'Exploring Data', analysis: 'Analyzing Patterns',
    validation: 'Validating Insights', recommendations: 'Generating Recommendations',
    saving: 'Saving Results', complete: 'Complete',
  };

  const isDone = run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled';
  const statusColor = run.status === 'completed' ? 'green' : run.status === 'failed' ? 'red' : run.status === 'cancelled' ? 'orange' : 'blue';

  return (
    <Card withBorder p="lg" shadow="sm" radius="md">
      {/* Header */}
      <Group justify="space-between" mb="md">
        <Group gap="sm">
          {isDone
            ? (run.status === 'completed'
              ? <IconCheck size={20} color="var(--mantine-color-green-6)" />
              : <IconAlertTriangle size={20} color={`var(--mantine-color-${statusColor}-6)`} />)
            : <Loader size="sm" color="blue" />}
          <Title order={4}>
            {isDone
              ? (run.status === 'completed' ? 'Discovery Complete' : run.status === 'failed' ? 'Discovery Failed' : 'Discovery Cancelled')
              : 'Discovery in Progress'}
          </Title>
        </Group>
        <Group gap="xs">
          <Badge color={statusColor} variant="light" size="lg">{phaseLabel[run.phase] || run.phase}</Badge>
          {!isDone && <Button size="xs" variant="light" color="red" onClick={onCancel}>Cancel</Button>}
          {isDone && <Button size="xs" variant="subtle" color="gray" onClick={onCancel}>Dismiss</Button>}
        </Group>
      </Group>

      {/* Progress bar */}
      <Progress value={run.progress} mb={4} animated={!isDone} size="md" radius="xl" color={statusColor} />
      <Group justify="space-between" mb="md">
        {run.error && <Text size="xs" c="red">{run.error}</Text>}
        <Text size="xs" c="dimmed" ml="auto">{run.progress}%</Text>
      </Group>

      {/* Stats row */}
      <Group gap="lg" mb="md">
        <Group gap={4}>
          <IconDatabase size={14} color="var(--mantine-color-blue-5)" />
          <Text size="sm" fw={600}>{run.total_queries}</Text>
          <Text size="xs" c="dimmed">queries</Text>
        </Group>
        {run.successful_queries > 0 && (
          <Group gap={4}>
            <IconCheck size={14} color="var(--mantine-color-green-5)" />
            <Text size="sm" fw={600}>{run.successful_queries}</Text>
            <Text size="xs" c="dimmed">successful</Text>
          </Group>
        )}
        {run.failed_queries > 0 && (
          <Group gap={4}>
            <IconX size={14} color="var(--mantine-color-red-5)" />
            <Text size="sm" fw={600}>{run.failed_queries}</Text>
            <Text size="xs" c="dimmed">failed</Text>
          </Group>
        )}
        {run.insights_found > 0 && (
          <Group gap={4}>
            <IconBulb size={14} color="var(--mantine-color-yellow-5)" />
            <Text size="sm" fw={600}>{run.insights_found}</Text>
            <Text size="xs" c="dimmed">insights</Text>
          </Group>
        )}
      </Group>

      {/* Live step feed */}
      {steps.length > 0 && (
        <ScrollArea h={400} type="auto" viewportRef={(el) => {
          scrollRef.current = el;
        }} onScrollPositionChange={({ y }) => {
          const el = scrollRef.current;
          if (!el) return;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          userScrolledUp.current = !atBottom;
        }}>
          <Stack gap={6}>
            {steps.map((step, idx) => (
              <StepCard key={idx} step={step} />
            ))}
          </Stack>
        </ScrollArea>
      )}

      {steps.length === 0 && (
        <Text size="sm" c="dimmed" ta="center" py="xl">{run.phase_detail || 'Starting...'}</Text>
      )}
    </Card>
  );
}

function StepCard({ step }: { step: RunStep }) {
  const [opened, { toggle }] = useDisclosure(false);
  const hasDetails = step.query || (step.llm_thinking && step.llm_thinking.length > 80);

  if (step.type === 'insight') {
    return (
      <Card withBorder p="xs" radius="sm" bg="var(--mantine-color-green-0)">
        <Group gap="xs">
          <IconBulb size={16} color="var(--mantine-color-yellow-6)" />
          <Text size="sm" fw={600}>{step.insight_name || step.message}</Text>
          {step.insight_severity && (
            <Badge size="xs" color={
              step.insight_severity === 'critical' ? 'red' :
              step.insight_severity === 'high' ? 'orange' :
              step.insight_severity === 'medium' ? 'yellow' : 'gray'
            }>{step.insight_severity}</Badge>
          )}
        </Group>
      </Card>
    );
  }

  if (step.type === 'analysis') {
    return (
      <Card withBorder p="xs" radius="sm" bg="var(--mantine-color-violet-0)">
        <Group gap="xs">
          <IconBrain size={16} color="var(--mantine-color-violet-6)" />
          <Text size="sm" fw={600}>{step.message}</Text>
        </Group>
      </Card>
    );
  }

  if (step.type === 'validation') {
    return (
      <Card withBorder p="xs" radius="sm" bg="var(--mantine-color-teal-0)">
        <Group gap="xs">
          <IconShieldCheck size={16} color="var(--mantine-color-teal-6)" />
          <Text size="sm">{step.message}</Text>
        </Group>
      </Card>
    );
  }

  if (step.type === 'error') {
    return (
      <Card withBorder p="xs" radius="sm" bg="var(--mantine-color-red-0)">
        <Group gap="xs">
          <IconAlertTriangle size={16} color="var(--mantine-color-red-6)" />
          <Text size="sm" c="red">{step.error || step.message}</Text>
        </Group>
      </Card>
    );
  }

  // Query step (exploration)
  const thinking = step.llm_thinking || '';
  const thinkingPreview = thinking.length > 120 ? thinking.slice(0, 120) + '...' : thinking;

  return (
    <Card withBorder p="xs" radius="sm"
      style={{ cursor: hasDetails ? 'pointer' : 'default' }}
      onClick={hasDetails ? toggle : undefined}>
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Group gap="xs" wrap="nowrap" style={{ flex: 1, minWidth: 0 }}>
          {hasDetails && (
            <IconChevronRight size={14} style={{
              transform: opened ? 'rotate(90deg)' : 'none',
              transition: 'transform 150ms',
              flexShrink: 0,
            }} />
          )}
          <IconDatabase size={14} color="var(--mantine-color-blue-5)" style={{ flexShrink: 0 }} />
          <Text size="xs" c="dimmed" lineClamp={1} style={{ flex: 1 }}>
            {step.step_num > 0 && <Text span fw={600} c="dark" size="xs">Step {step.step_num}: </Text>}
            {thinkingPreview || step.message}
          </Text>
        </Group>
        <Group gap={4} wrap="nowrap" style={{ flexShrink: 0 }}>
          {step.row_count > 0 && <Badge size="xs" variant="outline" color="blue">{step.row_count} rows</Badge>}
          {step.query_time_ms > 0 && <Badge size="xs" variant="outline" color="gray">{step.query_time_ms}ms</Badge>}
          {step.query_fixed && <Badge size="xs" variant="light" color="orange">fixed</Badge>}
          {step.error && <Badge size="xs" variant="light" color="red">error</Badge>}
        </Group>
      </Group>

      {hasDetails && (
        <Collapse in={opened}>
          <Stack gap={4} mt="xs">
            {thinking.length > 120 && (
              <Text size="xs" c="dimmed" style={{ whiteSpace: 'pre-wrap' }}>{thinking}</Text>
            )}
            {step.query && (
              <Code block style={{ fontSize: 11, maxHeight: 150, overflow: 'auto' }}>
                {step.query}
              </Code>
            )}
            {step.query_result && (
              <Text size="xs" c="dimmed">{step.query_result}</Text>
            )}
          </Stack>
        </Collapse>
      )}
    </Card>
  );
}
