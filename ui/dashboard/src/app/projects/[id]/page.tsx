'use client';

import { useEffect, useState, useCallback } from 'react';
import { useParams } from 'next/navigation';
import {
  Badge, Button, Card, Checkbox, Code, Grid, Group, Loader, Menu, Progress, ScrollArea, Stack, Tabs, Text, Timeline, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconAlertTriangle, IconBulb, IconChartBar, IconCheck, IconChevronDown, IconDatabase,
  IconEdit, IconPlayerPlay, IconSearch, IconSettings, IconTrendingUp, IconX,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { api, DiscoveryResult, DiscoveryRunStatus, Insight, Project, Recommendation, RunStep } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

export default function ProjectPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [run, setRun] = useState<DiscoveryRunStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [triggering, setTriggering] = useState(false);
  const [analysisAreas, setAnalysisAreas] = useState<{ id: string; name: string }[]>([]);
  const [selectedAreas, setSelectedAreas] = useState<string[]>([]);

  // Load project, latest discovery, and analysis areas
  useEffect(() => {
    Promise.all([
      api.getProject(id).then(setProject),
      api.getLatestDiscovery(id).then(setDiscovery).catch(() => null),
      api.getProject(id).then((p) =>
        api.getAnalysisAreas(p.domain, p.category)
          .then((areas) => setAnalysisAreas(areas.map((a) => ({ id: a.id, name: a.name }))))
      ).catch(() => []),
    ])
      .catch((e) => notifications.show({ title: 'Error', message: e.message, color: 'red' }))
      .finally(() => setLoading(false));
  }, [id]);

  // Poll for live run status
  const pollStatus = useCallback(async () => {
    try {
      const status = await api.getProjectStatus(id);
      if (status?.run) {
        setRun(status.run as unknown as DiscoveryRunStatus);
      }
    } catch { /* ignore polling errors */ }
  }, [id]);

  useEffect(() => {
    if (!run || (run.status !== 'running' && run.status !== 'pending')) return;
    const interval = setInterval(pollStatus, 2000); // poll every 2s
    return () => clearInterval(interval);
  }, [run, pollStatus]);

  // Also poll once on mount to pick up any running discovery
  useEffect(() => { pollStatus(); }, [pollStatus]);

  const handleTrigger = async (areas?: string[]) => {
    setTriggering(true);
    try {
      const result = await api.triggerDiscovery(id, areas);
      if (result.run_id) {
        const newRun = await api.getRun(result.run_id);
        setRun(newRun);
      }
      const msg = areas && areas.length > 0
        ? `Running: ${areas.join(', ')}`
        : 'Full discovery started';
      notifications.show({ title: 'Discovery started', message: msg, color: 'blue' });
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

  // Group insights by analysis area
  const insightsByArea: Record<string, Insight[]> = {};
  discovery?.insights?.forEach((insight) => {
    if (!insightsByArea[insight.analysis_area]) insightsByArea[insight.analysis_area] = [];
    insightsByArea[insight.analysis_area].push(insight);
  });
  const areas = Object.keys(insightsByArea);

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
            </Group>
          </div>
          <Group>
            <Button variant="light" component={Link} href={`/projects/${id}/prompts`}
              leftSection={<IconEdit size={16} />}>Prompts</Button>
            <Button variant="light" component={Link} href={`/projects/${id}/settings`}
              leftSection={<IconSettings size={16} />}>Settings</Button>

            {/* Run Discovery — full or selective */}
            <Menu shadow="md" width={250} disabled={!!isRunning}>
              <Menu.Target>
                <Button leftSection={<IconPlayerPlay size={16} />}
                  rightSection={<IconChevronDown size={14} />}
                  loading={triggering} disabled={!!isRunning}>
                  {isRunning ? 'Running...' : 'Run Discovery'}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => handleTrigger()}>
                  Run All Areas
                </Menu.Item>
                <Menu.Divider />
                <Menu.Label>Run specific areas</Menu.Label>
                {analysisAreas.map((area) => (
                  <Menu.Item key={area.id}>
                    <Checkbox
                      label={area.name}
                      checked={selectedAreas.includes(area.id)}
                      onChange={(e) => {
                        if (e.currentTarget.checked) {
                          setSelectedAreas([...selectedAreas, area.id]);
                        } else {
                          setSelectedAreas(selectedAreas.filter((a) => a !== area.id));
                        }
                      }}
                    />
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
        {run && (run.status === 'running' || run.status === 'pending') && (
          <LiveRunStatus run={run} onCancel={async () => {
            try {
              await api.cancelRun(run.id);
              setRun({ ...run, status: 'cancelled' });
              notifications.show({ title: 'Cancelled', message: 'Discovery run cancelled', color: 'orange' });
            } catch (e: unknown) {
              notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
            }
          }} />
        )}

        {/* Completed Run Summary */}
        {run && run.status === 'completed' && !discovery && (
          <Card withBorder p="md" bg="green.0">
            <Group>
              <IconCheck size={20} color="green" />
              <Text fw={600}>Discovery completed — {run.insights_found} insights found</Text>
            </Group>
          </Card>
        )}

        {run && run.status === 'failed' && (
          <Card withBorder p="md" bg="red.0">
            <Group>
              <IconX size={20} color="red" />
              <Text fw={600}>Discovery failed: {run.error}</Text>
            </Group>
          </Card>
        )}

        {/* No discovery yet */}
        {!discovery && !isRunning && (
          <Card withBorder p="xl" ta="center">
            <Stack align="center" gap="md">
              <IconChartBar size={48} color="var(--mantine-color-gray-5)" />
              <Title order={3} c="dimmed">No discoveries yet</Title>
              <Text c="dimmed">Run your first discovery to see insights.</Text>
            </Stack>
          </Card>
        )}

        {/* KPI Cards */}
        {discovery && (
          <>
            <Grid>
              <Grid.Col span={{ base: 6, md: 3 }}>
                <Card withBorder p="md" ta="center">
                  <Text size="xl" fw={700} c="blue">{discovery.summary?.total_insights || 0}</Text>
                  <Text size="sm" c="dimmed">Insights</Text>
                </Card>
              </Grid.Col>
              <Grid.Col span={{ base: 6, md: 3 }}>
                <Card withBorder p="md" ta="center">
                  <Text size="xl" fw={700} c="violet">{discovery.summary?.total_recommendations || 0}</Text>
                  <Text size="sm" c="dimmed">Recommendations</Text>
                </Card>
              </Grid.Col>
              <Grid.Col span={{ base: 6, md: 3 }}>
                <Card withBorder p="md" ta="center">
                  <Text size="xl" fw={700} c="red">
                    {discovery.insights?.filter((i) => i.severity === 'critical').length || 0}
                  </Text>
                  <Text size="sm" c="dimmed">Critical</Text>
                </Card>
              </Grid.Col>
              <Grid.Col span={{ base: 6, md: 3 }}>
                <Card withBorder p="md" ta="center">
                  <Text size="xl" fw={700} c="green">{discovery.summary?.queries_executed || 0}</Text>
                  <Text size="sm" c="dimmed">Queries Run</Text>
                </Card>
              </Grid.Col>
            </Grid>

            {/* Insights by Area */}
            {areas.length > 0 && (
              <Tabs defaultValue={areas[0]}>
                <Tabs.List>
                  {areas.map((area) => (
                    <Tabs.Tab key={area} value={area}>
                      {area.charAt(0).toUpperCase() + area.slice(1)} ({insightsByArea[area].length})
                    </Tabs.Tab>
                  ))}
                </Tabs.List>
                {areas.map((area) => (
                  <Tabs.Panel key={area} value={area} pt="md">
                    <Stack gap="md">
                      {insightsByArea[area].sort((a, b) => b.risk_score - a.risk_score).map((insight, idx) => (
                        <InsightCard key={idx} insight={insight} />
                      ))}
                    </Stack>
                  </Tabs.Panel>
                ))}
              </Tabs>
            )}

            {/* Recommendations */}
            {discovery.recommendations && discovery.recommendations.length > 0 && (
              <Card withBorder p="lg">
                <Title order={3} mb="md">
                  <IconBulb size={20} style={{ verticalAlign: 'middle', marginRight: 8 }} />
                  Recommendations
                </Title>
                <Stack gap="md">
                  {discovery.recommendations.sort((a, b) => b.priority - a.priority).map((rec, idx) => (
                    <RecommendationCard key={idx} rec={rec} />
                  ))}
                </Stack>
              </Card>
            )}
          </>
        )}
      </Stack>
    </Shell>
  );
}

// --- Live Run Status ---

function LiveRunStatus({ run, onCancel }: { run: DiscoveryRunStatus; onCancel: () => void }) {
  const phaseIcon: Record<string, React.ReactNode> = {
    init: <IconDatabase size={14} />,
    schema_discovery: <IconDatabase size={14} />,
    exploration: <IconSearch size={14} />,
    analysis: <IconChartBar size={14} />,
    recommendations: <IconBulb size={14} />,
    saving: <IconCheck size={14} />,
  };

  return (
    <Card withBorder p="lg" shadow="sm">
      <Group justify="space-between" mb="sm">
        <Group>
          <Loader size="sm" />
          <Title order={4}>Discovery Running</Title>
        </Group>
        <Group>
          <Badge color="blue" variant="light">{run.phase}</Badge>
          <Button size="xs" variant="light" color="red" onClick={onCancel}>
            Cancel
          </Button>
        </Group>
      </Group>

      <Progress value={run.progress} mb="sm" animated />

      <Text size="sm" c="dimmed" mb="md">{run.phase_detail}</Text>

      <Group gap="xl" mb="md">
        <Text size="xs" c="dimmed">Queries: {run.total_queries}</Text>
        <Text size="xs" c="dimmed">Insights: {run.insights_found}</Text>
      </Group>

      {/* Live step log */}
      {run.steps && run.steps.length > 0 && (
        <ScrollArea h={300} type="auto">
          <Timeline active={run.steps.length - 1} bulletSize={20} lineWidth={2}>
            {run.steps.slice(-20).map((step, idx) => (
              <Timeline.Item
                key={idx}
                bullet={step.type === 'insight' ? <IconBulb size={12} /> :
                        step.type === 'error' ? <IconX size={12} /> :
                        step.type === 'query' ? <IconDatabase size={12} /> :
                        phaseIcon[step.phase] || undefined}
                color={step.type === 'error' ? 'red' : step.type === 'insight' ? 'green' : 'blue'}
                title={step.message}
              >
                {step.llm_thinking && (
                  <Text size="xs" c="dimmed" mt={2}>Thinking: {step.llm_thinking}</Text>
                )}
                {step.query && (
                  <Code block mt={4} style={{ fontSize: '11px', maxHeight: 80, overflow: 'auto' }}>
                    {step.query}
                  </Code>
                )}
                {step.query_result && (
                  <Text size="xs" c="dimmed" mt={2}>{step.query_result}</Text>
                )}
                {step.error && (
                  <Text size="xs" c="red" mt={2}>{step.error}</Text>
                )}
              </Timeline.Item>
            ))}
          </Timeline>
        </ScrollArea>
      )}
    </Card>
  );
}

// --- Insight Card ---

function InsightCard({ insight }: { insight: Insight }) {
  return (
    <Card withBorder p="md" radius="md">
      <Group justify="space-between" mb="xs">
        <Group gap="xs">
          <IconAlertTriangle size={16} color={`var(--mantine-color-${severityColor[insight.severity] || 'gray'}-6)`} />
          <Text fw={600}>{insight.name}</Text>
        </Group>
        <Group gap="xs">
          <Badge color={severityColor[insight.severity] || 'gray'} variant="light" size="sm">{insight.severity}</Badge>
          {insight.affected_count > 0 && (
            <Badge variant="outline" size="sm">{insight.affected_count.toLocaleString()} affected</Badge>
          )}
        </Group>
      </Group>
      <Text size="sm" c="dimmed">{insight.description}</Text>
      {insight.indicators && insight.indicators.length > 0 && (
        <Stack gap={4} mt="sm">
          {insight.indicators.slice(0, 4).map((ind, i) => (
            <Text key={i} size="xs" c="dimmed">- {ind}</Text>
          ))}
        </Stack>
      )}
      {insight.validation && (
        <Badge mt="sm" size="xs" variant="outline"
          color={insight.validation.status === 'confirmed' ? 'green' : insight.validation.status === 'adjusted' ? 'yellow' : 'red'}>
          {insight.validation.status}
        </Badge>
      )}
    </Card>
  );
}

// --- Recommendation Card ---

function RecommendationCard({ rec }: { rec: Recommendation }) {
  const priorityColor = rec.priority >= 5 ? 'red' : rec.priority >= 4 ? 'orange' : 'blue';
  return (
    <Card withBorder p="md" radius="md" style={{ borderLeft: `4px solid var(--mantine-color-${priorityColor}-6)` }}>
      <Group justify="space-between" mb="xs">
        <Text fw={600}>{rec.title}</Text>
        <Badge color={priorityColor} variant="light" size="sm">P{rec.priority}</Badge>
      </Group>
      <Text size="sm" c="dimmed" mb="sm">{rec.description}</Text>
      {rec.expected_impact && (
        <Group gap="xs" mb="sm">
          <IconTrendingUp size={14} />
          <Text size="xs" c="dimmed">{rec.expected_impact.metric}: {rec.expected_impact.estimated_improvement}</Text>
        </Group>
      )}
      {rec.actions && rec.actions.length > 0 && (
        <Stack gap={4}>
          {rec.actions.slice(0, 3).map((action, i) => (
            <Text key={i} size="xs" c="dimmed">- {action}</Text>
          ))}
        </Stack>
      )}
    </Card>
  );
}
