'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Badge, Box, Button, Card, Grid, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { IconArrowLeft, IconStarFilled } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import BookmarkButton from '@/components/lists/BookmarkButton';
import RelatedSidebar, { RelatedChipStrip, RelatedItem } from '@/components/lists/RelatedSidebar';
import { markRead } from '@/lib/readState';
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
  const router = useRouter();
  // See the twin goBack on the insight detail page for the rationale.
  const goBack = () => {
    if (typeof window !== 'undefined' && window.history.length > 1) {
      router.back();
    } else {
      router.push(`/projects/${id}/discoveries/${runId}`);
    }
  };
  const [recommendation, setRecommendation] = useState<Recommendation | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);
  const [similarRecs, setSimilarRecs] = useState<SearchResultItem[]>([]);

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then((disc) => {
        setDiscovery(disc);
        // Strict id match only. See the twin comment in the insight detail
        // page: UUIDs can masquerade as small integers via parseInt and
        // silently open the wrong recommendation.
        const recs = disc?.recommendations || [];
        const found = recs.find((r) => r.id === recommendationId) || null;
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

  // Record that the user has opened this recommendation. See insight detail
  // page for the rationale — fire-and-forget, server-side dedupe.
  useEffect(() => {
    if (!recommendation || !recommendationId) return;
    markRead(id, 'recommendation', recommendationId).catch(() => {});
  }, [id, recommendationId, recommendation]);

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

  // Shape related insights and similar recommendations for the sidebar.
  const relatedItems: RelatedItem[] = relatedInsights.map(insight => ({
    id: insight.id,
    title: insight.name,
    href: `/projects/${id}/discoveries/${runId}/insights/${insight.id}`,
    badge: insight.severity
      ? { label: insight.severity, color: severityColor[insight.severity] || 'gray' }
      : undefined,
    subtitle: insight.affected_count > 0
      ? `${insight.affected_count.toLocaleString()} affected`
      : undefined,
  }));
  const similarItems: RelatedItem[] = similarRecs.map(sim => ({
    id: sim.id,
    title: sim.name,
    href: `/projects/${id}/discoveries/${sim.discovery_id}/recommendations/${sim.id}`,
    subtitle: sim.analysis_area,
  }));

  return (
    <Shell>
      <Button variant="subtle" onClick={goBack}
        leftSection={<IconArrowLeft size={16} />} size="sm" w="fit-content" mb="md">
        Back
      </Button>

      {/* Header */}
      <div style={{ maxWidth: 800, marginBottom: 16 }}>
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
          <BookmarkButton projectId={id} discoveryId={runId} targetType="recommendation" targetId={recommendationId} />
        </Group>
      </div>

      <Box hiddenFrom="lg" mb="md">
        <RelatedChipStrip
          relatedLabel="Related Insights"
          related={relatedItems}
          similar={similarItems}
        />
      </Box>

      <Grid gutter="lg">
        <Grid.Col span={{ base: 12, lg: 9 }}>
      <Stack gap="lg" maw={800}>
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

        {/* Related insights and similar recommendations moved to the right
            sidebar (or top chip strip on narrow screens). */}
      </Stack>
        </Grid.Col>

        <Grid.Col span={{ base: 12, lg: 3 }} visibleFrom="lg">
          <Box style={{ position: 'sticky', top: 16 }}>
            <RelatedSidebar
              relatedLabel="Related Insights"
              related={relatedItems}
              similarLabel="Similar Recommendations"
              similar={similarItems}
            />
          </Box>
        </Grid.Col>
      </Grid>
    </Shell>
  );
}
