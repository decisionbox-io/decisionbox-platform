'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Accordion, Badge, Box, Button, Card, Code, Drawer, Grid, Group, Loader, Stack, Table, Text, Title,
} from '@mantine/core';
import {
  IconAlertTriangle, IconArrowLeft, IconCheck, IconCode, IconDatabase, IconSearch, IconX,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import BookmarkButton from '@/components/lists/BookmarkButton';
import RelatedSidebar, { RelatedChipStrip, RelatedItem } from '@/components/lists/RelatedSidebar';
import SimilarItems from '@/components/lists/SimilarItems';
import { markRead } from '@/lib/readState';
import { api, DiscoveryResult, Feedback, Insight, SearchResultItem } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

// CompactValidationCard is sized for the right sidebar (and the narrow-screen
// fallback that lives at the bottom of the main column). Trades the big
// padding + wide Group layout of the old inline Validation card for a tighter
// presentation — status badge, counts on one row, reasoning clipped — so it
// sits next to the related-items sidebar without dominating.
function CompactValidationCard({ validation }: { validation: NonNullable<Insight['validation']> }) {
  const statusColor = validation.status === 'confirmed' ? 'green'
    : validation.status === 'adjusted' ? 'yellow'
    : validation.status === 'rejected' ? 'red' : 'gray';
  const statusIcon = validation.status === 'confirmed' ? <IconCheck size={12} /> : <IconX size={12} />;
  return (
    <Card withBorder p="md">
      <Group justify="space-between" mb={6}>
        <Text size="xs" fw={600} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.5px' }}>
          Validation
        </Text>
        <Badge size="sm" color={statusColor} leftSection={statusIcon} variant="light">
          {validation.status}
        </Badge>
      </Group>
      {(validation.original_count != null || validation.verified_count != null) && (
        <Group gap={4} mb={6}>
          {validation.original_count != null && (
            <Text size="xs" c="dimmed">{validation.original_count.toLocaleString()}</Text>
          )}
          {validation.verified_count != null && (
            <>
              <Text size="xs" c="dimmed">→</Text>
              <Text size="xs" fw={600}>{validation.verified_count.toLocaleString()} verified</Text>
            </>
          )}
        </Group>
      )}
      {validation.reasoning && (
        <Text size="xs" c="dimmed" lineClamp={3}>{validation.reasoning}</Text>
      )}
    </Card>
  );
}

export default function InsightDetailPage() {
  const { id, runId, insightId } = useParams<{ id: string; runId: string; insightId: string }>();
  const router = useRouter();
  // goBack relies on browser history whenever possible — that's the only
  // way we scale across every entry point (similar insights, Ask sources,
  // related sidebar, bookmark lists, insights list, discovery detail, ...)
  // without having to wire a `?from=` hint at every call site. The
  // history-length guard handles the fresh-tab case where router.back()
  // would otherwise navigate out of the app to nothing.
  const goBack = () => {
    if (typeof window !== 'undefined' && window.history.length > 1) {
      router.back();
    } else {
      router.push(`/projects/${id}/discoveries/${runId}`);
    }
  };
  const [insight, setInsight] = useState<Insight | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);
  const [similarInsights, setSimilarInsights] = useState<SearchResultItem[]>([]);
  // Technical details (SQL queries, exploration steps, token counts) are
  // opened explicitly via a button in the sidebar. The Drawer gets the full
  // viewport width, so code blocks don't have to squeeze into the sidebar.
  const [techOpen, setTechOpen] = useState(false);

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then((disc) => {
        setDiscovery(disc);
        // Match strictly by id. Do NOT fall back to insights[parseInt(insightId)]
        // — UUIDs like "67be9dfd-..." happen to parse to small integers and
        // silently return the wrong insight. The agent now assigns UUIDs that
        // match the standalone collection + Qdrant point id, so the exact-id
        // lookup always resolves for data written after this commit.
        const insights = disc?.insights || [];
        const found = insights.find((i) => i.id === insightId) || null;
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

  // Record that the user has opened this insight. Fire-and-forget —
  // markRead dedupes at the server layer (unique index) and optimistically
  // updates the shared read set, so listing pages can apply greyed styling.
  useEffect(() => {
    if (!insight || !insightId) return;
    markRead(id, 'insight', insightId).catch(() => {});
  }, [id, insightId, insight]);

  // Fetch similar insights via semantic search (non-blocking)
  useEffect(() => {
    if (!insight) return;
    api.searchInsights(id, { query: insight.name, limit: 6, types: ['insight'] })
      .then(resp => {
        // Exclude the current insight from results
        setSimilarInsights(resp.results.filter(r => r.id !== insightId && r.name !== insight.name));
      })
      .catch(() => {});
  }, [id, insight, insightId]);

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

  // Related recommendations — recs in this discovery that cite this insight id.
  const relatedRecs = (discovery?.recommendations || []).filter(
    (r) => r.related_insight_ids?.includes(insight.id)
  );

  // Shape related items for the right sidebar / mobile chip strip. Similar
  // (semantic-search) items are rendered separately below the main content
  // as rich cards — they're exploration, not direct navigation, so they
  // deserve the space to show a description snippet instead of being
  // crammed into a sticky column.
  const relatedItems: RelatedItem[] = relatedRecs.map((rec, i) => ({
    id: String(rec.id || i),
    title: rec.title,
    href: `/projects/${id}/discoveries/${runId}/recommendations/${rec.id || i}`,
    badge: {
      label: `P${rec.priority}`,
      color: rec.priority <= 1 ? 'red' : rec.priority <= 2 ? 'orange' : 'blue',
    },
    subtitle: rec.expected_impact?.estimated_improvement,
  }));

  return (
    <Shell>
      <Button variant="subtle" onClick={goBack}
        leftSection={<IconArrowLeft size={16} />} size="sm" w="fit-content" mb="md">
        Back
      </Button>

      {/* Header — full width so title can breathe, no sidebar beside it. */}
      <div style={{ maxWidth: 800, marginBottom: 16 }}>
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
          <BookmarkButton projectId={id} discoveryId={runId} targetType="insight" targetId={insightId} />
        </Group>
      </div>

      {/* Mobile chip strip — related + similar items collapsed into a
          horizontally-scrollable strip. Hidden once the right sidebar shows. */}
      <Box hiddenFrom="lg" mb="md">
        <RelatedChipStrip
          relatedLabel="Related Recommendations"
          related={relatedItems}
        />
      </Box>

      <Grid gutter="lg">
        <Grid.Col span={{ base: 12, lg: 9 }}>
      <Stack gap="lg" maw={800}>
        {/* Description — the narrative "what". */}
        <Card withBorder p="lg">
          <Text size="sm">{insight.description}</Text>
        </Card>

        {/* Assessment — risk, confidence, target segment. Promoted above
            Indicators/Metrics so skimming readers see the decision-ready
            numbers right after the description. */}
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

        {/* Key Indicators — plain-language bullets supporting the claim. */}
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

        {/* Metrics — raw numbers for readers who want to dig in. */}
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

        {/* Narrow-screen fallback: Validation + Technical Details trigger.
            On ≥ lg the sidebar hosts both; below that we render them inline
            at the bottom of the main column so they're still accessible. */}
        <Box hiddenFrom="lg">
          <Stack gap="md">
            {insight.validation && (
              <CompactValidationCard validation={insight.validation} />
            )}
            <Button
              variant="subtle"
              size="sm"
              leftSection={<IconCode size={14} />}
              onClick={() => setTechOpen(true)}
              w="fit-content"
            >
              Show technical details
            </Button>
          </Stack>
        </Box>

        {insight.discovered_at && (
          <Text size="xs" c="dimmed">Discovered: {new Date(insight.discovered_at).toLocaleString()}</Text>
        )}
      </Stack>
        </Grid.Col>

        {/* Right sidebar — navigation (related) + supporting content
            (validation + tech-details trigger). Sticky so it stays in view
            as the user scrolls the main narrative. */}
        <Grid.Col span={{ base: 12, lg: 3 }} visibleFrom="lg">
          <Box style={{ position: 'sticky', top: 16 }}>
            <Stack gap="md">
              <RelatedSidebar
                relatedLabel="Related Recommendations"
                related={relatedItems}
              />
              {insight.validation && (
                <CompactValidationCard validation={insight.validation} />
              )}
              <Button
                variant="subtle"
                size="sm"
                leftSection={<IconCode size={14} />}
                onClick={() => setTechOpen(true)}
                justify="flex-start"
                fullWidth
              >
                Show technical details
              </Button>
            </Stack>
          </Box>
        </Grid.Col>
      </Grid>

      {/* Technical Details Drawer — full-viewport height on the right. The
          SQL code blocks inside need width they wouldn't get in a 240px
          sidebar, and wrapping them in a Drawer keeps the reader's main
          scroll position intact when they close it. */}
      <Drawer
        opened={techOpen}
        onClose={() => setTechOpen(false)}
        position="right"
        size="lg"
        title={
          <Group gap="xs">
            <IconSearch size={18} />
            <Text fw={600}>How This Insight Was Found</Text>
          </Group>
        }
      >
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
      </Drawer>

      {/* Similar Insights — full-width exploration section. The content
          column is capped at ~720px (9/12 of the Grid), so this sits below
          that width and visually complements rather than sprawls. */}
      <div style={{ maxWidth: 800 }}>
        <SimilarItems
          label="Similar Insights"
          items={similarInsights}
          hrefFor={(sim) => `/projects/${id}/discoveries/${sim.discovery_id}/insights/${sim.id}`}
        />
      </div>
    </Shell>
  );
}
