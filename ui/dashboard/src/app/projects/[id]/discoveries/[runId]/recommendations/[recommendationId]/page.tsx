'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import {
  Badge, Button, Card, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { IconArrowLeft, IconStarFilled } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import {
  Pill, normalizeConfidence,
} from '@/components/common/UIComponents';
import { api, DiscoveryResult, Feedback, Insight, Recommendation, SearchResultItem } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

const effortColors: Record<string, { bg: string; color: string }> = {
  low: { bg: '#EAF3DE', color: '#3B6D11' },
  medium: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
  high: { bg: '#FAECE7', color: '#993C1D' },
};

export default function RecommendationDetailPage() {
  const { id, runId, recommendationId } = useParams<{ id: string; runId: string; recommendationId: string }>();
  const [recommendation, setRecommendation] = useState<Recommendation | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);
  const [similarRecs, setSimilarRecs] = useState<SearchResultItem[]>([]);

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then((disc) => {
        setDiscovery(disc);
        const recs = disc?.recommendations || [];
        const found = recs.find((r) => r.id === recommendationId) || recs[parseInt(recommendationId)] || null;
        setRecommendation(found);
      }),
      api.listFeedback(runId).then((fb) => {
        const match = (fb || []).find((f) => f.target_type === 'recommendation' && f.target_id === recommendationId);
        if (match) setFeedback(match);
      }).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [runId, recommendationId]);

  // Fetch similar recommendations via semantic search (non-blocking)
  useEffect(() => {
    if (!recommendation) return;
    api.searchInsights(id, { query: recommendation.title, limit: 6, types: ['recommendation'] })
      .then(resp => {
        setSimilarRecs(resp.results.filter(r => r.id !== recommendationId && r.name !== recommendation.title));
      })
      .catch(() => {});
  }, [id, recommendation, recommendationId]);

  if (loading) return <Shell><Loader /></Shell>;
  if (!recommendation) return <Shell><Text>Recommendation not found</Text></Shell>;

  const effort = recommendation.priority <= 1 ? 'low' : recommendation.priority <= 3 ? 'medium' : 'high';
  const effortStyle = effortColors[effort] || effortColors.medium;

  const relatedInsights = (recommendation.related_insight_ids || [])
    .map(rid => (discovery?.insights || []).find(i => i.id === rid))
    .filter(Boolean) as Insight[];

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
            <IconStarFilled size={20} color="var(--db-purple-text)" />
            <Title order={2}>{recommendation.title}</Title>
          </Group>
          <Group gap="xs">
            <Badge color={recommendation.priority <= 1 ? 'red' : recommendation.priority <= 2 ? 'orange' : 'blue'} variant="light">
              P{recommendation.priority}
            </Badge>
            <Pill bg={effortStyle.bg} color={effortStyle.color}>
              {effort.charAt(0).toUpperCase() + effort.slice(1)} effort
            </Pill>
            {recommendation.category && <Badge variant="outline">{recommendation.category}</Badge>}
            <FeedbackButtons projectId={id} discoveryId={runId} targetType="recommendation" targetId={recommendationId}
              feedback={feedback} onUpdate={setFeedback} />
          </Group>
        </div>

        {/* Description */}
        <Card withBorder p="lg">
          <Text size="sm">{recommendation.description}</Text>
        </Card>

        {/* Impact */}
        {recommendation.expected_impact && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Expected Impact</Title>
            <Group gap="xl">
              {recommendation.expected_impact.metric && (
                <div>
                  <Text size="xs" c="dimmed">Metric</Text>
                  <Text size="sm" fw={600}>{recommendation.expected_impact.metric}</Text>
                </div>
              )}
              {recommendation.expected_impact.estimated_improvement && (
                <div>
                  <Text size="xs" c="dimmed">Estimated Improvement</Text>
                  <Text size="sm" fw={600} c="green">{recommendation.expected_impact.estimated_improvement}</Text>
                </div>
              )}
            </Group>
            {recommendation.expected_impact.reasoning && (
              <Text size="sm" c="dimmed" mt="sm">{recommendation.expected_impact.reasoning}</Text>
            )}
          </Card>
        )}

        {/* Target Segment */}
        {recommendation.target_segment && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Target Segment</Title>
            <Group gap="xl">
              <div>
                <Text size="xs" c="dimmed">Segment</Text>
                <Text size="sm" fw={600}>{recommendation.target_segment}</Text>
              </div>
              {recommendation.segment_size > 0 && (
                <div>
                  <Text size="xs" c="dimmed">Segment Size</Text>
                  <Text size="sm" fw={600}>{recommendation.segment_size.toLocaleString()}</Text>
                </div>
              )}
              {recommendation.confidence > 0 && (
                <div>
                  <Text size="xs" c="dimmed">Confidence</Text>
                  <Text size="sm" fw={600}>{normalizeConfidence(recommendation.confidence)}%</Text>
                </div>
              )}
            </Group>
          </Card>
        )}

        {/* Action Steps */}
        {recommendation.actions && recommendation.actions.length > 0 && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Action Steps</Title>
            <Stack gap="xs">
              {recommendation.actions.map((action, i) => (
                <Group key={i} gap="sm" align="flex-start" wrap="nowrap">
                  <Text size="sm" fw={600} c="dimmed" style={{ flexShrink: 0, minWidth: 20 }}>{i + 1}.</Text>
                  <Text size="sm">{action}</Text>
                </Group>
              ))}
            </Stack>
          </Card>
        )}

        {/* Related Insights */}
        {relatedInsights.length > 0 && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">Related Insights</Title>
            <Stack gap="xs">
              {relatedInsights.map((insight) => (
                <Link key={insight.id} href={`/projects/${id}/discoveries/${runId}/insights/${insight.id}`}
                  style={{ textDecoration: 'none' }}>
                  <Card padding="xs" withBorder style={{ cursor: 'pointer' }}>
                    <Group justify="space-between" wrap="nowrap">
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <Text size="sm" truncate>{insight.name}</Text>
                        {insight.description && (
                          <Text size="xs" c="dimmed" lineClamp={1}>{insight.description}</Text>
                        )}
                      </div>
                      <Group gap={6} wrap="nowrap">
                        {insight.severity && (
                          <Badge size="xs" color={severityColor[insight.severity] || 'gray'} variant="light">
                            {insight.severity}
                          </Badge>
                        )}
                        {insight.affected_count > 0 && (
                          <Badge size="xs" variant="outline">
                            {insight.affected_count.toLocaleString()} affected
                          </Badge>
                        )}
                      </Group>
                    </Group>
                  </Card>
                </Link>
              ))}
            </Stack>
          </Card>
        )}

        {/* Similar Recommendations (semantic search) */}
        {similarRecs.length > 0 && (
          <div style={{ marginTop: 8 }}>
            <Text size="sm" fw={600} mb="xs">Similar Recommendations</Text>
            <Stack gap="xs">
              {similarRecs.map(sim => (
                <Link key={sim.id} href={`/projects/${id}/discoveries/${sim.discovery_id}/recommendations/${sim.id}`}
                  style={{ textDecoration: 'none' }}>
                  <Card padding="xs" withBorder style={{ cursor: 'pointer' }}>
                    <Group justify="space-between" wrap="nowrap">
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <Text size="sm" truncate>{sim.name}</Text>
                        <Text size="xs" c="dimmed">
                          {sim.discovered_at ? new Date(sim.discovered_at).toLocaleDateString() : ''}
                          {sim.analysis_area ? ` · ${sim.analysis_area}` : ''}
                        </Text>
                      </div>
                      <Badge size="xs" variant="light" color="blue">
                        {Math.round(sim.score * 100)}% related
                      </Badge>
                    </Group>
                  </Card>
                </Link>
              ))}
            </Stack>
          </div>
        )}
      </Stack>
    </Shell>
  );
}
