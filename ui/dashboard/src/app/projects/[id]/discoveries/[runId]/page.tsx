'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import {
  Accordion, Badge, Button, Card, Chip, Code, Grid, Group, Loader, Select, Stack, Text, Title,
} from '@mantine/core';
import {
  IconAlertTriangle, IconArrowLeft, IconBulb, IconDatabase, IconSearch,
  IconSortDescending, IconTrendingUp,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import { api, DiscoveryResult, Feedback, Insight, Recommendation, ExplorationStep, AnalysisLogStep } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};
const severityOrder: Record<string, number> = {
  critical: 0, high: 1, medium: 2, low: 3,
};

export default function DiscoveryDetailPage() {
  const { id, runId } = useParams<{ id: string; runId: string }>();
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [feedbackMap, setFeedbackMap] = useState<Record<string, Feedback>>({});
  const [loading, setLoading] = useState(true);

  // Filters & sorting
  const [areaFilter, setAreaFilter] = useState<string[]>([]);
  const [severityFilter, setSeverityFilter] = useState<string[]>([]);
  const [sortBy, setSortBy] = useState<string>('severity');

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then(setDiscovery),
      api.listFeedback(runId).then((fb) => {
        const map: Record<string, Feedback> = {};
        (fb || []).forEach((f) => { map[`${f.target_type}:${f.target_id}`] = f; });
        setFeedbackMap(map);
      }).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [runId]);

  const handleFeedbackUpdate = (targetType: string, targetId: string, fb: Feedback | null) => {
    const key = `${targetType}:${targetId}`;
    setFeedbackMap((prev) => {
      const next = { ...prev };
      if (fb) next[key] = fb;
      else delete next[key];
      return next;
    });
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (!discovery) return <Shell><Text>Discovery not found</Text></Shell>;

  // Get unique areas and severities
  const allAreas = [...new Set((discovery.insights || []).map((i) => i.analysis_area))];
  const allSeverities = [...new Set((discovery.insights || []).map((i) => i.severity))];

  // Apply filters
  let filtered = discovery.insights || [];
  if (areaFilter.length > 0) {
    filtered = filtered.filter((i) => areaFilter.includes(i.analysis_area));
  }
  if (severityFilter.length > 0) {
    filtered = filtered.filter((i) => severityFilter.includes(i.severity));
  }

  // Sort insights
  const sorted = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'severity':
        const sevDiff = (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
        if (sevDiff !== 0) return sevDiff;
        return b.risk_score - a.risk_score;
      case 'risk':
        return b.risk_score - a.risk_score;
      case 'confidence':
        return b.confidence - a.confidence;
      case 'affected':
        return (b.affected_count || 0) - (a.affected_count || 0);
      default:
        return (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
    }
  });
  filtered = sorted;

  const durationSec = discovery.duration ? Math.round(discovery.duration / 1000000000) : 0;

  return (
    <Shell>
      <Stack gap="lg">
        {/* Header */}
        <Group>
          <Button variant="subtle" component={Link} href={`/projects/${id}`}
            leftSection={<IconArrowLeft size={16} />} size="sm">Back</Button>
        </Group>

        <Group justify="space-between">
          <div>
            <Title order={2}>
              {new Date(discovery.discovery_date).toLocaleDateString('en-US', {
                month: 'long', day: 'numeric', year: 'numeric',
              })}
            </Title>
            <Group gap="xs" mt={4}>
              <Badge variant="light" color={discovery.run_type === 'partial' ? 'violet' : 'blue'}>
                {discovery.run_type || 'full'}
              </Badge>
              {discovery.areas_requested && discovery.areas_requested.length > 0 && (
                <Text size="sm" c="dimmed">{discovery.areas_requested.join(', ')}</Text>
              )}
              <Text size="sm" c="dimmed">{discovery.total_steps} steps</Text>
              {durationSec > 0 && <Text size="sm" c="dimmed">{durationSec}s</Text>}
            </Group>
          </div>
        </Group>

        {/* KPI Row */}
        <Grid>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Card withBorder p="md" ta="center">
              <Text size="xl" fw={700} c="blue">{discovery.summary?.total_insights || 0}</Text>
              <Text size="sm" c="dimmed">Insights</Text>
            </Card>
          </Grid.Col>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Card withBorder p="md" ta="center">
              <Text size="xl" fw={700} c="red">
                {(discovery.insights || []).filter((i) => i.severity === 'critical').length}
              </Text>
              <Text size="sm" c="dimmed">Critical</Text>
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
              <Text size="xl" fw={700} c="green">{discovery.summary?.queries_executed || 0}</Text>
              <Text size="sm" c="dimmed">Queries</Text>
            </Card>
          </Grid.Col>
        </Grid>

        {/* Filters */}
        {(discovery.insights || []).length > 0 && (
          <Card withBorder p="md">
            <Group gap="lg">
              <div>
                <Text size="xs" fw={600} mb={4}>Area</Text>
                <Chip.Group multiple value={areaFilter} onChange={setAreaFilter}>
                  <Group gap={4}>
                    {allAreas.map((area) => (
                      <Chip key={area} value={area} size="xs" variant="outline">
                        {area}
                      </Chip>
                    ))}
                  </Group>
                </Chip.Group>
              </div>
              <div>
                <Text size="xs" fw={600} mb={4}>Severity</Text>
                <Chip.Group multiple value={severityFilter} onChange={setSeverityFilter}>
                  <Group gap={4}>
                    {allSeverities.sort((a, b) => (severityOrder[a] || 9) - (severityOrder[b] || 9)).map((sev) => (
                      <Chip key={sev} value={sev} size="xs" variant="outline"
                        color={severityColor[sev] || 'gray'}>
                        {sev}
                      </Chip>
                    ))}
                  </Group>
                </Chip.Group>
              </div>
              <div>
                <Text size="xs" fw={600} mb={4}>Sort by</Text>
                <Select size="xs" w={140} value={sortBy} onChange={(v) => setSortBy(v || 'severity')}
                  leftSection={<IconSortDescending size={14} />}
                  data={[
                    { value: 'severity', label: 'Severity' },
                    { value: 'risk', label: 'Risk Score' },
                    { value: 'confidence', label: 'Confidence' },
                    { value: 'affected', label: 'Affected Count' },
                  ]} />
              </div>
              {(areaFilter.length > 0 || severityFilter.length > 0) && (
                <Button variant="subtle" size="xs" onClick={() => { setAreaFilter([]); setSeverityFilter([]); }}>
                  Clear filters
                </Button>
              )}
            </Group>
          </Card>
        )}

        {/* Insights Report */}
        {filtered.length > 0 && (
          <>
            <Group justify="space-between">
              <Title order={3}>Insights ({filtered.length})</Title>
            </Group>
            <Stack gap="sm">
              {filtered.map((insight, idx) => (
                <Card key={idx} withBorder p="md" radius="md" component={Link}
                  href={`/projects/${id}/discoveries/${runId}/insights/${insight.id || idx}`}
                  style={{ textDecoration: 'none', cursor: 'pointer',
                    borderLeft: `3px solid var(--mantine-color-${severityColor[insight.severity] || 'gray'}-6)` }}>
                  <Group justify="space-between" mb={4}>
                    <Group gap="xs">
                      <IconAlertTriangle size={14}
                        color={`var(--mantine-color-${severityColor[insight.severity] || 'gray'}-6)`} />
                      <Text size="sm" fw={600}>{insight.name}</Text>
                    </Group>
                    <Group gap="xs">
                      <Badge size="xs" color={severityColor[insight.severity] || 'gray'} variant="light">
                        {insight.severity}
                      </Badge>
                      <Badge size="xs" variant="outline">{insight.analysis_area}</Badge>
                      {insight.affected_count > 0 && (
                        <Text size="xs" c="dimmed">{insight.affected_count.toLocaleString()} affected</Text>
                      )}
                      <FeedbackButtons projectId={id} discoveryId={runId} targetType="insight"
                        targetId={String(insight.id || idx)}
                        feedback={feedbackMap[`insight:${insight.id || idx}`]}
                        onUpdate={(fb) => handleFeedbackUpdate('insight', String(insight.id || idx), fb)} />
                    </Group>
                  </Group>
                  <Text size="xs" c="dimmed" lineClamp={2}>{insight.description}</Text>
                </Card>
              ))}
            </Stack>
          </>
        )}

        {(discovery.insights || []).length === 0 && (
          <Card withBorder p="xl" ta="center">
            <Text c="dimmed">No insights found in this discovery run.</Text>
          </Card>
        )}

        {/* Recommendations */}
        {(discovery.recommendations || []).length > 0 && (
          <>
            <Title order={3}>
              <IconBulb size={20} style={{ verticalAlign: 'middle', marginRight: 8 }} />
              Recommendations ({discovery.recommendations.length})
            </Title>
            <Stack gap="sm">
              {[...discovery.recommendations]
                .sort((a, b) => a.priority - b.priority)
                .map((rec, idx) => (
                  <RecommendationCard key={idx} rec={rec} projectId={id} discoveryId={runId} idx={idx}
                    feedback={feedbackMap[`recommendation:${idx}`]}
                    onFeedbackUpdate={(fb) => handleFeedbackUpdate('recommendation', String(idx), fb)} />
                ))}
            </Stack>
          </>
        )}
        {/* Transparency: How the AI Found This */}
        {((discovery.exploration_log && discovery.exploration_log.length > 0) ||
          (discovery.analysis_log && discovery.analysis_log.length > 0)) && (
          <>
            <Title order={3}>
              <IconSearch size={20} style={{ verticalAlign: 'middle', marginRight: 8 }} />
              How the AI Found This
            </Title>

            <Accordion variant="separated">
              {/* Exploration Trail */}
              {discovery.exploration_log && discovery.exploration_log.length > 0 && (
                <Accordion.Item value="exploration">
                  <Accordion.Control>
                    <Group gap="xs">
                      <IconDatabase size={16} />
                      <Text size="sm" fw={600}>Exploration Steps ({discovery.exploration_log.length})</Text>
                      <Text size="xs" c="dimmed">SQL queries the agent ran to discover data</Text>
                    </Group>
                  </Accordion.Control>
                  <Accordion.Panel>
                    <Stack gap="sm">
                      {discovery.exploration_log.map((step, idx) => (
                        <Card key={idx} withBorder p="sm" radius="sm">
                          <Group justify="space-between" mb={4}>
                            <Text size="xs" fw={600}>Step {step.step}</Text>
                            <Group gap="xs">
                              {step.row_count > 0 && (
                                <Badge size="xs" variant="outline">{step.row_count} rows</Badge>
                              )}
                              {step.execution_time_ms > 0 && (
                                <Badge size="xs" variant="outline">{step.execution_time_ms}ms</Badge>
                              )}
                              {step.fixed && <Badge size="xs" color="yellow">auto-fixed</Badge>}
                              {step.error && <Badge size="xs" color="red">error</Badge>}
                            </Group>
                          </Group>
                          {step.thinking && (
                            <Text size="xs" c="dimmed" mb={4}>{step.thinking}</Text>
                          )}
                          {step.query && (
                            <Code block style={{ fontSize: '10px', maxHeight: 80, overflow: 'auto' }}>
                              {step.query}
                            </Code>
                          )}
                          {step.error && <Text size="xs" c="red" mt={4}>{step.error}</Text>}
                        </Card>
                      ))}
                    </Stack>
                  </Accordion.Panel>
                </Accordion.Item>
              )}

              {/* Analysis Reasoning */}
              {discovery.analysis_log && discovery.analysis_log.length > 0 && (
                <Accordion.Item value="analysis">
                  <Accordion.Control>
                    <Group gap="xs">
                      <IconBulb size={16} />
                      <Text size="sm" fw={600}>Analysis by Area ({discovery.analysis_log.length})</Text>
                      <Text size="xs" c="dimmed">How insights were extracted from exploration data</Text>
                    </Group>
                  </Accordion.Control>
                  <Accordion.Panel>
                    <Stack gap="sm">
                      {discovery.analysis_log.map((step, idx) => (
                        <Card key={idx} withBorder p="sm" radius="sm">
                          <Group justify="space-between" mb={4}>
                            <Text size="sm" fw={600}>{step.area_name || step.area_id}</Text>
                            <Group gap="xs">
                              <Badge size="xs" variant="outline">{step.relevant_queries} queries fed</Badge>
                              <Badge size="xs" variant="outline">{step.tokens_in + step.tokens_out} tokens</Badge>
                              {step.duration_ms > 0 && (
                                <Badge size="xs" variant="outline">{(step.duration_ms / 1000).toFixed(1)}s</Badge>
                              )}
                              {step.error && <Badge size="xs" color="red">error</Badge>}
                            </Group>
                          </Group>
                          {step.error && <Text size="xs" c="red">{step.error}</Text>}
                        </Card>
                      ))}
                    </Stack>
                  </Accordion.Panel>
                </Accordion.Item>
              )}

              {/* Validation Trail */}
              {discovery.validation_log && discovery.validation_log.length > 0 && (
                <Accordion.Item value="validation">
                  <Accordion.Control>
                    <Group gap="xs">
                      <Text size="sm" fw={600}>Validation ({discovery.validation_log.length})</Text>
                      <Text size="xs" c="dimmed">How insights were verified against the warehouse</Text>
                    </Group>
                  </Accordion.Control>
                  <Accordion.Panel>
                    <Stack gap="sm">
                      {discovery.validation_log.map((v, idx) => (
                        <Card key={idx} withBorder p="sm" radius="sm">
                          <Group justify="space-between" mb={4}>
                            <Text size="xs">{v.analysis_area}</Text>
                            <Badge size="xs" variant="light"
                              color={v.status === 'confirmed' ? 'green' : v.status === 'adjusted' ? 'yellow' : v.status === 'error' ? 'red' : 'gray'}>
                              {v.status}
                            </Badge>
                          </Group>
                          {v.claimed_count > 0 && (
                            <Text size="xs" c="dimmed">
                              Claimed: {v.claimed_count.toLocaleString()} → Verified: {v.verified_count.toLocaleString()}
                            </Text>
                          )}
                          <Text size="xs" c="dimmed">{v.reasoning}</Text>
                        </Card>
                      ))}
                    </Stack>
                  </Accordion.Panel>
                </Accordion.Item>
              )}
            </Accordion>
          </>
        )}
      </Stack>
    </Shell>
  );
}

function RecommendationCard({ rec, projectId, discoveryId, idx, feedback, onFeedbackUpdate }: {
  rec: Recommendation; projectId: string; discoveryId: string; idx: number;
  feedback?: Feedback | null; onFeedbackUpdate?: (fb: Feedback | null) => void;
}) {
  const priorityColor = rec.priority <= 1 ? 'red' : rec.priority <= 2 ? 'orange' : 'blue';
  return (
    <Card withBorder p="md" radius="md"
      style={{ borderLeft: `3px solid var(--mantine-color-${priorityColor}-6)` }}>
      <Group justify="space-between" mb={4}>
        <Text size="sm" fw={600}>{rec.title}</Text>
        <Group gap="xs">
          <Badge color={priorityColor} variant="light" size="xs">P{rec.priority}</Badge>
          <FeedbackButtons projectId={projectId} discoveryId={discoveryId} targetType="recommendation"
            targetId={String(idx)} feedback={feedback} onUpdate={onFeedbackUpdate} />
        </Group>
      </Group>
      <Text size="xs" c="dimmed" mb="sm">{rec.description}</Text>
      {rec.expected_impact && (
        <Group gap="xs" mb="xs">
          <IconTrendingUp size={12} />
          <Text size="xs" c="dimmed">
            {rec.expected_impact.metric}: {rec.expected_impact.estimated_improvement}
          </Text>
        </Group>
      )}
      {rec.actions && rec.actions.length > 0 && (
        <Stack gap={2}>
          {rec.actions.slice(0, 3).map((action, i) => (
            <Text key={i} size="xs" c="dimmed">- {action}</Text>
          ))}
        </Stack>
      )}
    </Card>
  );
}
