'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, NumberInput, Select, Stack, Switch, Text, TextInput, Textarea, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconArrowLeft, IconCheck } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, Project, ProviderMeta, ConfigField } from '@/lib/api';

export default function ProjectSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const [project, setProject] = useState<Project | null>(null);
  const [warehouseProviders, setWarehouseProviders] = useState<ProviderMeta[]>([]);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Form state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [whProvider, setWhProvider] = useState('');
  const [whConfig, setWhConfig] = useState<Record<string, string>>({});
  const [datasets, setDatasets] = useState('');
  const [filterField, setFilterField] = useState('');
  const [filterValue, setFilterValue] = useState('');
  const [llmProvider, setLlmProvider] = useState('');
  const [llmModel, setLlmModel] = useState('');
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleCron, setScheduleCron] = useState('');
  const [maxSteps, setMaxSteps] = useState(100);

  useEffect(() => {
    Promise.all([
      api.getProject(id),
      api.listWarehouseProviders(),
      api.listLLMProviders(),
    ])
      .then(([proj, whProvs, llmProvs]) => {
        setProject(proj);
        setWarehouseProviders(whProvs);
        setLlmProviders(llmProvs);

        // Populate form
        setName(proj.name);
        setDescription(proj.description || '');
        setWhProvider(proj.warehouse.provider);
        setWhConfig({
          project_id: proj.warehouse.project_id || '',
          location: proj.warehouse.location || '',
        });
        setDatasets((proj.warehouse.datasets || []).join(', '));
        setFilterField(proj.warehouse.filter_field || '');
        setFilterValue(proj.warehouse.filter_value || '');
        setLlmProvider(proj.llm.provider);
        setLlmModel(proj.llm.model);
        setScheduleEnabled(proj.schedule?.enabled || false);
        setScheduleCron(proj.schedule?.cron_expr || '0 2 * * *');
        setMaxSteps(proj.schedule?.max_steps || 100);
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [id]);

  const handleSave = async () => {
    setSaving(true);
    try {
      const datasetsList = datasets.split(',').map((d) => d.trim()).filter(Boolean);

      await api.updateProject(id, {
        name,
        description,
        domain: project!.domain,
        category: project!.category,
        warehouse: {
          provider: whProvider,
          project_id: whConfig['project_id'] || '',
          datasets: datasetsList,
          location: whConfig['location'] || '',
          filter_field: filterField,
          filter_value: filterValue,
        },
        llm: { provider: llmProvider, model: llmModel },
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
      });

      notifications.show({ title: 'Saved', message: 'Project settings updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  const selectedWh = warehouseProviders.find((p) => p.id === whProvider);
  const selectedLlm = llmProviders.find((p) => p.id === llmProvider);

  return (
    <Shell>
      <Stack gap="lg" maw={700}>
        <Group>
          <Button variant="subtle" leftSection={<IconArrowLeft size={16} />}
            onClick={() => router.push(`/projects/${id}`)}>Back</Button>
          <Title order={2}>Project Settings</Title>
        </Group>

        {/* Basic Info */}
        <Card withBorder p="lg">
          <Title order={4} mb="md">Basic Information</Title>
          <Stack>
            <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} />
            <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />
            <Group>
              <TextInput label="Domain" value={project.domain} disabled />
              <TextInput label="Category" value={project.category} disabled />
            </Group>
          </Stack>
        </Card>

        {/* Warehouse */}
        <Card withBorder p="lg">
          <Title order={4} mb="md">Data Warehouse</Title>
          <Stack>
            <Select label="Provider" data={warehouseProviders.map((p) => ({ value: p.id, label: p.name }))}
              value={whProvider} onChange={(v) => setWhProvider(v || '')} />
            {selectedWh?.description && <Text size="xs" c="dimmed">{selectedWh.description}</Text>}

            {selectedWh?.config_fields.map((field) => (
              <DynamicField key={field.key} field={field}
                value={whConfig[field.key] || ''}
                onChange={(val) => setWhConfig((prev) => ({ ...prev, [field.key]: val }))} />
            ))}

            <TextInput label="Datasets" description="Comma-separated dataset names"
              placeholder="events_prod, features_prod"
              value={datasets} onChange={(e) => setDatasets(e.target.value)} />

            <Group grow>
              <TextInput label="Filter Field" placeholder="app_id" value={filterField}
                onChange={(e) => setFilterField(e.target.value)} />
              <TextInput label="Filter Value" placeholder="my-app-123" value={filterValue}
                onChange={(e) => setFilterValue(e.target.value)} />
            </Group>
          </Stack>
        </Card>

        {/* LLM */}
        <Card withBorder p="lg">
          <Title order={4} mb="md">AI Provider</Title>
          <Stack>
            <Select label="LLM Provider" data={llmProviders.map((p) => ({ value: p.id, label: p.name }))}
              value={llmProvider} onChange={(v) => setLlmProvider(v || '')} />
            {selectedLlm?.description && <Text size="xs" c="dimmed">{selectedLlm.description}</Text>}

            <TextInput label="Model" value={llmModel} onChange={(e) => setLlmModel(e.target.value)} />
          </Stack>
        </Card>

        {/* Schedule */}
        <Card withBorder p="lg">
          <Title order={4} mb="md">Discovery Schedule</Title>
          <Stack>
            <Switch label="Enable automatic discovery" checked={scheduleEnabled}
              onChange={(e) => setScheduleEnabled(e.currentTarget.checked)} />
            {scheduleEnabled && (
              <TextInput label="Cron Expression" value={scheduleCron}
                onChange={(e) => setScheduleCron(e.target.value)} description="e.g., 0 2 * * * (daily at 2 AM)" />
            )}
            <NumberInput label="Max Exploration Steps" value={maxSteps}
              onChange={(v) => setMaxSteps(Number(v) || 100)} min={10} max={500} />
          </Stack>
        </Card>

        <Button onClick={handleSave} loading={saving} leftSection={<IconCheck size={16} />} fullWidth>
          Save Settings
        </Button>
      </Stack>
    </Shell>
  );
}

function DynamicField({ field, value, onChange }: { field: ConfigField; value: string; onChange: (v: string) => void }) {
  return (
    <TextInput label={field.label} required={field.required}
      placeholder={field.placeholder || field.default} description={field.description}
      value={value} onChange={(e) => onChange(e.target.value)} />
  );
}
