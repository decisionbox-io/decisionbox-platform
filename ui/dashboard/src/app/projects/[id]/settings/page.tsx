'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  ActionIcon, Alert, Button, Card, Checkbox, CloseButton, Group, Loader, MultiSelect,
  NumberInput, Select, Stack, Switch, Text, TextInput, Textarea, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconArrowLeft, IconCheck, IconPlus } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { IconEye, IconEyeOff, IconKey, IconShieldCheck } from '@tabler/icons-react';
import { api, Project, ProviderMeta, ConfigField, SecretEntryResponse } from '@/lib/api';

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
  const [llmConfig, setLlmConfig] = useState<Record<string, string>>({});
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleCron, setScheduleCron] = useState('');
  const [maxSteps, setMaxSteps] = useState(100);
  const [profile, setProfile] = useState<Record<string, Record<string, unknown>>>({});
  const [profileSchema, setProfileSchema] = useState<Record<string, unknown> | null>(null);
  const [secretsList, setSecretsList] = useState<SecretEntryResponse[]>([]);
  const [newSecretKey, setNewSecretKey] = useState('llm-api-key');
  const [newSecretValue, setNewSecretValue] = useState('');
  const [savingSecret, setSavingSecret] = useState(false);

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
        setLlmConfig(proj.llm.config || {});
        setScheduleEnabled(proj.schedule?.enabled || false);
        setScheduleCron(proj.schedule?.cron_expr || '0 2 * * *');
        setMaxSteps(proj.schedule?.max_steps || 100);
        setProfile((proj.profile || {}) as Record<string, Record<string, unknown>>);

        // Load profile schema for this domain/category
        api.getProfileSchema(proj.domain, proj.category)
          .then(setProfileSchema)
          .catch(() => {});

        // Load secrets list
        api.listSecrets(proj.id || id)
          .then((s) => setSecretsList(s || []))
          .catch(() => {});
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
        llm: { provider: llmProvider, model: llmModel, config: llmConfig },
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
        profile,
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

            {selectedWh?.config_fields
              .filter((f) => f.key !== 'dataset')
              .map((field) => (
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
              value={llmProvider} onChange={(v) => {
                setLlmProvider(v || '');
                setLlmModel('');
                setLlmConfig({});
              }} />
            {selectedLlm?.description && <Text size="xs" c="dimmed">{selectedLlm.description}</Text>}

            <TextInput label="Model" value={llmModel} onChange={(e) => setLlmModel(e.target.value)}
              placeholder="e.g. claude-opus-4-6, gpt-4o, gemini-2.5-pro" />

            {/* Provider-specific config fields (e.g., project_id, location for Vertex AI) */}
            {selectedLlm?.config_fields
              .filter((f) => f.key !== 'model' && f.key !== 'api_key')
              .map((field) => (
                <DynamicField key={field.key} field={field}
                  value={llmConfig[field.key] || ''}
                  onChange={(val) => setLlmConfig((prev) => ({ ...prev, [field.key]: val }))} />
              ))}
          </Stack>
        </Card>

        {/* Secrets */}
        <Card withBorder p="lg">
          <Title order={4} mb="xs">
            <IconKey size={18} style={{ verticalAlign: 'middle', marginRight: 8 }} />
            Secrets
          </Title>
          <Text size="xs" c="dimmed" mb="md">
            API keys are stored encrypted and never exposed in full. Per-project — each project has its own keys.
          </Text>

          {/* Existing secrets */}
          {secretsList.length > 0 && (
            <Stack gap="xs" mb="md">
              {secretsList.map((s) => (
                <Group key={s.key} justify="space-between" p="xs" style={{ borderRadius: 4, background: 'var(--mantine-color-gray-0)' }}>
                  <Group gap="xs">
                    <IconShieldCheck size={14} color="var(--mantine-color-green-6)" />
                    <Text size="sm" fw={500}>{s.key}</Text>
                  </Group>
                  <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>{s.masked}</Text>
                </Group>
              ))}
            </Stack>
          )}

          {/* Add/update secret */}
          <Group gap="xs" align="end">
            <Select label="Key" size="xs" w={180} value={newSecretKey}
              onChange={(v) => setNewSecretKey(v || 'llm-api-key')}
              data={[
                { value: 'llm-api-key', label: 'LLM API Key' },
                { value: 'warehouse-creds', label: 'Warehouse Credentials' },
              ]}
              allowDeselect={false} />
            <TextInput label="Value" size="xs" style={{ flex: 1 }}
              placeholder="Enter secret value" value={newSecretValue}
              onChange={(e) => setNewSecretValue(e.target.value)}
              type="password" />
            <Button size="xs" loading={savingSecret} disabled={!newSecretValue}
              onClick={async () => {
                setSavingSecret(true);
                try {
                  await api.setSecret(id, newSecretKey, newSecretValue);
                  setNewSecretValue('');
                  notifications.show({ title: 'Saved', message: `Secret "${newSecretKey}" saved`, color: 'green' });
                  // Refresh list
                  const updated = await api.listSecrets(id);
                  setSecretsList(updated || []);
                } catch (e: unknown) {
                  notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
                } finally {
                  setSavingSecret(false);
                }
              }}>
              Save Secret
            </Button>
          </Group>
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

        {/* Game Profile */}
        {profileSchema && (
          <Card withBorder p="lg">
            <Title order={4} mb="xs">Game Profile</Title>
            <Text size="xs" c="dimmed" mb="md">
              Help the AI understand your game. This context improves insight quality.
            </Text>
            <ProfileEditor schema={profileSchema} profile={profile} onChange={setProfile} />
          </Card>
        )}

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

// Renders profile fields from JSON Schema sections (basic_info, gameplay, monetization, kpis)
function ProfileEditor({ schema, profile, onChange }: {
  schema: Record<string, unknown>;
  profile: Record<string, Record<string, unknown>>;
  onChange: (profile: Record<string, Record<string, unknown>>) => void;
}) {
  const properties = (schema as { properties?: Record<string, unknown> }).properties || {};

  const updateField = (section: string, field: string, value: unknown) => {
    onChange({
      ...profile,
      [section]: { ...(profile[section] || {}), [field]: value },
    });
  };

  const updateSection = (section: string, value: unknown) => {
    onChange({ ...profile, [section]: value as Record<string, unknown> });
  };

  return (
    <Stack gap="md">
      {Object.entries(properties).map(([sectionKey, sectionSchema]) => {
        const sec = sectionSchema as {
          title?: string; type?: string;
          properties?: Record<string, unknown>;
          items?: Record<string, unknown>;
        };

        // Array of objects (boosters, iap_packages, lootboxes)
        if (sec.type === 'array' && sec.items && (sec.items as Record<string, unknown>).type === 'object') {
          const items = (Array.isArray(profile[sectionKey]) ? profile[sectionKey] : []) as Record<string, unknown>[];
          const itemSchema = sec.items as { properties?: Record<string, unknown> };
          return (
            <ArrayOfObjectsEditor key={sectionKey} title={sec.title || sectionKey}
              itemSchema={itemSchema} items={items}
              onChange={(newItems) => updateSection(sectionKey, newItems)} />
          );
        }

        // Simple array (e.g., array of strings)
        if (sec.type === 'array') {
          const items = (Array.isArray(profile[sectionKey]) ? profile[sectionKey] : []) as string[];
          return (
            <div key={sectionKey}>
              <Text size="sm" fw={600} mb="xs">{sec.title || sectionKey}</Text>
              <TextInput size="xs" description="Comma-separated values"
                value={items.join(', ')}
                onChange={(e) => updateSection(sectionKey, e.target.value.split(',').map(s => s.trim()).filter(Boolean))} />
            </div>
          );
        }

        // Object sections — render individual fields
        if (!sec.properties) return null;
        return (
          <div key={sectionKey}>
            <Text size="sm" fw={600} mb="xs">{sec.title || sectionKey}</Text>
            <Stack gap="xs">
              {Object.entries(sec.properties).map(([fieldKey, fieldSchema]) => (
                <SchemaField key={fieldKey} fieldKey={fieldKey} fieldSchema={fieldSchema}
                  value={(profile[sectionKey] || {})[fieldKey]}
                  onChange={(v) => updateField(sectionKey, fieldKey, v)} />
              ))}
            </Stack>
          </div>
        );
      })}
    </Stack>
  );
}

// Renders a single field from a JSON Schema property
function SchemaField({ fieldKey, fieldSchema, value, onChange }: {
  fieldKey: string; fieldSchema: unknown; value: unknown;
  onChange: (v: unknown) => void;
}) {
  const fs = fieldSchema as {
    type?: string; title?: string; description?: string;
    enum?: string[]; items?: { type?: string; enum?: string[]; properties?: Record<string, unknown> };
  };

  if (fs.type === 'string' && fs.enum) {
    return (
      <Select label={fs.title || fieldKey} description={fs.description}
        data={fs.enum} value={(value as string) || null} clearable size="xs"
        onChange={(v) => onChange(v || '')} />
    );
  }
  if (fs.type === 'array' && fs.items?.enum) {
    return (
      <MultiSelect label={fs.title || fieldKey} description={fs.description}
        data={fs.items.enum} value={(value as string[]) || []} size="xs"
        onChange={(v) => onChange(v)} />
    );
  }
  if (fs.type === 'array' && fs.items?.type === 'string') {
    const items = (Array.isArray(value) ? value : []) as string[];
    return (
      <TextInput label={fs.title || fieldKey} description={fs.description || 'Comma-separated'}
        value={items.join(', ')} size="xs"
        onChange={(e) => onChange(e.target.value.split(',').map(s => s.trim()).filter(Boolean))} />
    );
  }
  if (fs.type === 'array' && fs.items?.type === 'object') {
    // Nested array of objects (e.g., IAP contents: [{item: "coins", count: 100}])
    const itemSchema = fs.items as { properties?: Record<string, unknown> };
    const items = (Array.isArray(value) ? value : []) as Record<string, unknown>[];
    return (
      <InlineArrayEditor title={fs.title || fieldKey} itemSchema={itemSchema}
        items={items} onChange={onChange} />
    );
  }
  if (fs.type === 'boolean') {
    return (
      <Checkbox label={fs.title || fieldKey} description={fs.description}
        checked={!!value} size="xs"
        onChange={(e) => onChange(e.currentTarget.checked)} />
    );
  }
  if (fs.type === 'number' || fs.type === 'integer') {
    return (
      <NumberInput label={fs.title || fieldKey} description={fs.description}
        value={(value as number) ?? ''} size="xs"
        onChange={(v) => onChange(v)} />
    );
  }
  return (
    <TextInput label={fs.title || fieldKey} description={fs.description}
      value={(value as string) || ''} size="xs"
      onChange={(e) => onChange(e.target.value)} />
  );
}

// Renders repeatable items for array-of-objects (boosters, IAP packages, lootboxes)
function ArrayOfObjectsEditor({ title, itemSchema, items, onChange }: {
  title: string;
  itemSchema: { properties?: Record<string, unknown> };
  items: Record<string, unknown>[];
  onChange: (items: Record<string, unknown>[]) => void;
}) {
  const addItem = () => onChange([...items, {}]);
  const removeItem = (idx: number) => onChange(items.filter((_, i) => i !== idx));
  const updateItem = (idx: number, field: string, value: unknown) => {
    const updated = [...items];
    updated[idx] = { ...updated[idx], [field]: value };
    onChange(updated);
  };

  const fields = itemSchema.properties || {};

  return (
    <div>
      <Group justify="space-between" mb="xs">
        <Text size="sm" fw={600}>{title} ({items.length})</Text>
        <ActionIcon variant="light" size="sm" onClick={addItem}>
          <IconPlus size={14} />
        </ActionIcon>
      </Group>
      <Stack gap="xs">
        {items.map((item, idx) => (
          <Card key={idx} withBorder p="xs" radius="sm" bg="var(--mantine-color-gray-0)">
            <Group justify="space-between" mb={4}>
              <Text size="xs" c="dimmed">#{idx + 1}</Text>
              <CloseButton size="xs" onClick={() => removeItem(idx)} />
            </Group>
            <Group grow gap="xs" wrap="wrap">
              {Object.entries(fields).map(([fieldKey, fieldSchema]) => (
                <SchemaField key={fieldKey} fieldKey={fieldKey} fieldSchema={fieldSchema}
                  value={item[fieldKey]}
                  onChange={(v) => updateItem(idx, fieldKey, v)} />
              ))}
            </Group>
          </Card>
        ))}
        {items.length === 0 && (
          <Text size="xs" c="dimmed" ta="center" py="xs">
            No items. Click + to add.
          </Text>
        )}
      </Stack>
    </div>
  );
}

// Compact inline rows for nested array-of-objects (e.g., IAP contents: [{item, count}])
function InlineArrayEditor({ title, itemSchema, items, onChange }: {
  title: string;
  itemSchema: { properties?: Record<string, unknown> };
  items: Record<string, unknown>[];
  onChange: (items: unknown) => void;
}) {
  const fields = itemSchema.properties || {};
  const addItem = () => onChange([...items, {}]);
  const removeItem = (idx: number) => onChange(items.filter((_, i) => i !== idx));
  const updateItem = (idx: number, field: string, value: unknown) => {
    const updated = [...items];
    updated[idx] = { ...updated[idx], [field]: value };
    onChange(updated);
  };

  return (
    <div>
      <Group gap={4} mb={4}>
        <Text size="xs" fw={600}>{title}</Text>
        <ActionIcon variant="subtle" size="xs" onClick={addItem}>
          <IconPlus size={12} />
        </ActionIcon>
      </Group>
      <Stack gap={4}>
        {items.map((item, idx) => (
          <Group key={idx} gap={4} wrap="nowrap">
            {Object.entries(fields).map(([fk, fs]) => {
              const f = fs as { type?: string; title?: string };
              if (f.type === 'integer' || f.type === 'number') {
                return (
                  <NumberInput key={fk} placeholder={f.title || fk} size="xs"
                    value={(item[fk] as number) ?? ''} style={{ flex: 1 }}
                    onChange={(v) => updateItem(idx, fk, v)} />
                );
              }
              return (
                <TextInput key={fk} placeholder={f.title || fk} size="xs"
                  value={(item[fk] as string) || ''} style={{ flex: 2 }}
                  onChange={(e) => updateItem(idx, fk, e.target.value)} />
              );
            })}
            <CloseButton size="xs" onClick={() => removeItem(idx)} />
          </Group>
        ))}
      </Stack>
    </div>
  );
}
