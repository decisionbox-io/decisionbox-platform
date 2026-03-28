'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import {
  Accordion, Badge, Button, Card, Code, Group, Loader, Stack, Table, Text, Title,
} from '@mantine/core';
import {
  IconAlertTriangle, IconArrowLeft, IconCheck, IconDatabase, IconSearch, IconX,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import { api, DiscoveryResult, Feedback, Insight } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

export default function InsightDetailPage() {
  const { id, runId, insightId } = useParams<{ id: string; runId: string; insightId: string }>();
  const [insight, setInsight] = useState<Insight | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then((disc) => {
        setDiscovery(disc);
        const insights = disc?.insights || [];
        const found = insights.find((i) => i.id === insightId) || insights[parseInt(insightId)] || null;
        setInsight(found);
      }),
      api.listFeedback(runId).then((fb) => {
        const match = (fb || []).find((f) => f.target_type === 'insight' && f.target_id === insightId);
        if (match) setFeedback(match);
      }).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [runId, insightId]);

  if (loading) return <Shell><Loader /></Shell>;
  if (!insight) return <Shell><Text>Insight not found</Text></Shell>;

  // Get the exploration steps this insight is based on (cited by the LLM)
  const sourceSteps = (insight.source_steps || [])
    .map((stepNum) => (discovery?.exploration_log || []).find((s) => s.step === stepNum))
    .filter(Boolean);

  // Get the analysis step for this insight's area
  const analysisStep = discovery?.analysis_log?.find((a) => a.area_id === insight.analysis_area);

  // Get validation entries for this insight's area
  const validationEntries = (discovery?.validation_log || []).filter(
    (v) => v.analysis_area === insight.analysis_area
  );

  return (
    <Shell>
      <Stack gap="lg" maw={800}>
        <Button variant="subtle" component={Link}
          href={`/projects/${id}/discoveries/${runId}`}
          leftSection={<IconArrowLeft size={16} />} size="sm" w="fit-content">
          Back to Discovery
        </Button>

        {/* Header */}
        <div>
          <Group gap="sm" mb={4}>
            <IconAlertTriangle size={20}
              color={`var(--mantine-color-${severityColor[insight.severity] || 'gray'}-6)`} />
            <Title order={2}>{insight.name}</Title>
          </Group>
          <Group gap="xs">
            <Badge color={severityColor[insight.severity] || 'gray'} variant="light">{insight.severity}</Badge>
            <Badge variant="outline">{insight.analysis_area}</Badge>
            {insight.affected_count > 0 && (
              <Badge variant="outline">{insight.affected_count.toLocaleString()} affected</Badge>
            )}
            <FeedbackButtons projectId={id} discoveryId={runId} targetType="insight" targetId={insightId}
              feedback={feedback} onUpdate={setFeedback} />
          </Group>
        </div>

        {/* Description */}
        <Card withBorder p="lg">
          <Text size="sm">{insight.description}</Text>
        </Card>

        {/* Indicators */}
        {insight.indicators && insight.indicators.length > 0 && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Key Indicators</Title>
            <Stack gap={6}>
              {insight.indicators.map((ind, i) => (
                <Text key={i} size="sm">- {ind}</Text>
              ))}
            </Stack>
          </Card>
        )}

        {/* Metrics */}
        {insight.metrics && Object.keys(insight.metrics).length > 0 && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Metrics</Title>
            <Table>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Metric</Table.Th>
                  <Table.Th>Value</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {Object.entries(insight.metrics).map(([key, value]) => (
                  <Table.Tr key={key}>
                    <Table.Td><Text size="sm">{key.replace(/_/g, ' ')}</Text></Table.Td>
                    <Table.Td><Text size="sm" fw={600}>{String(value)}</Text></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Card>
        )}

        {/* Assessment */}
        <Card withBorder p="lg">
          <Title order={4} mb="sm">Assessment</Title>
          <Group gap="xl">
            <div>
              <Text size="xs" c="dimmed">Risk Score</Text>
              <Text size="lg" fw={700} c={insight.risk_score > 0.7 ? 'red' : insight.risk_score > 0.4 ? 'orange' : 'green'}>
                {(insight.risk_score * 100).toFixed(0)}%
              </Text>
            </div>
            <div>
              <Text size="xs" c="dimmed">Confidence</Text>
              <Text size="lg" fw={700}>{(insight.confidence * 100).toFixed(0)}%</Text>
            </div>
            {insight.target_segment && (
              <div>
                <Text size="xs" c="dimmed">Target Segment</Text>
                <Text size="sm">{insight.target_segment}</Text>
              </div>
            )}
          </Group>
        </Card>

        {/* Validation */}
        {insight.validation && (
          <Card withBorder p="lg">
            <Group mb="sm">
              <Title order={4}>Validation</Title>
              <Badge
                color={insight.validation.status === 'confirmed' ? 'green' :
                       insight.validation.status === 'adjusted' ? 'yellow' :
                       insight.validation.status === 'rejected' ? 'red' : 'gray'}
                leftSection={insight.validation.status === 'confirmed' ? <IconCheck size={12} /> : <IconX size={12} />}>
                {insight.validation.status}
              </Badge>
            </Group>
            {(insight.validation.original_count || insight.validation.verified_count) && (
              <Group gap="xl" mb="sm">
                {insight.validation.original_count != null && (
                  <div>
                    <Text size="xs" c="dimmed">Claimed Count</Text>
                    <Text size="sm" fw={600}>{insight.validation.original_count.toLocaleString()}</Text>
                  </div>
                )}
                {insight.validation.verified_count != null && (
                  <div>
                    <Text size="xs" c="dimmed">Verified Count</Text>
                    <Text size="sm" fw={600}>{insight.validation.verified_count.toLocaleString()}</Text>
                  </div>
                )}
              </Group>
            )}
            {insight.validation.reasoning && (
              <Text size="xs" c="dimmed">{insight.validation.reasoning}</Text>
            )}
          </Card>
        )}

        {/* Related Recommendations */}
        {(() => {
          const relatedRecs = (discovery?.recommendations || []).filter(
            (r) => r.related_insight_ids?.includes(insight.id)
          );
          if (relatedRecs.length === 0) return null;
          return (
            <Card withBorder p="lg">
              <Title order={4} mb="sm">Related Recommendations</Title>
              <Stack gap="xs">
                {relatedRecs.map((rec, i) => (
                  <div key={i} style={{
                    border: '1px solid var(--db-border-default)',
                    borderRadius: 'var(--db-radius)',
                    padding: '10px 14px',
                  }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8 }}>
                      <Text size="sm" fw={500}>{rec.title}</Text>
                      <div style={{ display: 'flex', gap: 4, flexShrink: 0 }}>
                        <Badge size="xs" color={rec.priority <= 1 ? 'red' : rec.priority <= 2 ? 'orange' : 'blue'}>
                          P{rec.priority}
                        </Badge>
                        {rec.expected_impact?.estimated_improvement && (
                          <Badge size="xs" color="green" variant="light">
                            {rec.expected_impact.estimated_improvement}
                          </Badge>
                        )}
                      </div>
                    </div>
                    {rec.description && (
                      <Text size="xs" c="dimmed" mt={4} lineClamp={2}>{rec.description}</Text>
                    )}
                    {rec.actions && rec.actions.length > 0 && (
                      <div style={{ marginTop: 6 }}>
                        <Text size="xs" c="dimmed" fw={500}>Actions:</Text>
                        {rec.actions.slice(0, 2).map((a, j) => (
                          <Text key={j} size="xs" c="dimmed">  {j + 1}. {a}</Text>
                        ))}
                        {rec.actions.length > 2 && (
                          <Text size="xs" c="dimmed">  ... +{rec.actions.length - 2} more</Text>
                        )}
                      </div>
                    )}
                  </div>
                ))}
              </Stack>
            </Card>
          );
        })()}

        {/* How This Insight Was Found */}
        <Title order={3}>
          <IconSearch size={18} style={{ verticalAlign: 'middle', marginRight: 8 }} />
          How This Insight Was Found
        </Title>

        <Accordion variant="separated" defaultValue="exploration">
          {/* Source exploration queries (cited by the LLM) */}
          {sourceSteps.length > 0 && (
            <Accordion.Item value="exploration">
              <Accordion.Control>
                <Group gap="xs">
                  <IconDatabase size={16} />
                  <Text size="sm" fw={600}>Source Data ({sourceSteps.length} queries cited)</Text>
                  <Text size="xs" c="dimmed">The specific queries the AI used for this insight</Text>
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <Stack gap="sm">
                  {sourceSteps.map((step, idx) => step && (
                    <Card key={idx} withBorder p="sm" radius="sm">
                      <Group justify="space-between" mb={4}>
                        <Text size="xs" fw={600}>Step {step.step}</Text>
                        <Group gap="xs">
                          {step.row_count > 0 && <Badge size="xs" variant="outline">{step.row_count} rows</Badge>}
                          {step.execution_time_ms > 0 && <Badge size="xs" variant="outline">{step.execution_time_ms}ms</Badge>}
                        </Group>
                      </Group>
                      {step.thinking && <Text size="xs" c="dimmed" mb={4}>{step.thinking}</Text>}
                      {step.query && (
                        <Code block style={{ fontSize: '10px', maxHeight: 120, overflow: 'auto' }}>
                          {step.query}
                        </Code>
                      )}
                    </Card>
                  ))}
                </Stack>
              </Accordion.Panel>
            </Accordion.Item>
          )}

          {/* No source steps — show message */}
          {sourceSteps.length === 0 && (
            <Card withBorder p="sm">
              <Text size="xs" c="dimmed">
                Source step citations not available for this insight.
                {insight.source_steps && insight.source_steps.length > 0
                  ? ` (Steps ${insight.source_steps.join(', ')} cited but not found in exploration log)`
                  : ' Run a new discovery to get per-insight source tracking.'}
              </Text>
            </Card>
          )}

          {/* Analysis step */}
          {analysisStep && (
            <Accordion.Item value="analysis">
              <Accordion.Control>
                <Group gap="xs">
                  <Text size="sm" fw={600}>AI Analysis ({analysisStep.area_name})</Text>
                  <Badge size="xs" variant="outline">{analysisStep.tokens_in + analysisStep.tokens_out} tokens</Badge>
                  {analysisStep.duration_ms > 0 && (
                    <Badge size="xs" variant="outline">{(analysisStep.duration_ms / 1000).toFixed(1)}s</Badge>
                  )}
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <Group gap="xl">
                  <div>
                    <Text size="xs" c="dimmed">Queries Fed</Text>
                    <Text size="sm" fw={600}>{analysisStep.relevant_queries}</Text>
                  </div>
                  <div>
                    <Text size="xs" c="dimmed">Input Tokens</Text>
                    <Text size="sm" fw={600}>{analysisStep.tokens_in.toLocaleString()}</Text>
                  </div>
                  <div>
                    <Text size="xs" c="dimmed">Output Tokens</Text>
                    <Text size="sm" fw={600}>{analysisStep.tokens_out.toLocaleString()}</Text>
                  </div>
                </Group>
              </Accordion.Panel>
            </Accordion.Item>
          )}

          {/* Validation entries */}
          {validationEntries.length > 0 && (
            <Accordion.Item value="validation">
              <Accordion.Control>
                <Text size="sm" fw={600}>Validation ({validationEntries.length} checks)</Text>
              </Accordion.Control>
              <Accordion.Panel>
                <Stack gap="sm">
                  {validationEntries.map((v, idx) => (
                    <Card key={idx} withBorder p="sm" radius="sm">
                      <Group justify="space-between" mb={4}>
                        <Badge size="xs" variant="light"
                          color={v.status === 'confirmed' ? 'green' : v.status === 'adjusted' ? 'yellow' : v.status === 'error' ? 'red' : 'gray'}>
                          {v.status}
                        </Badge>
                        {v.claimed_count > 0 && (
                          <Text size="xs" c="dimmed">
                            {v.claimed_count.toLocaleString()} → {v.verified_count.toLocaleString()}
                          </Text>
                        )}
                      </Group>
                      <Text size="xs" c="dimmed">{v.reasoning}</Text>
                      {v.query && (
                        <Code block mt={4} style={{ fontSize: '10px', maxHeight: 80, overflow: 'auto' }}>
                          {v.query}
                        </Code>
                      )}
                    </Card>
                  ))}
                </Stack>
              </Accordion.Panel>
            </Accordion.Item>
          )}
        </Accordion>

        {insight.discovered_at && (
          <Text size="xs" c="dimmed">Discovered: {new Date(insight.discovered_at).toLocaleString()}</Text>
        )}
      </Stack>
    </Shell>
  );
}
