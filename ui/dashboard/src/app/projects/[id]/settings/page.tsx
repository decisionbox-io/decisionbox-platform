'use client';

import { useCallback, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  ActionIcon, Alert, Button, Checkbox, CloseButton, Divider, Group, Loader, Modal, MultiSelect,
  NumberInput, Select, Stack, Switch, Tabs, Text, TextInput, Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconPlus } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import WarehouseConfigPanel from '@/components/projects/WarehouseConfigPanel';
import ProvidersPanel from '@/components/projects/ProvidersPanel';
import { api, Project, ProviderMeta } from '@/lib/api';

export default function ProjectSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();

  const [project, setProject] = useState<Project | null>(null);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // General tab state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [savingGeneral, setSavingGeneral] = useState(false);

  // Schedule tab state
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleCron, setScheduleCron] = useState('');
  const [maxSteps, setMaxSteps] = useState(100);
  const [savingSchedule, setSavingSchedule] = useState(false);

  // Profile tab state
  const [profile, setProfile] = useState<Record<string, Record<string, unknown>>>({});
  const [profileSchema, setProfileSchema] = useState<Record<string, unknown> | null>(null);
  const [savingProfile, setSavingProfile] = useState(false);

  // Blurb tab state
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  const [savingBlurb, setSavingBlurb] = useState(false);

  // Advanced — local-only preference, not on the project document.
  const [debugLogsEnabled, setDebugLogsEnabled] = useState(false);

  // Tab routing — honor `location.hash` so deep-links like
  // `/projects/:id/settings#advanced` open the right tab.
  const validTabs = ['general', 'warehouse', 'ai', 'blurb', 'schedule', 'profile', 'advanced'];
  const [activeTab, setActiveTab] = useState<string>('general');
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const applyHash = () => {
      const h = window.location.hash.replace(/^#/, '');
      if (h && validTabs.includes(h)) setActiveTab(h);
    };
    applyHash();
    window.addEventListener('hashchange', applyHash);
    return () => window.removeEventListener('hashchange', applyHash);
    // validTabs is stable (literal); exhaustive-deps is noisy here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    setDebugLogsEnabled(window.localStorage.getItem(`db:showDebugLogs:${id}`) === '1');
  }, [id]);

  useEffect(() => {
    Promise.all([api.getProject(id), api.listLLMProviders()])
      .then(([proj, llmProvs]) => {
        setProject(proj);
        setLlmProviders(llmProvs);
        setName(proj.name);
        setDescription(proj.description || '');
        setScheduleEnabled(proj.schedule?.enabled || false);
        setScheduleCron(proj.schedule?.cron_expr || '0 2 * * *');
        setMaxSteps(proj.schedule?.max_steps || 100);
        setProfile((proj.profile || {}) as Record<string, Record<string, unknown>>);
        if (proj.blurb_llm && proj.blurb_llm.provider) {
          setBlurb({
            enabled: true,
            provider: proj.blurb_llm.provider,
            model: proj.blurb_llm.model || '',
            config: proj.blurb_llm.config || {},
            apiKey: '',
          });
        }
        if (proj.domain) {
          api.getProfileSchema(proj.domain, proj.category)
            .then(setProfileSchema)
            .catch(() => {});
        }
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [id]);

  const refreshProject = useCallback(async () => {
    try {
      const proj = await api.getProject(id);
      setProject(proj);
    } catch {
      // Refresh failures are non-fatal — the panel that just saved
      // already updated its own local state.
    }
  }, [id]);

  const breadcrumb = project
    ? [{ label: 'Projects', href: '/' }, { label: project.name, href: `/projects/${id}` }, { label: 'Settings' }]
    : [{ label: 'Settings' }];

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  const saveGeneral = async () => {
    setSavingGeneral(true);
    try {
      const saved = await api.updateProject(id, { name, description });
      setProject(saved);
      notifications.show({ title: 'Saved', message: 'General settings updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingGeneral(false);
    }
  };

  const saveSchedule = async () => {
    setSavingSchedule(true);
    try {
      const saved = await api.updateProject(id, {
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
      });
      setProject(saved);
      notifications.show({ title: 'Saved', message: 'Schedule updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingSchedule(false);
    }
  };

  const saveProfile = async () => {
    setSavingProfile(true);
    try {
      const saved = await api.updateProject(id, { profile });
      setProject(saved);
      notifications.show({ title: 'Saved', message: 'Profile updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingProfile(false);
    }
  };

  const saveBlurb = async () => {
    setSavingBlurb(true);
    try {
      const saved = await api.updateProject(id, {
        blurb_llm:
          blurb.enabled && blurb.provider && blurb.model
            ? {
                provider: blurb.provider,
                model: blurb.model,
                config: Object.fromEntries(
                  Object.entries(blurb.config).filter(([k]) => k !== 'model' && k !== 'api_key'),
                ),
              }
            : undefined,
      });
      if (blurb.enabled && blurb.apiKey) {
        await api.setSecret(id, 'blurb-llm-api-key', blurb.apiKey);
        setBlurb((prev) => ({ ...prev, apiKey: '' }));
      }
      setProject(saved);
      notifications.show({ title: 'Saved', message: 'Blurb LLM updated', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingBlurb(false);
    }
  };

  return (
    <Shell breadcrumb={breadcrumb}>
      <Tabs value={activeTab} onChange={(v) => { if (v) setActiveTab(v); }} styles={{
        tab: { fontSize: 13, fontWeight: 500, padding: '8px 16px' },
        panel: { paddingTop: 20 },
      }}>
        <Tabs.List>
          <Tabs.Tab value="general">General</Tabs.Tab>
          <Tabs.Tab value="warehouse">Data Warehouse</Tabs.Tab>
          <Tabs.Tab value="ai">AI &amp; Embedding</Tabs.Tab>
          <Tabs.Tab value="blurb">Blurb Model</Tabs.Tab>
          <Tabs.Tab value="schedule">Schedule</Tabs.Tab>
          {profileSchema && <Tabs.Tab value="profile">Profile</Tabs.Tab>}
          <Tabs.Tab value="advanced">Advanced</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="general">
          <SettingsSection>
            <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} />
            <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />
            <Group>
              <TextInput label="Domain" value={project.domain} disabled style={{ flex: 1 }} />
              <TextInput label="Category" value={project.category} disabled style={{ flex: 1 }} />
            </Group>
            <Group justify="flex-end">
              <Button onClick={saveGeneral} loading={savingGeneral}>Save general</Button>
            </Group>
          </SettingsSection>
        </Tabs.Panel>

        <Tabs.Panel value="warehouse">
          <WarehouseConfigPanel projectId={id} variant="page" onSaved={() => { void refreshProject(); }} />
        </Tabs.Panel>

        <Tabs.Panel value="ai">
          <ProvidersPanel projectId={id} variant="page" onSaved={() => { void refreshProject(); }} />
        </Tabs.Panel>

        <Tabs.Panel value="blurb">
          <SettingsSection>
            <Text size="sm" fw={500}>Blurb Model</Text>
            <Text size="xs" c="dimmed" mb="sm">
              The LLM used during schema indexing to generate the per-table
              descriptions that get embedded into Qdrant. Override here to pick a
              cheaper / faster model. Changes apply to the next re-index.
            </Text>
            <BlurbLLMEditor
              llmProviders={llmProviders}
              value={blurb}
              onChange={(next) => setBlurb(next)}
              startInModelPhase={!!project?.blurb_llm?.provider}
            />
            <Group justify="flex-end">
              <Button onClick={saveBlurb} loading={savingBlurb}>Save blurb model</Button>
            </Group>
          </SettingsSection>
        </Tabs.Panel>

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
            <Group justify="flex-end">
              <Button onClick={saveSchedule} loading={savingSchedule}>Save schedule</Button>
            </Group>
          </SettingsSection>
        </Tabs.Panel>

        {profileSchema && (
          <Tabs.Panel value="profile">
            <SettingsSection>
              <Text size="xs" c="dimmed" mb="md">
                Help the AI understand your domain. This context improves insight quality.
              </Text>
              <ProfileEditor schema={profileSchema} profile={profile} onChange={setProfile} />
              <Group justify="flex-end">
                <Button onClick={saveProfile} loading={savingProfile}>Save profile</Button>
              </Group>
            </SettingsSection>
          </Tabs.Panel>
        )}

        <Tabs.Panel value="advanced">
          <SettingsSection>
            <Stack gap="sm">
              <Text size="sm" fw={500}>Debugging</Text>
              <Switch
                label="Show debug logs during discovery and indexing"
                description="Adds a verbose per-query + per-LLM-call tail to the live discovery panel, and a live agent-stderr tail to the schema-index panel on this project's page."
                checked={debugLogsEnabled}
                onChange={(e) => {
                  const next = e.currentTarget.checked;
                  setDebugLogsEnabled(next);
                  if (typeof window !== 'undefined' && id) {
                    window.localStorage.setItem(`db:showDebugLogs:${id}`, next ? '1' : '0');
                  }
                }}
              />
              <Text size="xs" c="dimmed">
                Local-browser preference — not shared with other users and not saved on the project.
              </Text>
              <Divider my="xs" />
              <Text size="sm" fw={500}>Schema cache</Text>
              <Text size="xs" c="dimmed">
                The agent caches the discovered warehouse schema so re-runs skip the full catalog pass. Clearing the cache also drops the Qdrant index and resets the project to <strong>needs indexing</strong> — discovery will be blocked until a fresh reindex completes.
              </Text>
              {id && <ClearSchemaCacheButton projectId={id} />}
              <Divider my="md" />
              <Text size="sm" fw={500} c="red">Danger zone</Text>
              <Text size="xs" c="dimmed">
                Deleting a project removes everything tied to it. <strong>This cannot be undone.</strong>
              </Text>
              {id && project && (
                <DeleteProjectButton projectId={id} projectName={project.name || id} />
              )}
            </Stack>
          </SettingsSection>
        </Tabs.Panel>
      </Tabs>
    </Shell>
  );

  // suppress unused warning when router isn't wired into a tab yet
  void router;
}

function formatRelativeTime(rfc3339: string): string {
  const t = new Date(rfc3339).getTime();
  if (Number.isNaN(t)) return rfc3339;
  const seconds = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (seconds < 60) return 'just now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} ${hours === 1 ? 'hour' : 'hours'} ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} ${days === 1 ? 'day' : 'days'} ago`;
  const months = Math.floor(days / 30);
  return `${months} ${months === 1 ? 'month' : 'months'} ago`;
}

function formatAbsoluteTime(rfc3339: string): string {
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339;
  return d.toLocaleString();
}

function ClearSchemaCacheButton({ projectId }: { projectId: string }) {
  const [opened, setOpened] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [info, setInfo] = useState<{ cached: boolean; last?: string } | null>(null);

  const refreshInfo = useCallback(async () => {
    try {
      const res = await api.getSchemaCacheInfo(projectId);
      setInfo({ cached: res.cached, last: res.last_cached_at });
    } catch {
      setInfo({ cached: false });
    }
  }, [projectId]);

  useEffect(() => { void refreshInfo(); }, [refreshInfo]);

  const handleConfirm = async () => {
    setSubmitting(true);
    try {
      await api.invalidateSchemaCache(projectId);
      notifications.show({
        title: 'Schema cache cleared',
        message: 'Project marked as needs_reindex. Click Re-index now on the project page when you\'re ready.',
        color: 'green',
      });
      setOpened(false);
      void refreshInfo();
    } catch (e: unknown) {
      notifications.show({ title: 'Could not clear schema cache', message: (e as Error).message, color: 'red' });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Group align="center">
        <Button variant="default" color="orange" onClick={() => setOpened(true)}>
          Clear schema cache
        </Button>
        <Text size="xs" c="dimmed">
          {info === null
            ? 'Loading cache info…'
            : info.cached && info.last
              ? `Last cached: ${formatRelativeTime(info.last)} (${formatAbsoluteTime(info.last)})`
              : 'No cache yet — next indexing run will discover schemas from the warehouse.'}
        </Text>
      </Group>
      <Modal
        opened={opened}
        onClose={() => { if (!submitting) setOpened(false); }}
        title="Clear schema cache?"
        centered
      >
        <Stack gap="sm">
          <Text size="sm">This resets the project&apos;s schema-discovery state:</Text>
          <ul style={{ margin: 0, paddingLeft: 20, fontSize: 14 }}>
            <li>Cached warehouse schema is deleted.</li>
            <li>The vector index in Qdrant is dropped.</li>
            <li>Project status is set to <strong>needs_reindex</strong>.</li>
          </ul>
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={() => setOpened(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button color="orange" onClick={handleConfirm} loading={submitting}>
              Yes, clear cache
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function DeleteProjectButton({ projectId, projectName }: { projectId: string; projectName: string }) {
  const router = useRouter();
  const [opened, setOpened] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [confirmText, setConfirmText] = useState('');

  const matches = confirmText === projectName;

  const handleConfirm = async () => {
    setSubmitting(true);
    try {
      const res = await api.deleteProject(projectId);
      notifications.show({
        title: 'Project deleted',
        message: `"${projectName}" and all related data have been removed.`,
        color: 'green',
      });
      if (res.secrets_skipped) {
        notifications.show({
          title: 'Action required',
          message: 'Warehouse and AI credentials are stored in an external secret manager. Remove them from your cloud console.',
          color: 'yellow',
          autoClose: 12000,
        });
      }
      router.push('/projects');
    } catch (e: unknown) {
      notifications.show({ title: 'Could not delete project', message: (e as Error).message, color: 'red', autoClose: 8000 });
      setSubmitting(false);
    }
  };

  return (
    <>
      <Group>
        <Button color="red" onClick={() => { setConfirmText(''); setOpened(true); }}>
          Delete project
        </Button>
      </Group>
      <Modal
        opened={opened}
        onClose={() => { if (!submitting) setOpened(false); }}
        title={<Text fw={600} c="red">Delete project?</Text>}
        centered
      >
        <Stack gap="sm">
          <Text size="sm">
            This permanently removes <strong>{projectName}</strong> and everything tied to it.
          </Text>
          <TextInput
            label={<>Type <strong>{projectName}</strong> to confirm</>}
            value={confirmText}
            onChange={(e) => setConfirmText(e.currentTarget.value)}
            placeholder={projectName}
            disabled={submitting}
            data-autofocus
          />
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={() => setOpened(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button color="red" onClick={handleConfirm} loading={submitting} disabled={!matches}>
              Delete project
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
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
