'use client';

import { useCallback, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Badge, Box, Button, Card, Grid, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { IconArrowLeft, IconStarFilled } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import Markdown from '@/components/common/Markdown';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import SuggestedQuestions from '@/components/ask/SuggestedQuestions';
import BookmarkButton from '@/components/lists/BookmarkButton';
import RelatedSidebar, { RelatedChipStrip, RelatedItem } from '@/components/lists/RelatedSidebar';
import SimilarItems from '@/components/lists/SimilarItems';
import { ValidationRouter } from '@/components/validation/ValidationRouter';
import { ConfidenceCell } from '@/components/scoring/ConfidenceCell';
import { SeverityCell } from '@/components/scoring/SeverityCell';
import { PriorityCell } from '@/components/scoring/PriorityCell';
import { ScoringBreakdown } from '@/components/scoring/ScoringBreakdown';
import { markRead } from '@/lib/readState';
import { api, DiscoveryResult, Feedback, Insight, Project, Recommendation, SearchResultItem } from '@/lib/api';

// The tint for a cited insight's badge in the sidebar. `critical` is kept for
// insights written before severity was derived.
const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
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
  const [project, setProject] = useState<Project | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);
  const [similarRecs, setSimilarRecs] = useState<SearchResultItem[]>([]);

  const refetchDiscovery = useCallback(async () => {
    const disc = await api.getDiscoveryById(runId);
    setDiscovery(disc);
    const found = (disc?.recommendations || []).find((r) => r.id === recommendationId) || null;
    setRecommendation(found);
  }, [runId, recommendationId]);

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
      api.getProject(id).then(setProject).catch(() => setProject(null)),
      api.listFeedback(runId).then((fb) => {
        const match = (fb || []).find((f) => f.target_type === 'recommendation' && f.target_id === recommendationId);
        if (match) setFeedback(match);
      }).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [id, runId, recommendationId]);

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

  const relatedInsights = (recommendation.related_insight_ids || [])
    .map(rid => (discovery?.insights || []).find(i => i.id === rid))
    .filter(Boolean) as Insight[];

  // Shape related insights for the sticky right sidebar. Similar
  // recommendations render separately below the grid as rich cards — see
  // the twin comment on the insight detail page.
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
          {/* The tier, and the severity inherited from the insights this
              addresses. The "P{n}" badge and the "effort" pill both read the
              priority integer the analysis model used to write; it comes from
              the priority matrix now, so the tier says what it says without
              the indirection -- and nothing here ever claimed to know how much
              work an action is. */}
          <PriorityCell scoring={recommendation.evidence_scoring} />
          <SeverityCell scoring={recommendation.evidence_scoring}
            severity={recommendation.severity ?? ''} showDriver={false} />
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
        />
      </Box>

      <Grid gutter="lg">
        <Grid.Col span={{ base: 12, lg: 9 }}>
      <Stack gap="lg" maw={800}>
        {/* Description — formatted Markdown when description_md is present.
            Plain/legacy descriptions are shown verbatim (not re-parsed as
            Markdown) so stray metacharacters in older runs are not
            reinterpreted. */}
        <Card withBorder p="lg">
          {recommendation.description_md
            ? <Markdown>{recommendation.description_md}</Markdown>
            : recommendation.description
              ? <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{recommendation.description}</Text>
              : <Text size="sm" c="dimmed">No description</Text>}
          {/* LLM-generated starter questions + "Ask about this" (enterprise;
              renders nothing on community builds or when the toggle is off). */}
          <SuggestedQuestions projectId={id} seed={{ type: 'recommendation', id: recommendation.id, title: recommendation.title }} />
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
              {(recommendation.evidence_scoring || recommendation.confidence > 0) && (
                <div>
                  <Text size="xs" c="dimmed">Confidence</Text>
                  <ConfidenceCell
                    scoring={recommendation.evidence_scoring}
                    confidence={recommendation.confidence}
                    showReason
                  />
                </div>
              )}
            </Group>
          </Card>
        )}

        {/* How this was scored. Severity here is INHERITED from the insights
            this recommendation cites -- the worst of them -- so the breakdown
            shows their driver; confidence is its own, from whether the
            verifier agreed this action follows from those findings. */}
        {recommendation.evidence_scoring && (
          <Card withBorder p="lg" data-testid="scoring-card">
            <ScoringBreakdown scoring={recommendation.evidence_scoring} axis="all" />
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

        {/* Narrow-screen fallback for the validation card — the sidebar
            is hidden below the lg breakpoint so we render it inline at
            the bottom of the main column. */}
        <Box hiddenFrom="lg">
          <ValidationRouter
            validation={recommendation.validation}
            discoveryId={runId}
            docKind="recommendation"
            docId={recommendation.id}
            validationEnabled={project?.validation_enabled !== false}
            projectSettingsHref={`/projects/${id}/settings#advanced`}
            onTerminal={refetchDiscovery}
          />
        </Box>
      </Stack>
        </Grid.Col>

        <Grid.Col span={{ base: 12, lg: 3 }} visibleFrom="lg">
          <Box style={{ position: 'sticky', top: 16 }}>
            <Stack gap="md">
              <RelatedSidebar
                relatedLabel="Related Insights"
                related={relatedItems}
              />
              <ValidationRouter
                validation={recommendation.validation}
                discoveryId={runId}
                docKind="recommendation"
                docId={recommendation.id}
                validationEnabled={project?.validation_enabled !== false}
                projectSettingsHref={`/projects/${id}/settings#advanced`}
                onTerminal={refetchDiscovery}
              />
            </Stack>
          </Box>
        </Grid.Col>
      </Grid>

      {/* Similar Recommendations — full-width exploration section below
          the grid. Twin of the same block on the insight detail page. */}
      <div style={{ maxWidth: 800 }}>
        <SimilarItems
          label="Similar Recommendations"
          items={similarRecs}
          hrefFor={(sim) => `/projects/${id}/discoveries/${sim.discovery_id}/recommendations/${sim.id}`}
        />
      </div>
    </Shell>
  );
}
