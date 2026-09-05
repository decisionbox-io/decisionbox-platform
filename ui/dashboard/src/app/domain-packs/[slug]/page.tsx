'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import dynamic from 'next/dynamic';
import { useTranslations } from 'next-intl';
import {
  ActionIcon, Alert, Button, Card, Group, Loader, Modal, Select, Stack, Switch,
  Tabs, Text, TextInput, Textarea, Title, Tooltip, NumberInput,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconAlertCircle, IconArrowLeft, IconCheck, IconPlus, IconTrash,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, DomainPack, PackAnalysisArea } from '@/lib/api';

const MDEditor = dynamic(() => import('@uiw/react-md-editor'), { ssr: false });

export default function DomainPackEditorPage() {
  const t = useTranslations('domainPackDetail');
  const params = useParams<{ slug: string }>();
  const slug = params?.slug;
  const isNew = slug === 'new';
  const router = useRouter();

  const [pack, setPack] = useState<DomainPack | null>(null);
  const [loading, setLoading] = useState(!isNew);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<string>('general');

  // Modals
  const [addCategoryOpen, setAddCategoryOpen] = useState(false);
  const [addAreaOpen, setAddAreaOpen] = useState(false);
  const [newCatId, setNewCatId] = useState('');
  const [newCatName, setNewCatName] = useState('');
  const [newCatDesc, setNewCatDesc] = useState('');
  const [newAreaTarget, setNewAreaTarget] = useState<string>('base');
  const [newAreaId, setNewAreaId] = useState('');
  const [newAreaName, setNewAreaName] = useState('');
  const [newAreaDesc, setNewAreaDesc] = useState('');
  const [newAreaKeywords, setNewAreaKeywords] = useState('');
  const [newAreaPriority, setNewAreaPriority] = useState<number>(1);

  useEffect(() => {
    if (isNew) {
      setPack({
        id: '', slug: '', name: '', description: '', version: '1.0.0',
        author: '', source_url: '', is_published: false,
        categories: [],
        prompts: {
          base: { base_context: '', exploration: '', recommendations: '' },
          categories: {},
        },
        analysis_areas: { base: [], categories: {} },
        profile_schema: { base: {}, categories: {} },
        created_at: '', updated_at: '',
      });
      return;
    }
    api.getDomainPack(slug)
      .then(setPack)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [slug, isNew]);

  const handleSave = async () => {
    if (!pack) return;
    setSaving(true);
    try {
      if (isNew) {
        const created = await api.createDomainPack(pack);
        notifications.show({ title: t('createdTitle'), message: t('createdMessage', { slug: created.slug }), color: 'green' });
        router.push(`/domain-packs/${created.slug}`);
      } else {
        await api.updateDomainPack(slug, pack);
        notifications.show({ title: t('savedTitle'), message: t('savedMessage'), color: 'green' });
      }
    } catch (e) {
      notifications.show({ title: t('errorTitle'), message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  // --- Category helpers ---
  const addCategory = () => {
    if (!pack || !newCatId) return;
    const catId = newCatId.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_]/g, '');
    setPack({
      ...pack,
      categories: [...pack.categories, { id: catId, name: newCatName || catId, description: newCatDesc }],
      prompts: { ...pack.prompts, categories: { ...pack.prompts.categories, [catId]: {} } },
      analysis_areas: { ...pack.analysis_areas, categories: { ...pack.analysis_areas.categories, [catId]: [] } },
    });
    setNewCatId(''); setNewCatName(''); setNewCatDesc('');
    setAddCategoryOpen(false);
  };

  const removeCategory = (catId: string) => {
    if (!pack) return;
    const newCats = { ...pack.prompts.categories };
    delete newCats[catId];
    const newAreaCats = { ...pack.analysis_areas.categories };
    delete newAreaCats[catId];
    setPack({
      ...pack,
      categories: pack.categories.filter(c => c.id !== catId),
      prompts: { ...pack.prompts, categories: newCats },
      analysis_areas: { ...pack.analysis_areas, categories: newAreaCats },
    });
  };

  // --- Analysis area helpers ---
  const addArea = () => {
    if (!pack || !newAreaId || !newAreaName) return;
    const areaId = newAreaId.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_]/g, '');
    const area: PackAnalysisArea = {
      id: areaId, name: newAreaName, description: newAreaDesc,
      keywords: newAreaKeywords.split(',').map(k => k.trim()).filter(Boolean),
      priority: newAreaPriority,
      prompt: `# ${newAreaName} Analysis\n\nAnalyze the data for {{DATASET}}.\n\nTotal queries: {{TOTAL_QUERIES}}\n\n## Query Results\n\n{{QUERY_RESULTS}}`,
    };

    if (newAreaTarget === 'base') {
      setPack({ ...pack, analysis_areas: { ...pack.analysis_areas, base: [...pack.analysis_areas.base, area] } });
    } else {
      const catAreas = pack.analysis_areas.categories[newAreaTarget] || [];
      setPack({
        ...pack,
        analysis_areas: {
          ...pack.analysis_areas,
          categories: { ...pack.analysis_areas.categories, [newAreaTarget]: [...catAreas, area] },
        },
      });
    }
    setNewAreaId(''); setNewAreaName(''); setNewAreaDesc(''); setNewAreaKeywords(''); setNewAreaPriority(1);
    setAddAreaOpen(false);
  };

  const updateBaseArea = (idx: number, updates: Partial<PackAnalysisArea>) => {
    if (!pack) return;
    const areas = [...pack.analysis_areas.base];
    areas[idx] = { ...areas[idx], ...updates };
    setPack({ ...pack, analysis_areas: { ...pack.analysis_areas, base: areas } });
  };

  const removeBaseArea = (idx: number) => {
    if (!pack) return;
    setPack({ ...pack, analysis_areas: { ...pack.analysis_areas, base: pack.analysis_areas.base.filter((_, i) => i !== idx) } });
  };

  const updateCatArea = (catId: string, idx: number, updates: Partial<PackAnalysisArea>) => {
    if (!pack) return;
    const areas = [...(pack.analysis_areas.categories[catId] || [])];
    areas[idx] = { ...areas[idx], ...updates };
    setPack({
      ...pack,
      analysis_areas: { ...pack.analysis_areas, categories: { ...pack.analysis_areas.categories, [catId]: areas } },
    });
  };

  const removeCatArea = (catId: string, idx: number) => {
    if (!pack) return;
    const areas = (pack.analysis_areas.categories[catId] || []).filter((_, i) => i !== idx);
    setPack({
      ...pack,
      analysis_areas: { ...pack.analysis_areas, categories: { ...pack.analysis_areas.categories, [catId]: areas } },
    });
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!pack) return <Shell><Text>{t('notFound')}</Text></Shell>;

  return (
    <Shell breadcrumb={[{ label: t('breadcrumbPlaybooks'), href: '/domain-packs' }, { label: isNew ? t('breadcrumbNew') : pack.name }]}>
      <Stack gap="lg">
        <Group justify="space-between">
          <Group>
            <Button variant="subtle" leftSection={<IconArrowLeft size={16} />}
              onClick={() => router.push('/domain-packs')}>{t('back')}</Button>
            <Title order={2}>{isNew ? t('newPlaybook') : pack.name}</Title>
          </Group>
          <Button onClick={handleSave} loading={saving} leftSection={<IconCheck size={16} />}>
            {isNew ? t('create') : t('save')}
          </Button>
        </Group>

        <Tabs value={activeTab} onChange={(v) => setActiveTab(v || 'general')}>
          <Tabs.List>
            <Tabs.Tab value="general">{t('tabGeneral')}</Tabs.Tab>
            <Tabs.Tab value="categories">{t('tabCategories', { count: pack.categories.length })}</Tabs.Tab>
            <Tabs.Tab value="prompts">{t('tabPrompts')}</Tabs.Tab>
            <Tabs.Tab value="areas">{t('tabAreas', { count: pack.analysis_areas.base.length + Object.values(pack.analysis_areas.categories).reduce((n, a) => n + a.length, 0) })}</Tabs.Tab>
            <Tabs.Tab value="schema">{t('tabSchema')}</Tabs.Tab>
          </Tabs.List>

          {/* General */}
          <Tabs.Panel value="general" pt="md">
            <Card withBorder p="lg">
              <Stack gap="md">
                <TextInput label={t('slugLabel')} description={t('slugDesc')} required
                  value={pack.slug} onChange={(e) => setPack({ ...pack, slug: e.target.value })}
                  disabled={!isNew} />
                <TextInput label={t('nameLabel')} required
                  value={pack.name} onChange={(e) => setPack({ ...pack, name: e.target.value })} />
                <Textarea label={t('descriptionLabel')} autosize minRows={2}
                  value={pack.description} onChange={(e) => setPack({ ...pack, description: e.currentTarget.value })} />
                <TextInput label={t('versionLabel')}
                  value={pack.version} onChange={(e) => setPack({ ...pack, version: e.target.value })} />
                <Switch label={t('publishedLabel')} description={t('publishedDesc')}
                  checked={pack.is_published}
                  onChange={(e) => setPack({ ...pack, is_published: e.currentTarget.checked })} />
              </Stack>
            </Card>
          </Tabs.Panel>

          {/* Categories */}
          <Tabs.Panel value="categories" pt="md">
            <Stack gap="md">
              <Group justify="flex-end">
                <Button variant="light" leftSection={<IconPlus size={16} />} onClick={() => setAddCategoryOpen(true)}>
                  {t('addCategory')}
                </Button>
              </Group>
              {pack.categories.length === 0 && (
                <Text c="dimmed" ta="center" py="xl">{t('noCategories')}</Text>
              )}
              {pack.categories.map((cat, i) => (
                <Card key={cat.id} withBorder p="md">
                  <Group justify="space-between" mb="sm">
                    <Text fw={600}>{cat.name} <Text span c="dimmed" size="sm">({cat.id})</Text></Text>
                    <Tooltip label={t('removeCategoryTooltip')}>
                      <ActionIcon color="red" variant="light" onClick={() => removeCategory(cat.id)}>
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Tooltip>
                  </Group>
                  <TextInput label={t('nameLabel')} value={cat.name} mb="xs"
                    onChange={(e) => {
                      const cats = [...pack.categories];
                      cats[i] = { ...cats[i], name: e.target.value };
                      setPack({ ...pack, categories: cats });
                    }} />
                  <TextInput label={t('descriptionLabel')} value={cat.description}
                    onChange={(e) => {
                      const cats = [...pack.categories];
                      cats[i] = { ...cats[i], description: e.target.value };
                      setPack({ ...pack, categories: cats });
                    }} />
                </Card>
              ))}
            </Stack>
          </Tabs.Panel>

          {/* Prompts */}
          <Tabs.Panel value="prompts" pt="md">
            <Stack gap="lg">
              <Card withBorder p="lg">
                <Title order={4} mb="xs">{t('baseContextTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('baseContextHelp', { profile: '{{PROFILE}}', previousContext: '{{PREVIOUS_CONTEXT}}' })}
                </Text>
                <MDEditor value={pack.prompts.base.base_context} height={300} preview="edit"
                  onChange={(val) => setPack({ ...pack, prompts: { ...pack.prompts, base: { ...pack.prompts.base, base_context: val || '' } } })} />
              </Card>

              <Card withBorder p="lg">
                <Title order={4} mb="xs">{t('explorationTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('explorationHelp', { dataset: '{{DATASET}}', schemaInfo: '{{SCHEMA_INFO}}', filter: '{{FILTER}}', filterContext: '{{FILTER_CONTEXT}}', filterRule: '{{FILTER_RULE}}', analysisAreas: '{{ANALYSIS_AREAS}}' })}
                </Text>
                <MDEditor value={pack.prompts.base.exploration} height={400} preview="edit"
                  onChange={(val) => setPack({ ...pack, prompts: { ...pack.prompts, base: { ...pack.prompts.base, exploration: val || '' } } })} />
              </Card>

              <Card withBorder p="lg">
                <Title order={4} mb="xs">{t('recommendationsTitle')}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('recommendationsHelp', { discoveryDate: '{{DISCOVERY_DATE}}', insightsSummary: '{{INSIGHTS_SUMMARY}}', insightsData: '{{INSIGHTS_DATA}}' })}
                </Text>
                <MDEditor value={pack.prompts.base.recommendations} height={400} preview="edit"
                  onChange={(val) => setPack({ ...pack, prompts: { ...pack.prompts, base: { ...pack.prompts.base, recommendations: val || '' } } })} />
              </Card>

              {/* Category exploration contexts */}
              {pack.categories.map(cat => (
                <Card key={cat.id} withBorder p="lg">
                  <Title order={4} mb="xs">{t('categoryExplorationTitle', { name: cat.name })}</Title>
                  <Text size="xs" c="dimmed" mb="md">
                    {t('categoryExplorationHelp')}
                  </Text>
                  <MDEditor
                    value={pack.prompts.categories[cat.id]?.exploration_context || ''}
                    height={250} preview="edit"
                    onChange={(val) => setPack({
                      ...pack,
                      prompts: {
                        ...pack.prompts,
                        categories: {
                          ...pack.prompts.categories,
                          [cat.id]: { ...pack.prompts.categories[cat.id], exploration_context: val || '' },
                        },
                      },
                    })} />
                </Card>
              ))}
            </Stack>
          </Tabs.Panel>

          {/* Analysis Areas */}
          <Tabs.Panel value="areas" pt="md">
            <Stack gap="lg">
              <Group justify="space-between">
                <Title order={4}>{t('baseAnalysisAreas')}</Title>
                <Button variant="light" size="sm" leftSection={<IconPlus size={14} />}
                  onClick={() => { setNewAreaTarget('base'); setAddAreaOpen(true); }}>
                  {t('addArea')}
                </Button>
              </Group>
              {pack.analysis_areas.base.map((area, idx) => (
                <AreaEditor key={area.id} area={area}
                  onUpdate={(u) => updateBaseArea(idx, u)}
                  onRemove={() => removeBaseArea(idx)} />
              ))}

              {pack.categories.map(cat => (
                <div key={cat.id}>
                  <Group justify="space-between" mt="lg" mb="sm">
                    <Title order={4}>{t('categoryAreasTitle', { name: cat.name })}</Title>
                    <Button variant="light" size="sm" leftSection={<IconPlus size={14} />}
                      onClick={() => { setNewAreaTarget(cat.id); setAddAreaOpen(true); }}>
                      {t('addArea')}
                    </Button>
                  </Group>
                  {(pack.analysis_areas.categories[cat.id] || []).length === 0 && (
                    <Text c="dimmed" size="sm">{t('noCategoryAreas')}</Text>
                  )}
                  {(pack.analysis_areas.categories[cat.id] || []).map((area, idx) => (
                    <AreaEditor key={area.id} area={area}
                      onUpdate={(u) => updateCatArea(cat.id, idx, u)}
                      onRemove={() => removeCatArea(cat.id, idx)} />
                  ))}
                </div>
              ))}
            </Stack>
          </Tabs.Panel>

          {/* Profile Schema */}
          <Tabs.Panel value="schema" pt="md">
            <Card withBorder p="lg">
              <Title order={4} mb="xs">{t('baseProfileSchema')}</Title>
              <Text size="xs" c="dimmed" mb="md">
                {t('baseProfileSchemaHelp')}
              </Text>
              <Textarea
                value={JSON.stringify(pack.profile_schema.base, null, 2)}
                onChange={(e) => {
                  try {
                    const parsed = JSON.parse(e.currentTarget.value);
                    setPack({ ...pack, profile_schema: { ...pack.profile_schema, base: parsed } });
                  } catch { /* ignore parse errors while typing */ }
                }}
                minRows={15} autosize
                styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }}
              />
            </Card>

            {pack.categories.map(cat => (
              <Card key={cat.id} withBorder p="lg" mt="md">
                <Title order={4} mb="xs">{t('categorySchemaTitle', { name: cat.name })}</Title>
                <Text size="xs" c="dimmed" mb="md">
                  {t('categorySchemaHelp')}
                </Text>
                <Textarea
                  value={JSON.stringify(pack.profile_schema.categories[cat.id] || {}, null, 2)}
                  onChange={(e) => {
                    try {
                      const parsed = JSON.parse(e.currentTarget.value);
                      setPack({
                        ...pack,
                        profile_schema: {
                          ...pack.profile_schema,
                          categories: { ...pack.profile_schema.categories, [cat.id]: parsed },
                        },
                      });
                    } catch { /* ignore parse errors while typing */ }
                  }}
                  minRows={10} autosize
                  styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }}
                />
              </Card>
            ))}
          </Tabs.Panel>
        </Tabs>
      </Stack>

      {/* Add Category Modal */}
      <Modal opened={addCategoryOpen} onClose={() => setAddCategoryOpen(false)} title={t('addCategory')}>
        <Stack>
          <TextInput label={t('categoryIdLabel')} description={t('categoryIdDesc')} required
            placeholder={t('categoryIdPlaceholder')} value={newCatId} onChange={(e) => setNewCatId(e.target.value)} />
          <TextInput label={t('displayNameLabel')} required placeholder={t('categoryNamePlaceholder')}
            value={newCatName} onChange={(e) => setNewCatName(e.target.value)} />
          <TextInput label={t('descriptionLabel')} placeholder={t('categoryDescPlaceholder')}
            value={newCatDesc} onChange={(e) => setNewCatDesc(e.target.value)} />
          <Button onClick={addCategory} disabled={!newCatId || !newCatName}>{t('addCategory')}</Button>
        </Stack>
      </Modal>

      {/* Add Analysis Area Modal */}
      <Modal opened={addAreaOpen} onClose={() => setAddAreaOpen(false)} title={t('addAnalysisArea')}>
        <Stack>
          <Select label={t('targetLabel')} data={[
            { value: 'base', label: t('targetBaseOption') },
            ...pack.categories.map(c => ({ value: c.id, label: c.name })),
          ]} value={newAreaTarget} onChange={(v) => setNewAreaTarget(v || 'base')} />
          <TextInput label={t('areaIdLabel')} description={t('areaIdDesc')} required
            placeholder={t('areaIdPlaceholder')} value={newAreaId} onChange={(e) => setNewAreaId(e.target.value)} />
          <TextInput label={t('displayNameLabel')} required placeholder={t('areaNamePlaceholder')}
            value={newAreaName} onChange={(e) => setNewAreaName(e.target.value)} />
          <TextInput label={t('descriptionLabel')} placeholder={t('areaDescPlaceholder')}
            value={newAreaDesc} onChange={(e) => setNewAreaDesc(e.target.value)} />
          <TextInput label={t('keywordsLabel')} description={t('keywordsDesc')} placeholder={t('areaKeywordsPlaceholder')}
            value={newAreaKeywords} onChange={(e) => setNewAreaKeywords(e.target.value)} />
          <NumberInput label={t('priorityLabel')} description={t('priorityDesc')} min={1}
            value={newAreaPriority} onChange={(v) => setNewAreaPriority(typeof v === 'number' ? v : 1)} />
          <Button onClick={addArea} disabled={!newAreaId || !newAreaName}>{t('addArea')}</Button>
        </Stack>
      </Modal>
    </Shell>
  );
}

/* --- Analysis Area Editor Component --- */

function AreaEditor({ area, onUpdate, onRemove }: {
  area: PackAnalysisArea;
  onUpdate: (updates: Partial<PackAnalysisArea>) => void;
  onRemove: () => void;
}) {
  const t = useTranslations('domainPackDetail');
  const [expanded, setExpanded] = useState(false);

  return (
    <Card withBorder p="md" mb="sm">
      <Group justify="space-between" mb={expanded ? 'sm' : 0}>
        <Group gap="sm" style={{ cursor: 'pointer' }} onClick={() => setExpanded(!expanded)}>
          <Text fw={600} size="sm">{area.name}</Text>
          <Text size="xs" c="dimmed">({area.id}) P{area.priority}</Text>
          <Text size="xs" c="dimmed">{expanded ? '▲' : '▼'}</Text>
        </Group>
        <Tooltip label={t('removeAreaTooltip')}>
          <ActionIcon color="red" variant="light" size="sm" onClick={onRemove}>
            <IconTrash size={14} />
          </ActionIcon>
        </Tooltip>
      </Group>

      {expanded && (
        <Stack gap="sm" mt="sm">
          <TextInput label={t('nameLabel')} value={area.name} size="sm"
            onChange={(e) => onUpdate({ name: e.target.value })} />
          <TextInput label={t('descriptionLabel')} value={area.description} size="sm"
            onChange={(e) => onUpdate({ description: e.target.value })} />
          <TextInput label={t('keywordsLabel')} value={area.keywords.join(', ')} size="sm"
            description={t('areaKeywordsDesc')}
            onChange={(e) => onUpdate({ keywords: e.target.value.split(',').map(k => k.trim()).filter(Boolean) })} />
          <NumberInput label={t('priorityLabel')} value={area.priority} min={1} size="sm"
            onChange={(v) => onUpdate({ priority: typeof v === 'number' ? v : 1 })} />
          <Text size="sm" fw={600} mt="xs">{t('analysisPromptTitle')}</Text>
          <Text size="xs" c="dimmed">
            {t('analysisPromptHelp', { dataset: '{{DATASET}}', queryResults: '{{QUERY_RESULTS}}', totalQueries: '{{TOTAL_QUERIES}}' })}
          </Text>
          <MDEditor value={area.prompt} height={300} preview="edit"
            onChange={(val) => onUpdate({ prompt: val || '' })} />
        </Stack>
      )}
    </Card>
  );
}
