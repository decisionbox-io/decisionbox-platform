'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import {
  ActionIcon, Alert, Button, Checkbox, CloseButton, Group, Loader, MultiSelect,
  NumberInput, Select, Stack, Switch, Tabs, Text, TextInput, Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconCheck, IconDatabase, IconKey, IconPlus, IconSettings, IconShieldCheck } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, Project, ProviderMeta, ConfigField, SecretEntryResponse } from '@/lib/api';

export default function ProjectSettingsPage() {
  const { id } = useParams<{ id: string }>();
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

        setName(proj.name);
        setDescription(proj.description || '');
        setWhProvider(proj.warehouse.provider);
        setWhConfig({
          project_id: proj.warehouse.project_id || '',
          location: proj.warehouse.location || '',
          ...(proj.warehouse.config || {}),
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

        api.getProfileSchema(proj.domain, proj.category)
          .then(setProfileSchema)
          .catch(() => {});

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
          config: Object.fromEntries(
            Object.entries(whConfig).filter(([k]) => k !== 'project_id' && k !== 'location' && k !== 'dataset')
          ),
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

  const breadcrumb = project
    ? [{ label: 'Projects', href: '/' }, { label: project.name, href: `/projects/${id}` }, { label: 'Settings' }]
    : [{ label: 'Settings' }];

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  const selectedWh = warehouseProviders.find((p) => p.id === whProvider);
  const selectedLlm = llmProviders.find((p) => p.id === llmProvider);

  const saveButton = (
    <button onClick={handleSave} disabled={saving} style={{
      display: 'inline-flex', alignItems: 'center', gap: 6,
      background: 'var(--db-text-primary)', color: '#fff',
      border: 'none', borderRadius: 6, padding: '6px 14px',
      fontSize: 13, fontWeight: 500, cursor: saving ? 'default' : 'pointer',
      fontFamily: 'inherit', opacity: saving ? 0.5 : 1,
      transition: 'background 120ms ease',
    }}
    onMouseEnter={e => { if (!saving) e.currentTarget.style.background = '#333'; }}
    onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-text-primary)'; }}
    >
      <IconCheck size={14} />
      {saving ? 'Saving...' : 'Save settings'}
    </button>
  );

  return (
    <Shell breadcrumb={breadcrumb} actions={saveButton}>
      <Tabs defaultValue="general" styles={{
        tab: { fontSize: 13, fontWeight: 500, padding: '8px 16px' },
        panel: { paddingTop: 20 },
      }}>
        <Tabs.List>
          <Tabs.Tab value="general">General</Tabs.Tab>
          <Tabs.Tab value="warehouse">Data Warehouse</Tabs.Tab>
          <Tabs.Tab value="ai">AI Provider</Tabs.Tab>
          <Tabs.Tab value="secrets">Secrets</Tabs.Tab>
          <Tabs.Tab value="schedule">Schedule</Tabs.Tab>
          {profileSchema && <Tabs.Tab value="profile">Profile</Tabs.Tab>}
        </Tabs.List>

        {/* General */}
        <Tabs.Panel value="general">
          <SettingsSection>
            <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} />
            <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />
            <Group>
              <TextInput label="Domain" value={project.domain} disabled style={{ flex: 1 }} />
              <TextInput label="Category" value={project.category} disabled style={{ flex: 1 }} />
            </Group>
          </SettingsSection>
        </Tabs.Panel>

        {/* Data Warehouse */}
        <Tabs.Panel value="warehouse">
          <SettingsSection>
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

            <div style={{ fontSize: 13, fontWeight: 500, marginTop: 8 }}>Filter (optional)</div>
            <Text size="xs" c="dimmed">For shared datasets. Leave empty if the entire dataset is yours.</Text>
            <Group grow>
              <TextInput label="Filter Field" placeholder="app_id" value={filterField}
                onChange={(e) => setFilterField(e.target.value)} />
              <TextInput label="Filter Value" placeholder="my-app-123" value={filterValue}
                onChange={(e) => setFilterValue(e.target.value)} />
            </Group>
          </SettingsSection>
        </Tabs.Panel>

        {/* AI Provider */}
        <Tabs.Panel value="ai">
          <SettingsSection>
            <Select label="LLM Provider" data={llmProviders.map((p) => ({ value: p.id, label: p.name }))}
              value={llmProvider} onChange={(v) => {
                setLlmProvider(v || '');
                setLlmModel('');
                setLlmConfig({});
              }} />
            {selectedLlm?.description && <Text size="xs" c="dimmed">{selectedLlm.description}</Text>}

            <TextInput label="Model" value={llmModel} onChange={(e) => setLlmModel(e.target.value)}
              placeholder="e.g. claude-opus-4-6, gpt-4o, gemini-2.5-pro" />

            {selectedLlm?.config_fields
              .filter((f) => f.key !== 'model' && f.key !== 'api_key')
              .map((field) => (
                <DynamicField key={field.key} field={field}
                  value={llmConfig[field.key] || ''}
                  onChange={(val) => setLlmConfig((prev) => ({ ...prev, [field.key]: val }))} />
              ))}
          </SettingsSection>
        </Tabs.Panel>

        {/* Secrets */}
        <Tabs.Panel value="secrets">
          <SettingsSection>
            <Text size="xs" c="dimmed" mb="md">
              API keys are stored encrypted and never exposed in full. Per-project — each project has its own keys.
            </Text>

            {secretsList.length > 0 && (
              <Stack gap="xs" mb="md">
                {secretsList.map((s) => (
                  <div key={s.key} style={{
                    borderRadius: 'var(--db-radius)', background: 'var(--db-bg-muted)', padding: 8,
                  }}>
                    <Group justify="space-between">
                      <Group gap="xs">
                        <IconShieldCheck size={14} color={s.warning ? 'var(--db-amber-text)' : 'var(--db-green-text)'} />
                        <Text size="sm" fw={500}>{s.key}</Text>
                      </Group>
                      <Text size="xs" c="dimmed" style={{ fontFamily: 'SF Mono, Fira Code, monospace' }}>{s.masked}</Text>
                    </Group>
                    {s.warning && <Text size="xs" c="orange" mt={4}>{s.warning}</Text>}
                  </div>
                ))}
              </Stack>
            )}

            <Group gap="xs" align="end">
              <Select label="Key" size="xs" w={180} value={newSecretKey}
                onChange={(v) => setNewSecretKey(v || 'llm-api-key')}
                data={[
                  { value: 'llm-api-key', label: 'LLM API Key' },
                  { value: 'warehouse-credentials', label: 'Warehouse Credentials (SA Key JSON)' },
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
          </SettingsSection>
        </Tabs.Panel>

        {/* Schedule */}
        <Tabs.Panel value="schedule">
          <SettingsSection>
            <Switch label="Enable automatic discovery" checked={scheduleEnabled}
              onChange={(e) => setScheduleEnabled(e.currentTarget.checked)} />
            {scheduleEnabled && (
              <TextInput label="Cron Expression" value={scheduleCron}
                onChange={(e) => setScheduleCron(e.target.value)} description="e.g., 0 2 * * * (daily at 2 AM)" />
            )}
            <NumberInput label="Max Exploration Steps" value={maxSteps}
              onChange={(v) => setMaxSteps(Number(v) || 100)} min={10} max={500} />
          </SettingsSection>
        </Tabs.Panel>

        {/* Profile */}
        {profileSchema && (
          <Tabs.Panel value="profile">
            <SettingsSection>
              <Text size="xs" c="dimmed" mb="md">
                Help the AI understand your game. This context improves insight quality.
              </Text>
              <ProfileEditor schema={profileSchema} profile={profile} onChange={setProfile} />
            </SettingsSection>
          </Tabs.Panel>
        )}
      </Tabs>
    </Shell>
  );
}

function SettingsSection({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: '20px',
      maxWidth: 640,
    }}>
      <Stack gap="md">{children}</Stack>
    </div>
  );
}

function DynamicField({ field, value, onChange }: { field: ConfigField; value: string; onChange: (v: string) => void }) {
  return (
    <TextInput label={field.label} required={field.required}
      placeholder={field.placeholder || field.default} description={field.description}
      value={value} onChange={(e) => onChange(e.target.value)} />
  );
}

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

        if (sec.type === 'array' && sec.items && (sec.items as Record<string, unknown>).type === 'object') {
          const items = (Array.isArray(profile[sectionKey]) ? profile[sectionKey] : []) as Record<string, unknown>[];
          const itemSchema = sec.items as { properties?: Record<string, unknown> };
          return (
            <ArrayOfObjectsEditor key={sectionKey} title={sec.title || sectionKey}
              itemSchema={itemSchema} items={items}
              onChange={(newItems) => updateSection(sectionKey, newItems)} />
          );
        }

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
      <Stack gap="sm">
        {items.map((item, idx) => (
          <div key={idx} style={{
            border: '1px solid var(--db-border-default)',
            borderRadius: 'var(--db-radius-lg)',
            padding: 16, background: 'var(--db-bg-muted)',
          }}>
            <Group justify="space-between" mb={8}>
              <Text size="xs" fw={500} c="dimmed">#{idx + 1}</Text>
              <CloseButton size="xs" onClick={() => removeItem(idx)} />
            </Group>
            <div style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(2, 1fr)',
              gap: 12,
            }}>
              {Object.entries(fields).map(([fieldKey, fieldSchema]) => {
                const fs = fieldSchema as { type?: string; title?: string };
                // Full-width for text fields (description, name) and nested arrays
                const isWide = fs.type === 'array' || fieldKey === 'description' || fieldKey === 'name';
                return (
                  <div key={fieldKey} style={{ gridColumn: isWide ? '1 / -1' : undefined }}>
                    <SchemaField fieldKey={fieldKey} fieldSchema={fieldSchema}
                      value={item[fieldKey]}
                      onChange={(v) => updateItem(idx, fieldKey, v)} />
                  </div>
                );
              })}
            </div>
          </div>
        ))}
        {items.length === 0 && (
          <div style={{
            border: '2px dashed var(--db-border-strong)',
            borderRadius: 'var(--db-radius)',
            padding: '20px', textAlign: 'center',
          }}>
            <Text size="xs" c="dimmed">No items yet. Click + to add.</Text>
          </div>
        )}
      </Stack>
    </div>
  );
}

function InlineArrayEditor({ title, itemSchema, items, onChange }: {
  title: string;
  itemSchema: { properties?: Record<string, unknown> };
  items: Record<string, unknown>[];
  onChange: (items: unknown) => void;
}) {
  const fields = itemSchema.properties || {};
  const fieldEntries = Object.entries(fields);
  const addItem = () => onChange([...items, {}]);
  const removeItem = (idx: number) => onChange(items.filter((_, i) => i !== idx));
  const updateItem = (idx: number, field: string, value: unknown) => {
    const updated = [...items];
    updated[idx] = { ...updated[idx], [field]: value };
    onChange(updated);
  };

  return (
    <div>
      <Group justify="space-between" mb={6}>
        <Text size="xs" fw={600}>{title}</Text>
        <ActionIcon variant="subtle" size="xs" onClick={addItem}>
          <IconPlus size={12} />
        </ActionIcon>
      </Group>

      {/* Column headers */}
      {items.length > 0 && (
        <Group gap={8} mb={4} wrap="nowrap" style={{ paddingRight: 28 }}>
          {fieldEntries.map(([fk, fs]) => {
            const f = fs as { title?: string; type?: string };
            const isNumber = f.type === 'integer' || f.type === 'number';
            return (
              <Text key={fk} size="xs" c="dimmed" fw={500}
                style={{ flex: isNumber ? 1 : 2, fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.3px' }}>
                {f.title || fk}
              </Text>
            );
          })}
        </Group>
      )}

      <Stack gap={6}>
        {items.map((item, idx) => (
          <Group key={idx} gap={8} wrap="nowrap" align="center">
            {fieldEntries.map(([fk, fs]) => {
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

      {items.length === 0 && (
        <Text size="xs" c="dimmed" ta="center" py="xs">No items. Click + to add.</Text>
      )}
    </div>
  );
}
