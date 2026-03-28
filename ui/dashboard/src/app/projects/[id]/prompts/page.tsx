'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import dynamic from 'next/dynamic';
import {
  ActionIcon, Alert, Badge, Button, Card, Group, Loader, Modal, Stack, Switch,
  Tabs, Text, TextInput, Title, Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconArrowLeft, IconCheck, IconPlus, IconTrash } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, ProjectPrompts, AnalysisAreaConfig } from '@/lib/api';

// Dynamic import to avoid SSR issues with the markdown editor
const MDEditor = dynamic(() => import('@uiw/react-md-editor'), { ssr: false });

export default function PromptsPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
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
      notifications.show({ title: 'Saved', message: 'Prompts updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
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
  if (!prompts) return <Shell><Text>Prompts not found</Text></Shell>;

  const areas = Object.entries(prompts.analysis_areas)
    .sort(([, a], [, b]) => a.priority - b.priority);

  return (
    <Shell>
      <Stack gap="lg">
        <Group justify="space-between">
          <Group>
            <Button variant="subtle" leftSection={<IconArrowLeft size={16} />}
              onClick={() => router.push(`/projects/${id}`)}>Back</Button>
            <Title order={2}>Prompt Editor</Title>
          </Group>
          <Group>
            <Button variant="light" leftSection={<IconPlus size={16} />}
              onClick={() => setAddModalOpen(true)}>Add Analysis Area</Button>
            <Button onClick={handleSave} loading={saving} leftSection={<IconCheck size={16} />}>
              Save All
            </Button>
          </Group>
        </Group>

        <Tabs value={activeTab} onChange={(v) => setActiveTab(v || 'exploration')}>
          <Tabs.List>
            <Tabs.Tab value="base_context">Base Context</Tabs.Tab>
            <Tabs.Tab value="exploration">Exploration</Tabs.Tab>
            <Tabs.Tab value="recommendations">Recommendations</Tabs.Tab>
            {areas.map(([areaId, area]) => (
              <Tabs.Tab key={areaId} value={areaId}>
                <Group gap={4}>
                  {area.name}
                  {!area.enabled && <Badge size="xs" color="gray">disabled</Badge>}
                  {area.is_custom && <Badge size="xs" color="violet">custom</Badge>}
                </Group>
              </Tabs.Tab>
            ))}
          </Tabs.List>

          {/* Base Context */}
          <Tabs.Panel value="base_context" pt="md">
            <Card withBorder p="lg">
              <Title order={4} mb="sm">Base Context (Shared)</Title>
              <Text size="xs" c="dimmed" mb="md">
                This context is prepended to ALL prompts — exploration, analysis, and recommendations.
                Use it for shared instructions like project profile and previous discovery context.
                Placeholders: {'{{PROFILE}}'}, {'{{PREVIOUS_CONTEXT}}'}
              </Text>
              <MDEditor
                value={prompts.base_context}
                onChange={(val) => setPrompts({ ...prompts, base_context: val || '' })}
                height={400}
                preview="edit"
              />
            </Card>
          </Tabs.Panel>

          {/* Exploration Prompt */}
          <Tabs.Panel value="exploration" pt="md">
            <Card withBorder p="lg">
              <Title order={4} mb="sm">Exploration System Prompt</Title>
              <Text size="xs" c="dimmed" mb="md">
                This prompt guides the AI agent during autonomous data exploration.
                It tells the agent what to look for, how to write queries, and what rules to follow.
                Base context is automatically prepended.
              </Text>
              <MDEditor
                value={prompts.exploration}
                onChange={(val) => setPrompts({ ...prompts, exploration: val || '' })}
                height={500}
                preview="edit"
              />
            </Card>
          </Tabs.Panel>

          {/* Recommendations Prompt */}
          <Tabs.Panel value="recommendations" pt="md">
            <Card withBorder p="lg">
              <Title order={4} mb="sm">Recommendations Prompt</Title>
              <Text size="xs" c="dimmed" mb="md">
                This prompt generates actionable recommendations from discovered insights.
              </Text>
              <MDEditor
                value={prompts.recommendations}
                onChange={(val) => setPrompts({ ...prompts, recommendations: val || '' })}
                height={500}
                preview="edit"
              />
            </Card>
          </Tabs.Panel>

          {/* Analysis Area Prompts */}
          {areas.map(([areaId, area]) => (
            <Tabs.Panel key={areaId} value={areaId} pt="md">
              <Card withBorder p="lg">
                <Group justify="space-between" mb="md">
                  <div>
                    <Title order={4}>{area.name}</Title>
                    <Text size="xs" c="dimmed">{area.description}</Text>
                  </div>
                  <Group>
                    <Switch label="Enabled" checked={area.enabled}
                      onChange={(e) => updateArea(areaId, { enabled: e.currentTarget.checked })} />
                    {area.is_custom && (
                      <Tooltip label="Remove custom area">
                        <ActionIcon color="red" variant="light"
                          onClick={() => removeArea(areaId)}>
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Group>
                </Group>

                <Stack gap="sm" mb="md">
                  <TextInput label="Area Name" value={area.name}
                    onChange={(e) => updateArea(areaId, { name: e.target.value })} />
                  <TextInput label="Description" value={area.description}
                    onChange={(e) => updateArea(areaId, { description: e.target.value })} />
                  <TextInput label="Keywords" description="Comma-separated keywords to filter exploration queries"
                    value={area.keywords.join(', ')}
                    onChange={(e) => updateArea(areaId, {
                      keywords: e.target.value.split(',').map((k) => k.trim()).filter(Boolean),
                    })} />
                </Stack>

                <Text size="sm" fw={600} mb="xs">Analysis Prompt</Text>
                <Text size="xs" c="dimmed" mb="sm">
                  Placeholders: {'{{DATASET}}'}, {'{{QUERY_RESULTS}}'}, {'{{TOTAL_QUERIES}}'}. Base context (profile + previous context) is prepended automatically.
                </Text>
                <MDEditor
                  value={area.prompt}
                  onChange={(val) => updateArea(areaId, { prompt: val || '' })}
                  height={400}
                  preview="edit"
                />
              </Card>
            </Tabs.Panel>
          ))}
        </Tabs>
      </Stack>

      {/* Add Custom Area Modal */}
      <Modal opened={addModalOpen} onClose={() => setAddModalOpen(false)} title="Add Custom Analysis Area">
        <Stack>
          <TextInput label="Area ID" description="Unique identifier (lowercase, no spaces)"
            placeholder="whale_analysis" value={newAreaId}
            onChange={(e) => setNewAreaId(e.target.value)} required />
          <TextInput label="Display Name" placeholder="Whale Analysis"
            value={newAreaName} onChange={(e) => setNewAreaName(e.target.value)} required />
          <TextInput label="Description" placeholder="Identify high-value spenders"
            value={newAreaDesc} onChange={(e) => setNewAreaDesc(e.target.value)} />
          <TextInput label="Keywords" description="Comma-separated keywords for filtering queries"
            placeholder="whale, spend, purchase, vip"
            value={newAreaKeywords} onChange={(e) => setNewAreaKeywords(e.target.value)} />
          <Button onClick={addCustomArea} disabled={!newAreaId || !newAreaName}>
            Add Area
          </Button>
        </Stack>
      </Modal>
    </Shell>
  );
}
