'use client';

import { useCallback, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
import {
  Accordion, Badge, Box, Button, Card, Code, Drawer, Grid, Group, Loader, Stack, Table, Text, Title,
} from '@mantine/core';
import {
  IconAlertTriangle, IconArrowLeft, IconCode, IconDatabase, IconSearch,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import Markdown from '@/components/common/Markdown';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import SuggestedQuestions from '@/components/ask/SuggestedQuestions';
import BookmarkButton from '@/components/lists/BookmarkButton';
import RelatedSidebar, { RelatedChipStrip, RelatedItem } from '@/components/lists/RelatedSidebar';
import SimilarItems from '@/components/lists/SimilarItems';
import { ValidationRouter } from '@/components/validation/ValidationRouter';
import { ValidationLogRow } from '@/components/validation/ValidationLogRow';
import { isLegacyValidation } from '@/components/validation/validationShape';
import { DatasourceBadge } from '@/components/common/UIComponents';
import { markRead } from '@/lib/readState';
import { useFormat } from '@/lib/format';
import QuestionsDrawer from '@/components/common/QuestionsDrawer';
import { api, DiscoveryResult, DiscoveryQuestion, Feedback, Insight, Project, SearchResultItem, ExplorationStep, AnalysisLogStep, ValidationLogEntry } from '@/lib/api';

const severityColor: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

export default function InsightDetailPage() {
  const t = useTranslations('insightDetail');
  const f = useFormat();
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
  const [project, setProject] = useState<Project | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [loading, setLoading] = useState(true);
  const [similarInsights, setSimilarInsights] = useState<SearchResultItem[]>([]);
  // Pending clarifying questions the agent raised about this specific insight.
  const [questions, setQuestions] = useState<DiscoveryQuestion[]>([]);
  // Per-step / per-area / per-result logs are no longer embedded on the
  // discovery doc — fetch them from the dedicated split-log endpoints.
  const [explorationLog, setExplorationLog] = useState<ExplorationStep[]>([]);
  const [analysisLog, setAnalysisLog] = useState<AnalysisLogStep[]>([]);
  const [validationLog, setValidationLog] = useState<ValidationLogEntry[]>([]);
  // Technical details (SQL queries, exploration steps, token counts) are
  // opened explicitly via a button in the sidebar. The Drawer gets the full
  // viewport width, so code blocks don't have to squeeze into the sidebar.
  const [techOpen, setTechOpen] = useState(false);

  // refetchDiscovery is called by the validation router on terminal
  // job status so the embedded insight picks up the new verdict the
  // agent wrote back. Defined as a useCallback so the router's
  // useEffect dependency stays stable across renders.
  const refetchDiscovery = useCallback(async () => {
    const disc = await api.getDiscoveryById(runId);
    setDiscovery(disc);
    const found = (disc?.insights || []).find((i) => i.id === insightId) || null;
    setInsight(found);
  }, [runId, insightId]);

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
      api.getProject(id).then(setProject).catch(() => setProject(null)),
      api.listFeedback(runId).then((fb) => {
        const match = (fb || []).find((f) => f.target_type === 'insight' && f.target_id === insightId);
        if (match) setFeedback(match);
      }).catch(() => {}),
      api.listExplorationSteps(runId).then((s) => setExplorationLog(s || [])).catch(() => {}),
      api.listAnalysisSteps(runId).then((s) => setAnalysisLog(s || [])).catch(() => {}),
      api.listValidationResults(runId).then((s) => setValidationLog(s || [])).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [id, runId, insightId]);

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

  // Pending clarifying questions the agent raised about THIS insight. The list
  // endpoint is project-wide, so filter to the questions whose linked target is
  // this insight. Enterprise-backed; empty (404) on community builds.
  useEffect(() => {
    if (!insightId) return;
    api.listProjectQuestions(id, { status: 'pending' })
      .then((qs) => setQuestions((qs || []).filter(
        (qn) => qn.linked_target?.type === 'insight' && qn.linked_target?.id === insightId,
      )))
      .catch(() => setQuestions([]));
  }, [id, insightId]);

  if (loading) return <Shell><Loader /></Shell>;
  if (!insight) return <Shell><Text>{t('notFound')}</Text></Shell>;

  // Get the exploration steps this insight is based on (cited by the LLM)
  const sourceSteps = (insight.source_steps || [])
    .map((stepNum) => explorationLog.find((s) => s.step === stepNum))
    .filter(Boolean);

  // Datasource(s) this insight was derived from (multi-warehouse), taken
  // from the warehouse_ids of the exploration steps it cites. Empty on a
  // single-warehouse project (primary steps carry no distinct id), so the
  // header stays unchanged there.
  const insightDatasources = Array.from(
    new Set(
      sourceSteps
        .map((s) => s?.warehouse_id)
        .filter((w): w is string => !!w && w !== 'default'),
    ),
  );

  // Get the analysis step for this insight's area
  const analysisStep = analysisLog.find((a) => a.area_id === insight.analysis_area);

  // Validation log entries for this insight's area. The new-shape verdict
  // (verifier + refuter + per-claim breakdown) is reachable from the
  // sidebar ValidationPanel's "Show breakdown" drawer — duplicating it
  // in the technical-details drawer would just create two paths to the
  // same data. Legacy entries lack a per-claim breakdown, so for them
  // the technical-details accordion is still the place to see the
  // historical probes.
  const legacyValidationEntries = validationLog
    .filter((v) => v.analysis_area === insight.analysis_area)
    .filter((v) => isLegacyValidation(v));

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
      {/* Clarifying questions about this insight — collapsible right-edge drawer,
          renders nothing when there are none. */}
      <QuestionsDrawer
        projectId={id}
        questions={questions}
        onResolved={(qid) => setQuestions((prev) => prev.filter((qn) => qn.id !== qid))}
        title={t('questionsDrawerTitle')}
        storageKey="dbx-questions-drawer-insight"
        viewAllHref={`/projects/${id}/questions`}
      />

      <Button variant="subtle" onClick={goBack}
        leftSection={<IconArrowLeft size={16} />} size="sm" w="fit-content" mb="md">
        {t('back')}
      </Button>

      {/* Header — full width so title can breathe, no sidebar beside it. */}
      <div style={{ maxWidth: 800, marginBottom: 16 }}>
        <Group gap="sm" mb={4}>
          <IconAlertTriangle size={20}
            color={`var(--mantine-color-${severityColor[insight.severity] || 'gray'}-6)`} />
          <Title order={2}>{insight.name}</Title>
        </Group>
        <Group gap="xs">
          <Badge color={severityColor[insight.severity] || 'gray'} variant="light">
            {severityColor[insight.severity] ? t(`severity_${insight.severity}`) : insight.severity}
          </Badge>
          <Badge variant="outline">{insight.analysis_area}</Badge>
          {insight.affected_count > 0 && (
            <Badge variant="outline">{t('affectedBadge', { count: f.number(insight.affected_count) })}</Badge>
          )}
          {insightDatasources.map((ds) => (
            <Badge key={ds} color="blue" variant="light" title={t('datasourceTooltip')}>{ds}</Badge>
          ))}
          <FeedbackButtons projectId={id} discoveryId={runId} targetType="insight" targetId={insightId}
            feedback={feedback} onUpdate={setFeedback} />
          <BookmarkButton projectId={id} discoveryId={runId} targetType="insight" targetId={insightId} />
        </Group>
      </div>

      {/* Mobile chip strip — related + similar items collapsed into a
          horizontally-scrollable strip. Hidden once the right sidebar shows. */}
      <Box hiddenFrom="lg" mb="md">
        <RelatedChipStrip
          relatedLabel={t('relatedRecommendations')}
          related={relatedItems}
        />
      </Box>

      <Grid gutter="lg">
        <Grid.Col span={{ base: 12, lg: 9 }}>
      <Stack gap="lg" maw={800}>
        {/* Description — the narrative "what". Rendered as formatted Markdown
            when description_md is present. Plain/legacy descriptions (no
            description_md) are shown verbatim — NOT re-parsed as Markdown — so
            stray metacharacters in older runs can't be reinterpreted. */}
        <Card withBorder p="lg">
          {insight.description_md
            ? <Markdown>{insight.description_md}</Markdown>
            : insight.description
              ? <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{insight.description}</Text>
              : <Text size="sm" c="dimmed">{t('noDescription')}</Text>}
          {/* LLM-generated starter questions + "Ask about this" (enterprise;
              renders nothing on community builds or when the toggle is off). */}
          <SuggestedQuestions projectId={id} seed={{ type: 'insight', id: insight.id, title: insight.name }} />
        </Card>

        {/* Assessment — risk, confidence, target segment. Promoted above
            Indicators/Metrics so skimming readers see the decision-ready
            numbers right after the description. */}
        <Card withBorder p="lg">
          <Title order={4} mb="sm">{t('assessment')}</Title>
          <Group gap="xl">
            <div>
              <Text size="xs" c="dimmed">{t('riskScore')}</Text>
              <Text size="lg" fw={700} c={insight.risk_score > 0.7 ? 'red' : insight.risk_score > 0.4 ? 'orange' : 'green'}>
                {f.number(insight.risk_score, { style: 'percent', maximumFractionDigits: 0 })}
              </Text>
            </div>
            <div>
              <Text size="xs" c="dimmed">{t('confidence')}</Text>
              <Text size="lg" fw={700}>{f.number(insight.confidence, { style: 'percent', maximumFractionDigits: 0 })}</Text>
            </div>
            {insight.target_segment && (
              <div>
                <Text size="xs" c="dimmed">{t('targetSegment')}</Text>
                <Text size="sm">{insight.target_segment}</Text>
              </div>
            )}
          </Group>
        </Card>

        {/* Key Indicators — plain-language bullets supporting the claim. */}
        {insight.indicators && insight.indicators.length > 0 && (
          <Card withBorder p="lg">
            <Title order={4} mb="sm">{t('keyIndicators')}</Title>
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
            <Title order={4} mb="sm">{t('metrics')}</Title>
            <Table>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('metricColumn')}</Table.Th>
                  <Table.Th>{t('valueColumn')}</Table.Th>
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
            <ValidationRouter
              validation={insight.validation}
              discoveryId={runId}
              docKind="insight"
              docId={insight.id}
              validationEnabled={project?.validation_enabled !== false}
              projectSettingsHref={`/projects/${id}/settings#advanced`}
              onTerminal={refetchDiscovery}
            />
            <Button
              variant="subtle"
              size="sm"
              leftSection={<IconCode size={14} />}
              onClick={() => setTechOpen(true)}
              w="fit-content"
            >
              {t('showTechnicalDetails')}
            </Button>
          </Stack>
        </Box>

        {insight.discovered_at && (
          <Text size="xs" c="dimmed">{t('discoveredAt', { time: f.dateTime(insight.discovered_at, { dateStyle: 'medium', timeStyle: 'short' }) })}</Text>
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
                relatedLabel={t('relatedRecommendations')}
                related={relatedItems}
              />
              <ValidationRouter
                validation={insight.validation}
                discoveryId={runId}
                docKind="insight"
                docId={insight.id}
                validationEnabled={project?.validation_enabled !== false}
                projectSettingsHref={`/projects/${id}/settings#advanced`}
                onTerminal={refetchDiscovery}
              />
              <Button
                variant="subtle"
                size="sm"
                leftSection={<IconCode size={14} />}
                onClick={() => setTechOpen(true)}
                justify="flex-start"
                fullWidth
              >
                {t('showTechnicalDetails')}
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
            <Text fw={600}>{t('howFoundTitle')}</Text>
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
                  <Text size="sm" fw={600}>{t('sourceDataTitle', { count: sourceSteps.length })}</Text>
                  <Text size="xs" c="dimmed">{t('sourceDataHelp')}</Text>
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <Stack gap="sm">
                  {sourceSteps.map((step, idx) => step && (
                    <Card key={idx} withBorder p="sm" radius="sm">
                      <Group justify="space-between" mb={4}>
                        <Group gap={6} align="center">
                          <Text size="xs" fw={600}>{t('stepLabel', { step: step.step })}</Text>
                          <DatasourceBadge warehouseId={step.warehouse_id} />
                        </Group>
                        <Group gap="xs">
                          {step.row_count > 0 && <Badge size="xs" variant="outline">{t('rowsBadge', { count: f.number(step.row_count) })}</Badge>}
                          {step.execution_time_ms > 0 && <Badge size="xs" variant="outline">{t('msBadge', { ms: f.number(step.execution_time_ms) })}</Badge>}
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
                {t('sourceStepsUnavailable', {
                  hasSteps: insight.source_steps && insight.source_steps.length > 0 ? 'yes' : 'no',
                  steps: (insight.source_steps || []).join(', '),
                })}
              </Text>
            </Card>
          )}

          {/* Analysis step */}
          {analysisStep && (
            <Accordion.Item value="analysis">
              <Accordion.Control>
                <Group gap="xs">
                  <Text size="sm" fw={600}>{t('aiAnalysisTitle', { area: analysisStep.area_name })}</Text>
                  <Badge size="xs" variant="outline">{t('tokensBadge', { count: f.number(analysisStep.tokens_in + analysisStep.tokens_out) })}</Badge>
                  {analysisStep.duration_ms > 0 && (
                    <Badge size="xs" variant="outline">{t('secondsBadge', { seconds: f.number(analysisStep.duration_ms / 1000, { maximumFractionDigits: 1, minimumFractionDigits: 1 }) })}</Badge>
                  )}
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <Group gap="xl">
                  <div>
                    <Text size="xs" c="dimmed">{t('queriesFed')}</Text>
                    <Text size="sm" fw={600}>{f.number(analysisStep.relevant_queries)}</Text>
                  </div>
                  <div>
                    <Text size="xs" c="dimmed">{t('inputTokens')}</Text>
                    <Text size="sm" fw={600}>{f.number(analysisStep.tokens_in)}</Text>
                  </div>
                  <div>
                    <Text size="xs" c="dimmed">{t('outputTokens')}</Text>
                    <Text size="sm" fw={600}>{f.number(analysisStep.tokens_out)}</Text>
                  </div>
                </Group>
              </Accordion.Panel>
            </Accordion.Item>
          )}

          {/* Legacy validation log entries only — new-shape verdicts are
              rendered in the sidebar ValidationPanel + its "Show breakdown"
              drawer, so showing them here too would duplicate the surface. */}
          {legacyValidationEntries.length > 0 && (
            <Accordion.Item value="validation">
              <Accordion.Control>
                <Text size="sm" fw={600}>{t('validationLegacyTitle', { count: legacyValidationEntries.length })}</Text>
              </Accordion.Control>
              <Accordion.Panel>
                <Stack gap="sm">
                  {legacyValidationEntries.map((v, idx) => (
                    <ValidationLogRow key={idx} entry={v} />
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
          label={t('similarInsights')}
          items={similarInsights}
          hrefFor={(sim) => `/projects/${id}/discoveries/${sim.discovery_id}/insights/${sim.id}`}
        />
      </div>
    </Shell>
  );
}
