'use client';

import { CSSProperties, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
import dynamic from 'next/dynamic';
import {
  ActionIcon, Alert, Badge, Button, Card, Divider, Group, Loader, Modal, NavLink, ScrollArea, Stack, Switch,
  Text, TextInput, Title, Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconArrowLeft, IconCheck, IconPlus, IconTrash } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, ProjectPrompts, AnalysisAreaConfig } from '@/lib/api';

// Dynamic import to avoid SSR issues with the markdown editor
const MDEditor = dynamic(() => import('@uiw/react-md-editor'), { ssr: false });

// The detail card is a flex column that fills the master–detail row's height so
// the editor stretches down instead of leaving dead space; editorFillStyle lets
// the MDEditor (height="100%") fill the remaining space under the header/inputs.
const detailCardStyle: CSSProperties = { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' };
const editorFillStyle: CSSProperties = { flex: 1, minHeight: 0 };

export default function PromptsPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const t = useTranslations('promptsPage');
  const [prompts, setPrompts] = useState<ProjectPrompts | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<string>('base_context');
  const [addModalOpen, setAddModalOpen] = useState(false);

  // New area form
  const [newAreaId, setNewAreaId] = useState('');
  const [newAreaName, setNewAreaName] = useState('');
  const [newAreaDesc, setNewAreaDesc] = useState('');
  const [newAreaKeywords, setNewAreaKeywords] = useState('');

  useEffect(() => {
    api.getProject(id)
      .then(() => api.getPrompts(id))
      .then(setPrompts)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [id]);

  const handleSave = async () => {
    if (!prompts) return;
    setSaving(true);
    try {
      await api.updatePrompts(id, prompts);
      notifications.show({ title: t('savedTitle'), message: t('savedMessage'), color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: t('errorTitle'), message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  const updateArea = (areaId: string, updates: Partial<AnalysisAreaConfig>) => {
    if (!prompts) return;
    setPrompts({
      ...prompts,
      analysis_areas: {
        ...prompts.analysis_areas,
        [areaId]: { ...prompts.analysis_areas[areaId], ...updates },
      },
    });
  };

  const addCustomArea = () => {
    if (!prompts || !newAreaId || !newAreaName) return;

    const areaId = newAreaId.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_]/g, '');

    setPrompts({
      ...prompts,
      analysis_areas: {
        ...prompts.analysis_areas,
        [areaId]: {
          name: newAreaName,
          description: newAreaDesc,
          keywords: newAreaKeywords.split(',').map((k) => k.trim()).filter(Boolean),
          prompt: `# ${newAreaName} Analysis\n\nAnalyze the query results and identify insights related to ${newAreaName.toLowerCase()}.\n\n## Required Output Format\n\nRespond with ONLY valid JSON:\n\n\`\`\`json\n{\n  "insights": [\n    {\n      "name": "...",\n      "description": "...",\n      "severity": "high",\n      "affected_count": 0,\n      "risk_score": 0.0,\n      "confidence": 0.0,\n      "indicators": []\n    }\n  ]\n}\n\`\`\`\n\n## Query Results\n\n{{QUERY_RESULTS}}`,
          is_base: false,
          is_custom: true,
          priority: Object.keys(prompts.analysis_areas).length + 1,
          enabled: true,
        },
      },
    });

    setNewAreaId('');
    setNewAreaName('');
    setNewAreaDesc('');
    setNewAreaKeywords('');
    setAddModalOpen(false);
    setActiveTab(areaId);
  };

  const removeArea = (areaId: string) => {
    if (!prompts) return;
    setPrompts({
      ...prompts,
      analysis_areas: Object.fromEntries(
        Object.entries(prompts.analysis_areas).filter(([key]) => key !== areaId),
      ),
    });
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!prompts) return <Shell><Text>{t('notFound')}</Text></Shell>;

  const areas = Object.entries(prompts.analysis_areas)
    .sort(([, a], [, b]) => a.priority - b.priority);

  return (
    <Shell fullWidth>
      <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - var(--db-topbar-height))', margin: '-24px', padding: 24, overflow: 'hidden' }}>
        <Group justify="space-between" mb="md" style={{ flexShrink: 0 }}>
          <Group>
            <Button variant="subtle" leftSection={<IconArrowLeft size={16} />}
              onClick={() => router.push(`/projects/${id}`)}>{t('back')}</Button>
            <Title order={2}>{t('title')}</Title>
          </Group>
          <Group>
            <Button variant="light" leftSection={<IconPlus size={16} />}
              onClick={() => setAddModalOpen(true)}>{t('addAnalysisArea')}</Button>
            <Button onClick={handleSave} loading={saving} leftSection={<IconCheck size={16} />}>
              {t('saveAll')}
            </Button>
          </Group>
        </Group>

        {/* Master–detail: sections + analysis areas on the left, the selected
            prompt's editor on the right — fills the remaining height. */}
        <Group align="stretch" gap="lg" wrap="nowrap" style={{ flex: 1, minHeight: 0 }}>
          {/* LEFT — master list, a distinct bordered sidebar. The Discovery
              items + the "Analysis areas" header stay fixed; only the areas
              scroll (ScrollArea shows a scrollbar so it's clearly scrollable). */}
          <Card withBorder p={0} style={{ width: 260, flexShrink: 0, display: 'flex', flexDirection: 'column' }}>
            <Stack gap={2} p="xs" style={{ flexShrink: 0 }}>
              <Text size="xs" fw={700} c="dimmed" tt="uppercase" mb={2} px={6}>{t('discovery')}</Text>
              <NavLink label={t('baseContextNav')} active={activeTab === 'base_context'}
                onClick={() => setActiveTab('base_context')} />
              <NavLink label={t('explorationNav')} active={activeTab === 'exploration'}
                onClick={() => setActiveTab('exploration')} />
              <NavLink label={t('recommendationsNav')} active={activeTab === 'recommendations'}
                onClick={() => setActiveTab('recommendations')} />
            </Stack>

            <Divider />

            <Group justify="space-between" align="center" px="sm" py={8} style={{ flexShrink: 0 }}>
              <Group gap={6}>
                <Text size="xs" fw={700} c="dimmed" tt="uppercase">{t('analysisAreas')}</Text>
                <Badge size="xs" variant="light" color="gray">{areas.length}</Badge>
              </Group>
              <Tooltip label={t('addAnalysisAreaTooltip')}>
                <ActionIcon size="sm" variant="subtle" onClick={() => setAddModalOpen(true)}>
                  <IconPlus size={14} />
                </ActionIcon>
              </Tooltip>
            </Group>

            <Divider />

            <ScrollArea style={{ flex: 1, minHeight: 0 }} type="auto">
              <Stack gap={2} p="xs">
                {areas.map(([areaId, area]) => (
                  <NavLink key={areaId} label={area.name} active={activeTab === areaId}
                    onClick={() => setActiveTab(areaId)}
                    rightSection={
                      <Group gap={4} wrap="nowrap">
                        {!area.enabled && <Badge size="xs" color="gray">{t('badgeOff')}</Badge>}
                        {area.is_custom && <Badge size="xs" color="violet">{t('badgeCustom')}</Badge>}
                      </Group>
                    } />
                ))}
                {areas.length === 0 && (
                  <Text size="xs" c="dimmed" px="xs" py={4}>{t('noAreasYet')}</Text>
                )}
              </Stack>
            </ScrollArea>
          </Card>

          {/* RIGHT — detail for the selected item, fills width + height */}
          <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
            {activeTab === 'base_context' && (
              <Card withBorder p="lg" style={detailCardStyle}>
                <Title order={4} mb="sm">{t('baseContextTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('baseContextHelp', { profile: '{{PROFILE}}', previousContext: '{{PREVIOUS_CONTEXT}}' })}
                </Text>
                <div style={editorFillStyle}>
                  <MDEditor
                    value={prompts.base_context}
                    onChange={(val) => setPrompts({ ...prompts, base_context: val || '' })}
                    height="100%"
                    preview="edit"
                  />
                </div>
              </Card>
            )}

            {activeTab === 'exploration' && (
              <Card withBorder p="lg" style={detailCardStyle}>
                <Title order={4} mb="sm">{t('explorationTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('explorationHelp')}
                </Text>
                <div style={editorFillStyle}>
                  <MDEditor
                    value={prompts.exploration}
                    onChange={(val) => setPrompts({ ...prompts, exploration: val || '' })}
                    height="100%"
                    preview="edit"
                  />
                </div>
              </Card>
            )}

            {activeTab === 'recommendations' && (
              <Card withBorder p="lg" style={detailCardStyle}>
                <Title order={4} mb="sm">{t('recommendationsTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('recommendationsHelp')}
                </Text>
                <div style={editorFillStyle}>
                  <MDEditor
                    value={prompts.recommendations}
                    onChange={(val) => setPrompts({ ...prompts, recommendations: val || '' })}
                    height="100%"
                    preview="edit"
                  />
                </div>
              </Card>
            )}

            {areas.map(([areaId, area]) => activeTab === areaId && (
              <Card withBorder p="lg" key={areaId} style={detailCardStyle}>
                <Group justify="space-between" mb="md">
                  <div>
                    <Title order={4}>{area.name}</Title>
                    <Text size="xs" c="dimmed">{area.description}</Text>
                  </div>
                  <Group>
                    <Switch label={t('enabled')} checked={area.enabled}
                      onChange={(e) => updateArea(areaId, { enabled: e.currentTarget.checked })} />
                    {area.is_custom && (
                      <Tooltip label={t('removeCustomArea')}>
                        <ActionIcon color="red" variant="light"
                          onClick={() => { removeArea(areaId); setActiveTab('base_context'); }}>
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Group>
                </Group>

                <Stack gap="sm" mb="md">
                  <TextInput label={t('areaNameLabel')} value={area.name}
                    onChange={(e) => updateArea(areaId, { name: e.target.value })} />
                  <TextInput label={t('descriptionLabel')} value={area.description}
                    onChange={(e) => updateArea(areaId, { description: e.target.value })} />
                  <TextInput label={t('keywordsLabel')} description={t('keywordsDescription')}
                    value={area.keywords.join(', ')}
                    onChange={(e) => updateArea(areaId, {
                      keywords: e.target.value.split(',').map((k) => k.trim()).filter(Boolean),
                    })} />
                </Stack>

                <Text size="sm" fw={600} mb="xs">{t('analysisPromptLabel')}</Text>
                <Text size="xs" c="dimmed" mb="sm">
                  {t('analysisPromptHelp', { dataset: '{{DATASET}}', queryResults: '{{QUERY_RESULTS}}', totalQueries: '{{TOTAL_QUERIES}}' })}
                </Text>
                <div style={editorFillStyle}>
                  <MDEditor
                    value={area.prompt}
                    onChange={(val) => updateArea(areaId, { prompt: val || '' })}
                    height="100%"
                    preview="edit"
                  />
                </div>
              </Card>
            ))}
          </div>
        </Group>
      </div>

      {/* Add Custom Area Modal */}
      <Modal opened={addModalOpen} onClose={() => setAddModalOpen(false)} title={t('modalTitle')}>
        <Stack>
          <TextInput label={t('areaIdLabel')} description={t('areaIdDescription')}
            placeholder={t('areaIdPlaceholder')} value={newAreaId}
            onChange={(e) => setNewAreaId(e.target.value)} required />
          <TextInput label={t('displayNameLabel')} placeholder={t('displayNamePlaceholder')}
            value={newAreaName} onChange={(e) => setNewAreaName(e.target.value)} required />
          <TextInput label={t('descriptionLabel')} placeholder={t('modalDescriptionPlaceholder')}
            value={newAreaDesc} onChange={(e) => setNewAreaDesc(e.target.value)} />
          <TextInput label={t('keywordsLabel')} description={t('modalKeywordsDescription')}
            placeholder={t('modalKeywordsPlaceholder')}
            value={newAreaKeywords} onChange={(e) => setNewAreaKeywords(e.target.value)} />
          <Button onClick={addCustomArea} disabled={!newAreaId || !newAreaName}>
            {t('addArea')}
          </Button>
        </Stack>
      </Modal>
    </Shell>
  );
}
