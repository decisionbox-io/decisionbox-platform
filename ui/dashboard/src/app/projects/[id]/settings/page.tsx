'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  ActionIcon, Alert, Button, Checkbox, CloseButton, Collapse, Divider, Group, Loader, Modal, MultiSelect,
  NumberInput, Select, Stack, Switch, Tabs, Text, TextInput, Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconCheck, IconPlus, IconPlugConnected, IconShieldCheck, IconX } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { DynamicField as CatalogAwareField, LiveModelCombobox, modelWireIsKnown } from '@/components/common/LLMModelField';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import { EmbeddingEditor, EmbeddingState, emptyEmbeddingState } from '@/components/EmbeddingEditor';
import { api, Project, ProviderMeta, EmbeddingProviderMeta, ConfigField, LiveModel, SecretEntryResponse, TestConnectionResult } from '@/lib/api';

export default function ProjectSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [warehouseProviders, setWarehouseProviders] = useState<ProviderMeta[]>([]);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
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
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);
  const [liveError, setLiveError] = useState<string | null>(null);
  const [liveLoading, setLiveLoading] = useState(false);
  const [showAdvancedLLM, setShowAdvancedLLM] = useState(false);
  // Monotonic id guards against out-of-order responses if the user
  // triggers multiple refreshes or the auto-refresh-on-mount overlaps
  // with a manual click.
  const liveReqIdRef = useRef(0);
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleCron, setScheduleCron] = useState('');
  const [maxSteps, setMaxSteps] = useState(100);
  // Debug-logs visibility is a client-side-only preference (it controls
  // whether the live-run panel on the project page renders the verbose
  // per-query debug tail). It's kept in localStorage, not on the project
  // document — no server round-trip needed, and nothing the agent cares
  // about. Keyed per-project so different projects can keep different
  // defaults.
  const [debugLogsEnabled, setDebugLogsEnabled] = useState(false);
  const [profile, setProfile] = useState<Record<string, Record<string, unknown>>>({});
  const [profileSchema, setProfileSchema] = useState<Record<string, unknown> | null>(null);
  const [secretsList, setSecretsList] = useState<SecretEntryResponse[]>([]);
  const [newSecretValue, setNewSecretValue] = useState('');
  const [newWhCredential, setNewWhCredential] = useState('');
  const [savingSecret, setSavingSecret] = useState(false);
  const [savingWhCredential, setSavingWhCredential] = useState(false);

  // Embedding state
  const [embeddingProviders, setEmbeddingProviders] = useState<EmbeddingProviderMeta[]>([]);
  // Unified embedding editor state. Parallels the blurb editor state
  // shape so both settings panels follow the same "edit here → save
  // via project PUT + secret rotation" pattern.
  const [embedding, setEmbedding] = useState<EmbeddingState>(emptyEmbeddingState);
  const [savingEmbKey, setSavingEmbKey] = useState(false);

  // Blurb LLM state — per-project override for the schema-indexing
  // model (PLAN-SCHEMA-RETRIEVAL.md §6.2). Disabled by default; the
  // agent falls back to project.llm + llm-api-key when blurb_llm is nil.
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  const [savingBlurbKey, setSavingBlurbKey] = useState(false);

  // Warn on browser close/refresh with unsaved changes
  useEffect(() => {
    const handler = (e: BeforeUnloadEvent) => {
      if (!dirty) return;
      e.preventDefault();
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty]);

  // Load the debug-logs preference from localStorage. Kept separate from
  // the project save cycle because this is purely a local UI preference —
  // doesn't go to the server, doesn't count as "dirty".
  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    setDebugLogsEnabled(window.localStorage.getItem(`db:showDebugLogs:${id}`) === '1');
  }, [id]);

  // Active tab — defaults to "general" but honours `location.hash` so
  // deep-links like `/projects/:id/settings#advanced` open the right tab.
  // The set of valid tab values must match the `<Tabs.Tab value=...>` IDs
  // below; an unknown hash is ignored.
  const validTabs = ['general', 'warehouse', 'ai', 'blurb', 'embedding', 'schedule', 'profile', 'advanced'];
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

  // Intercept client-side navigation when dirty
  const router = useRouter();
  const guardedNavigate = useCallback((href: string) => {
    if (!dirty || window.confirm('You have unsaved changes. Leave without saving?')) {
      router.push(href);
    }
  }, [dirty, router]);

  // Intercept sidebar/breadcrumb link clicks when dirty
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: MouseEvent) => {
      const anchor = (e.target as HTMLElement).closest('a[href]') as HTMLAnchorElement | null;
      if (!anchor) return;
      const href = anchor.getAttribute('href');
      if (!href || href.startsWith('http') || href.startsWith('#')) return;
      // Allow clicks within the same settings page
      if (href.includes('/settings')) return;
      e.preventDefault();
      e.stopPropagation();
      guardedNavigate(href);
    };
    document.addEventListener('click', handler, true);
    return () => document.removeEventListener('click', handler, true);
  }, [dirty, guardedNavigate]);

  useEffect(() => {
    Promise.all([
      api.getProject(id),
      api.listWarehouseProviders(),
      api.listLLMProviders(),
      api.listEmbeddingProviders(),
    ])
      .then(([proj, whProvs, llmProvs, embProvs]) => {
        setProject(proj);
        setWarehouseProviders(whProvs);
        setLlmProviders(llmProvs);
        setEmbeddingProviders(embProvs || []);

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

        // Auto-refresh the model list on page load so the settings
        // picker is immediately populated. We swallow any error here —
        // the refresh button is visible for retry and the user can
        // still type a model ID by hand.
        if (proj.llm.provider) {
          const reqId = ++liveReqIdRef.current;
          api.listLiveLLMModelsForProject(proj.id)
            .then((resp) => {
              if (reqId !== liveReqIdRef.current) return;
              setLiveModels(resp.models);
              if (resp.live_error) setLiveError(resp.live_error);
            })
            .catch((e) => {
              if (reqId !== liveReqIdRef.current) return;
              setLiveError((e as Error).message);
            });
        }
        setEmbedding({
          provider: proj.embedding?.provider || '',
          model: proj.embedding?.model || '',
          config: {},
          // Saved key never round-trips back from the server — user
          // re-enters only when rotating.
          apiKey: '',
        });
        // Blurb LLM: hydrate from the saved project doc. When
        // blurb_llm is nil the editor renders "use analysis LLM".
        if (proj.blurb_llm && proj.blurb_llm.provider) {
          setBlurb({
            enabled: true,
            provider: proj.blurb_llm.provider,
            model: proj.blurb_llm.model || '',
            config: proj.blurb_llm.config || {},
            apiKey: '', // API key is never sent back — user re-enters to rotate.
          });
        } else {
          setBlurb(emptyBlurbLLMState());
        }
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

      const saved = await api.updateProject(id, {
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
        // When the user toggles the blurb override off, send blurb_llm
        // with empty strings — the Go JSON unmarshaller drops it with
        // `omitempty` via the pointer type, which tells the indexer to
        // fall back to the analysis LLM on the next run.
        blurb_llm:
          blurb.enabled && blurb.provider && blurb.model
            ? {
                provider: blurb.provider,
                model: blurb.model,
                config: Object.fromEntries(
                  Object.entries(blurb.config).filter(([k]) => k !== 'model' && k !== 'api_key')
                ),
              }
            : undefined,
        embedding: { provider: embedding.provider, model: embedding.model },
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
        profile,
      });

      // Rotate the blurb-LLM api key if the user entered one. A blank
      // field means "leave the stored key alone" — we never auto-delete
      // the secret on save.
      if (blurb.enabled && blurb.apiKey) {
        try {
          setSavingBlurbKey(true);
          await api.setSecret(id, 'blurb-llm-api-key', blurb.apiKey);
          setBlurb((prev) => ({ ...prev, apiKey: '' }));
        } catch (e: unknown) {
          notifications.show({ title: 'Blurb LLM key save failed', message: (e as Error).message, color: 'red' });
        } finally {
          setSavingBlurbKey(false);
        }
      }

      // Same pattern for the embedding api key — blank field means
      // "keep the stored key", any value rotates.
      if (embedding.apiKey) {
        try {
          setSavingEmbKey(true);
          await api.setSecret(id, 'embedding-api-key', embedding.apiKey);
          setEmbedding((prev) => ({ ...prev, apiKey: '' }));
          // Refresh the secrets list so the panel's "Current key" hint
          // reflects the new mask without a page reload.
          api.listSecrets(id).then((s) => setSecretsList(s || [])).catch(() => {});
        } catch (e: unknown) {
          notifications.show({ title: 'Embedding key save failed', message: (e as Error).message, color: 'red' });
        } finally {
          setSavingEmbKey(false);
        }
      }

      // Sync local project state with the saved payload so derived
      // flags (e.g. setupMode = llmProvider !== project.llm.provider)
      // recompute correctly without a page reload. The API returns
      // the updated project; fall back to a merge when it doesn't.
      setProject((prev) => {
        if (saved) return saved;
        if (!prev) return prev;
        return {
          ...prev,
          name, description,
          warehouse: { ...prev.warehouse, provider: whProvider, datasets: datasetsList, location: whConfig['location'] || '', filter_field: filterField, filter_value: filterValue, project_id: whConfig['project_id'] || '', config: prev.warehouse.config },
          llm: { provider: llmProvider, model: llmModel, config: llmConfig },
          embedding: { ...(prev.embedding || {}), provider: embedding.provider, model: embedding.model },
          schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
          profile,
        };
      });

      setDirty(false);

      // If the provider changed, we're now entering normal mode and
      // want the new provider's live model list right away. Kick off
      // an auto-refresh so the model picker is populated without
      // requiring the user to click Refresh.
      if (saved && saved.llm?.provider) {
        const reqId = ++liveReqIdRef.current;
        api.listLiveLLMModelsForProject(saved.id)
          .then((resp) => {
            if (reqId !== liveReqIdRef.current) return;
            setLiveModels(resp.models);
            if (resp.live_error) setLiveError(resp.live_error);
            else setLiveError(null);
          })
          .catch((e) => {
            if (reqId !== liveReqIdRef.current) return;
            setLiveError((e as Error).message);
          });
      }

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
      {/* `value` + `onChange` (not `defaultValue`) so the tab is
          controlled; `useEffect` below reads `location.hash` on mount
          and on hashchange, letting deep-links like
          `/projects/:id/settings#advanced` open the right tab. */}
      <Tabs value={activeTab} onChange={(v) => { if (v) setActiveTab(v); }} styles={{
        tab: { fontSize: 13, fontWeight: 500, padding: '8px 16px' },
        panel: { paddingTop: 20 },
      }}>
        <Tabs.List>
          <Tabs.Tab value="general">General</Tabs.Tab>
          <Tabs.Tab value="warehouse">Data Warehouse</Tabs.Tab>
          <Tabs.Tab value="ai">AI Provider</Tabs.Tab>
          <Tabs.Tab value="blurb">Blurb Model</Tabs.Tab>
          <Tabs.Tab value="embedding">Embedding &amp; Search</Tabs.Tab>
          <Tabs.Tab value="schedule">Schedule</Tabs.Tab>
          {profileSchema && <Tabs.Tab value="profile">Profile</Tabs.Tab>}
          <Tabs.Tab value="advanced">Advanced</Tabs.Tab>
        </Tabs.List>

        {/* General */}
        <Tabs.Panel value="general">
          <SettingsSection>
            <TextInput label="Project Name" required value={name} onChange={(e) => { setName(e.target.value); setDirty(true); }} />
            <Textarea label="Description" value={description} onChange={(e) => { setDescription(e.target.value); setDirty(true); }} />
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
              value={whProvider} onChange={(v) => { setWhProvider(v || ''); setDirty(true); }} />
            {selectedWh?.description && <Text size="xs" c="dimmed">{selectedWh.description}</Text>}

            {selectedWh?.config_fields
              .filter((f) => f.key !== 'dataset')
              .map((field) => (
                <DynamicField key={field.key} field={field}
                  value={whConfig[field.key] || ''}
                  onChange={(val) => { setWhConfig((prev) => ({ ...prev, [field.key]: val })); setDirty(true); }} />
              ))}

            {(selectedWh?.auth_methods?.length ?? 0) > 0 && (
              <>
                <Select label="Authentication" size="xs"
                  key={`auth-${whProvider}`}
                  data={selectedWh!.auth_methods!.map((m) => ({ value: m.id, label: m.name }))}
                  value={whConfig['auth_method'] || ''}
                  onChange={(v) => {
                    // Clear stale keys from previous auth method
                    const allAuthKeys: string[] = [];
                    for (const m of selectedWh!.auth_methods!) {
                      for (const f of (m.fields || [])) { allAuthKeys.push(f.key); }
                    }
                    setWhConfig((prev) => {
                      const next: Record<string, string> = { ...prev, auth_method: v || '' };
                      for (const k of allAuthKeys) { delete next[k]; }
                      return next;
                    });
                    setDirty(true);
                  }} />
                {(() => {
                  const am = selectedWh!.auth_methods!.find((m) => m.id === whConfig['auth_method']);
                  if (!am) return null;
                  const fields = am.fields || [];
                  const configFields = fields.filter((f) => f.type !== 'credential');
                  const credField = fields.find((f) => f.type === 'credential');
                  return (
                    <>
                      {am.description && <Text size="xs" c="dimmed">{am.description}</Text>}
                      {configFields.map((field) => (
                        <DynamicField key={field.key} field={field}
                          value={whConfig[field.key] || ''}
                          onChange={(val) => { setWhConfig((prev) => ({ ...prev, [field.key]: val })); setDirty(true); }} />
                      ))}
                      {credField && (
                        <>
                          {secretsList.some((s) => s.key === 'warehouse-credentials') && (
                            <div style={{ borderRadius: 'var(--db-radius)', background: 'var(--db-bg-muted)', padding: 8 }}>
                              <Group gap="xs">
                                <IconShieldCheck size={14} color="var(--db-green-text)" />
                                <Text size="xs" fw={500}>{credField.label} saved</Text>
                                <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>
                                  {secretsList.find((s) => s.key === 'warehouse-credentials')?.masked}
                                </Text>
                              </Group>
                            </div>
                          )}
                          <Textarea size="xs"
                            label={`Update ${credField.label}`}
                            placeholder={credField.placeholder || `Enter ${credField.label.toLowerCase()}`}
                            description={(credField.description || '') + ' Stored encrypted. Leave empty to keep current.'}
                            value={newWhCredential}
                            onChange={(e) => setNewWhCredential(e.target.value)}
                            minRows={2} autosize
                            styles={{ input: { fontFamily: 'monospace', fontSize: '12px' } }}
                          />
                          <Button size="xs" loading={savingWhCredential} disabled={!newWhCredential}
                            onClick={async () => {
                              setSavingWhCredential(true);
                              try {
                                await api.setSecret(id, 'warehouse-credentials', newWhCredential);
                                setNewWhCredential('');
                                notifications.show({ title: 'Saved', message: 'Warehouse credentials updated', color: 'green' });
                                const updated = await api.listSecrets(id);
                                setSecretsList(updated || []);
                              } catch (e: unknown) {
                                notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
                              } finally {
                                setSavingWhCredential(false);
                              }
                            }}>
                            Update Credential
                          </Button>
                        </>
                      )}
                    </>
                  );
                })()}
              </>
            )}

            <TextInput label="Datasets" description="Comma-separated dataset names"
              placeholder="events_prod, features_prod"
              value={datasets} onChange={(e) => { setDatasets(e.target.value); setDirty(true); }} />

            <div style={{ fontSize: 13, fontWeight: 500, marginTop: 8 }}>Filter (optional)</div>
            <Text size="xs" c="dimmed">For shared datasets. Leave empty if the entire dataset is yours.</Text>
            <Group grow>
              <TextInput label="Filter Field" placeholder="app_id" value={filterField}
                onChange={(e) => { setFilterField(e.target.value); setDirty(true); }} />
              <TextInput label="Filter Value" placeholder="my-app-123" value={filterValue}
                onChange={(e) => { setFilterValue(e.target.value); setDirty(true); }} />
            </Group>

            <TestConnectionButton projectId={id} target="warehouse" disabled={dirty} />
          </SettingsSection>
        </Tabs.Panel>

        {/* AI Provider */}
        <Tabs.Panel value="ai">
          <SettingsSection>
            {(() => {
              // Two-mode layout:
              //
              //   setupMode (provider changed, not saved yet) — show
              //   only provider select + connection params + API key.
              //   Hide model picker / refresh / wire_override / test
              //   connection; those need a saved provider to be useful.
              //   A single banner tells the user to save to continue.
              //
              //   normalMode (provider matches saved) — full UI.
              const savedProvider = project.llm.provider || '';
              const setupMode = llmProvider !== savedProvider;

              const providerSelect = (
                <>
                  <Select
                    label="LLM Provider"
                    data={llmProviders.map((p) => ({ value: p.id, label: p.name }))}
                    value={llmProvider}
                    onChange={(v) => {
                      setLlmProvider(v || '');
                      setLlmModel('');
                      setLlmConfig({});
                      // Cancel any in-flight live-list request from the
                      // old provider so its late-landing response can't
                      // overwrite our cleared state.
                      liveReqIdRef.current++;
                      setLiveModels(null);
                      setLiveError(null);
                      setDirty(true);
                    }}
                  />
                  {selectedLlm?.description && (
                    <Text size="xs" c="dimmed">{selectedLlm.description}</Text>
                  )}
                </>
              );

              const connectionParams = selectedLlm?.config_fields
                .filter((f) => f.key !== 'model' && f.key !== 'api_key' && f.key !== 'wire_override')
                .map((field) => (
                  <CatalogAwareField
                    key={field.key}
                    field={field}
                    providerMeta={selectedLlm}
                    value={llmConfig[field.key] || ''}
                    onChange={(val) => { setLlmConfig((prev) => ({ ...prev, [field.key]: val })); setDirty(true); }}
                  />
                ));

              const apiKeySection = selectedLlm?.config_fields.some((f) => f.key === 'api_key') ? (
                <>
                  {secretsList.some((s) => s.key === 'llm-api-key') && !setupMode && (
                    <div style={{ borderRadius: 'var(--db-radius)', background: 'var(--db-bg-muted)', padding: 8 }}>
                      <Group gap="xs">
                        <IconShieldCheck size={14} color="var(--db-green-text)" />
                        <Text size="xs" fw={500}>API Key saved</Text>
                        <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>
                          {secretsList.find((s) => s.key === 'llm-api-key')?.masked}
                        </Text>
                      </Group>
                    </div>
                  )}
                  <Group gap="xs" align="end">
                    <TextInput
                      label={setupMode ? 'API Key' : 'Update API Key'}
                      size="xs"
                      style={{ flex: 1 }}
                      placeholder="Enter API key"
                      value={newSecretValue}
                      onChange={(e) => setNewSecretValue(e.target.value)}
                      type="password"
                      description="Stored encrypted. Leave empty to keep current."
                    />
                    <Button
                      size="xs"
                      loading={savingSecret}
                      disabled={!newSecretValue}
                      onClick={async () => {
                        setSavingSecret(true);
                        try {
                          await api.setSecret(id, 'llm-api-key', newSecretValue);
                          setNewSecretValue('');
                          notifications.show({ title: 'Saved', message: 'LLM API key updated', color: 'green' });
                          const updated = await api.listSecrets(id);
                          setSecretsList(updated || []);
                        } catch (e: unknown) {
                          notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
                        } finally {
                          setSavingSecret(false);
                        }
                      }}
                    >
                      {setupMode ? 'Save Key' : 'Update Key'}
                    </Button>
                  </Group>
                </>
              ) : selectedLlm ? (
                <Text size="xs" c="dimmed">This provider uses cloud credentials. No API key needed.</Text>
              ) : null;

              if (setupMode) {
                return (
                  <>
                    {providerSelect}
                    {llmProvider && (
                      <>
                        <Alert color="blue" icon={<IconAlertCircle size={16} />} title="Finish switching to this provider">
                          <Text size="sm">
                            Fill in the connection details below and click <b>Save settings</b> in the top bar.
                            The model picker and connection test will appear after saving.
                          </Text>
                        </Alert>
                        {connectionParams}
                        {apiKeySection}
                      </>
                    )}
                  </>
                );
              }

              // Normal mode — provider saved, show everything.
              return (
                <>
                  {providerSelect}
                  {connectionParams}
                  {apiKeySection}

                  {selectedLlm && (
                    <LiveModelCombobox
                      providerMeta={selectedLlm}
                      liveModels={liveModels}
                      value={llmModel}
                      onChange={(val) => { setLlmModel(val); setDirty(true); }}
                    />
                  )}

                  {selectedLlm && (
                    <Group gap="xs">
                      <Button
                        size="xs"
                        variant="subtle"
                        loading={liveLoading}
                        disabled={dirty}
                        onClick={async () => {
                          const reqId = ++liveReqIdRef.current;
                          setLiveLoading(true);
                          setLiveError(null);
                          try {
                            const resp = await api.listLiveLLMModelsForProject(id);
                            if (reqId !== liveReqIdRef.current) return;
                            setLiveModels(resp.models);
                            if (resp.live_error) setLiveError(resp.live_error);
                            const fromUpstream = resp.models.filter((m) => m.source === 'live' || m.source === 'both').length;
                            notifications.show({
                              title: fromUpstream > 0 ? 'Models refreshed' : 'Live fetch returned no models',
                              message:
                                fromUpstream > 0
                                  ? `${fromUpstream} model${fromUpstream === 1 ? '' : 's'} loaded`
                                  : resp.live_error
                                    ? 'Upstream rejected the request — see details below.'
                                    : 'Upstream returned zero models for your region/credentials.',
                              color: fromUpstream > 0 ? 'green' : 'orange',
                            });
                          } catch (e: unknown) {
                            if (reqId !== liveReqIdRef.current) return;
                            setLiveError((e as Error).message);
                            notifications.show({ title: 'Could not refresh', message: (e as Error).message, color: 'orange' });
                          } finally {
                            if (reqId === liveReqIdRef.current) setLiveLoading(false);
                          }
                        }}
                      >
                        Refresh model list
                      </Button>
                      {dirty ? (
                        <Text size="xs" c="dimmed">Save changes before refreshing.</Text>
                      ) : liveModels !== null ? (
                        (() => {
                          const fromUpstream = liveModels.filter((m) => m.source === 'live' || m.source === 'both').length;
                          return (
                            <Text size="xs" c="dimmed">
                              {fromUpstream} model{fromUpstream === 1 ? '' : 's'} · refreshed from provider
                            </Text>
                          );
                        })()
                      ) : null}
                    </Group>
                  )}
                  {liveError && (
                    <Alert color="orange" icon={<IconAlertCircle size={16} />} title="Could not fetch live model list">
                      {liveError}
                    </Alert>
                  )}

                  {(() => {
                    const wireField = selectedLlm?.config_fields.find((f) => f.key === 'wire_override');
                    if (!wireField) return null;
                    const wireKnown = modelWireIsKnown(liveModels, selectedLlm ?? null, llmModel);
                    const renderField = (
                      <CatalogAwareField
                        field={wireField}
                        providerMeta={selectedLlm}
                        value={llmConfig[wireField.key] || ''}
                        onChange={(val) => { setLlmConfig((prev) => ({ ...prev, [wireField.key]: val })); setDirty(true); }}
                      />
                    );
                    if (!wireKnown) return renderField;
                    return (
                      <>
                        <Button
                          variant="subtle"
                          size="xs"
                          onClick={() => setShowAdvancedLLM((v) => !v)}
                          style={{ alignSelf: 'flex-start' }}
                        >
                          {showAdvancedLLM ? 'Hide advanced settings' : 'Advanced settings'}
                        </Button>
                        <Collapse in={showAdvancedLLM}>{renderField}</Collapse>
                      </>
                    );
                  })()}

                  <TestConnectionButton projectId={id} target="llm" disabled={dirty} />
                </>
              );
            })()}
          </SettingsSection>
        </Tabs.Panel>

        {/* Blurb LLM — schema-indexing model (plan §6.2) */}
        <Tabs.Panel value="blurb">
          <SettingsSection>
            <Text size="sm" fw={500}>Blurb Model</Text>
            <Text size="xs" c="dimmed" mb="sm">
              The LLM used during schema indexing to generate the per-table
              descriptions that get embedded into Qdrant. By default this
              reuses your analysis LLM; override here to pick a cheaper /
              faster model (spike defaults: Bedrock <code>qwen.qwen3-32b-v1:0</code>,
              OpenAI <code>gpt-4.1-nano</code>). Changes apply to the next
              re-index — click <b>Re-index schema</b> on the project page
              after saving.
            </Text>
            <BlurbLLMEditor
              llmProviders={llmProviders}
              value={blurb}
              onChange={(next) => { setBlurb(next); setDirty(true); }}
              startInModelPhase={!!project?.blurb_llm?.provider}
              footer={
                savingBlurbKey ? (
                  <Text size="xs" c="dimmed">Saving blurb LLM key…</Text>
                ) : null
              }
            />
          </SettingsSection>
        </Tabs.Panel>

        {/* Embedding & Search */}
        <Tabs.Panel value="embedding">
          <SettingsSection>
            <Text size="sm" fw={500}>Embedding Provider</Text>
            <Text size="xs" c="dimmed" mb="sm">
              Required for schema indexing and semantic search. Use the same picker as the LLM provider — pick a provider, enter credentials, load models.
              {secretsList.some(s => s.key === 'embedding-api-key') ? (
                <> Current key: <b>{secretsList.find(s => s.key === 'embedding-api-key')?.masked}</b>. Leave the key field blank to keep it.</>
              ) : null}
            </Text>
            <EmbeddingEditor
              providers={embeddingProviders}
              value={embedding}
              onChange={(next) => { setEmbedding(next); setDirty(true); }}
              startInModelPhase={!!project?.embedding?.provider}
            />
            {savingEmbKey && <Text size="xs" c="dimmed" mt="xs">Saving embedding API key…</Text>}
          </SettingsSection>
        </Tabs.Panel>

        {/* Schedule */}
        <Tabs.Panel value="schedule">
          <SettingsSection>
            <Switch label="Enable automatic discovery" checked={scheduleEnabled}
              onChange={(e) => { setScheduleEnabled(e.currentTarget.checked); setDirty(true); }} />
            {scheduleEnabled && (
              <TextInput label="Cron Expression" value={scheduleCron}
                onChange={(e) => { setScheduleCron(e.target.value); setDirty(true); }} description="e.g., 0 2 * * * (daily at 2 AM)" />
            )}
            <NumberInput label="Max Exploration Steps" value={maxSteps}
              onChange={(v) => { setMaxSteps(Number(v) || 100); setDirty(true); }} min={10} max={500} />
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

        {/* Advanced — client-side UI preferences + debug tooling. Nothing
            here goes to the server; toggles are stored in localStorage and
            do not count as "unsaved changes". */}
        <Tabs.Panel value="advanced">
          <SettingsSection>
            <Stack gap="sm">
              <Text size="sm" fw={500}>Debugging</Text>
              <Switch
                label="Show debug logs during discovery and indexing"
                description="Adds a verbose per-query + per-LLM-call tail to the live discovery panel, and a live agent-stderr tail to the schema-index panel on this project's page. Useful for troubleshooting stalled runs or watching what the agent is doing in real time."
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
                This is a local-browser preference — not shared with other users and not saved on the project. The indexing log tail is always captured server-side for 7 days; this toggle only controls whether the UI renders it.
              </Text>
              <Divider my="xs" />
              <Text size="sm" fw={500}>Schema cache</Text>
              <Text size="xs" c="dimmed">
                The agent caches the discovered warehouse schema (table list, columns, row counts) so re-runs skip the full catalog pass. Clearing the cache also drops the Qdrant index and resets the project to <strong>needs indexing</strong> — discovery will be blocked until a fresh reindex completes. Use this when row counts have drifted or the warehouse schema has changed in ways the cache wouldn&apos;t detect.
              </Text>
              {id && <ClearSchemaCacheButton projectId={id} />}
            </Stack>
          </SettingsSection>
        </Tabs.Panel>
      </Tabs>
    </Shell>
  );
}

// formatRelativeTime renders a small "Last cached: 3 hours ago" string
// next to the Clear button. Truncated to single-unit precision —
// users only care about freshness order-of-magnitude.
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

// formatAbsoluteTime renders the local datetime in parens after the
// relative one, so users can hover-or-not but still see exact time.
function formatAbsoluteTime(rfc3339: string): string {
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339;
  return d.toLocaleString();
}

// ClearSchemaCacheButton wraps the invalidate-cache call in a confirm
// modal so a misclick can't silently throw away discovery work. The
// confirmation copy spells out what the cache stores and what happens
// next — the user often clicks this *because* they've changed something
// and want fresh counts, so we don't surprise them.
function ClearSchemaCacheButton({ projectId }: { projectId: string }) {
  const [opened, setOpened] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [info, setInfo] = useState<{ cached: boolean; last?: string } | null>(null);

  const refreshInfo = useCallback(async () => {
    try {
      const res = await api.getSchemaCacheInfo(projectId);
      setInfo({ cached: res.cached, last: res.last_cached_at });
    } catch {
      // Endpoint failures here aren't user-actionable — fall back to
      // hiding the timestamp line. The Clear button still works.
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
      const msg = (e as Error).message || 'Unknown error';
      notifications.show({
        title: 'Could not clear schema cache',
        message: msg,
        color: 'red',
      });
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
          <Text size="sm">
            This resets the project&apos;s schema-discovery state:
          </Text>
          <ul style={{ margin: 0, paddingLeft: 20, fontSize: 14 }}>
            <li>Cached warehouse schema (table list, columns, row counts) is deleted.</li>
            <li>The vector index in Qdrant is dropped.</li>
            <li>Project status is set to <strong>needs_reindex</strong>, blocking discovery until you re-index.</li>
          </ul>
          <Text size="sm" c="dimmed">
            Reindexing is <strong>not</strong> started automatically — you control when it runs (warehouse access, off-peak hours, etc). Click <strong>Re-index now</strong> on the project page when you&apos;re ready; for ERP-scale schemas this can take 30–60 minutes. Past discoveries, insights and recommendations stay untouched.
          </Text>
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

function TestConnectionButton({ projectId, target, disabled }: {
  projectId: string; target: 'warehouse' | 'llm'; disabled?: boolean;
}) {
  const [status, setStatus] = useState<'idle' | 'testing' | 'success' | 'error'>('idle');
  const [errorMsg, setErrorMsg] = useState('');

  const label = target === 'warehouse' ? 'Test Warehouse Connection' : 'Test AI Provider Connection';

  const handleTest = async () => {
    setStatus('testing');
    setErrorMsg('');
    try {
      const result: TestConnectionResult = target === 'warehouse'
        ? await api.testWarehouse(projectId)
        : await api.testLLM(projectId);

      if (result.success) {
        setStatus('success');
        notifications.show({ title: 'Connection successful', message: `${result.provider} is reachable`, color: 'green' });
      } else {
        setStatus('error');
        setErrorMsg(result.error || 'Unknown error');
      }
    } catch (e: unknown) {
      setStatus('error');
      setErrorMsg((e as Error).message);
    }
  };

  return (
    <div style={{ marginTop: 4 }}>
      <Group gap="sm" align="center">
        <button onClick={handleTest} disabled={disabled || status === 'testing'} style={{
          display: 'inline-flex', alignItems: 'center', gap: 6,
          background: 'none', color: 'var(--db-text-primary)',
          border: '1px solid var(--db-border-default)', borderRadius: 6,
          padding: '5px 12px', fontSize: 13, fontWeight: 500, fontFamily: 'inherit',
          cursor: disabled || status === 'testing' ? 'default' : 'pointer',
          opacity: disabled || status === 'testing' ? 0.5 : 1,
          transition: 'all 120ms ease',
        }}
        onMouseEnter={e => { if (!disabled && status !== 'testing') e.currentTarget.style.borderColor = 'var(--db-border-strong)'; }}
        onMouseLeave={e => { e.currentTarget.style.borderColor = 'var(--db-border-default)'; }}
        >
          {status === 'testing' ? <Loader size={14} /> : <IconPlugConnected size={14} />}
          {status === 'testing' ? 'Testing...' : label}
        </button>
        {status === 'success' && <IconCheck size={16} color="var(--db-green-text)" />}
        {status === 'error' && <IconX size={16} color="var(--db-red-text)" />}
      </Group>
      {disabled && <Text size="xs" c="dimmed">Save settings first to test the connection.</Text>}
      {status === 'error' && errorMsg && (
        <Text size="xs" c="red" mt={6} style={{ maxWidth: 560, wordBreak: 'break-word' }}>{errorMsg}</Text>
      )}
    </div>
  );
}

function DynamicField({ field, value, onChange }: { field: ConfigField; value: string; onChange: (v: string) => void }) {
  if (field.type === 'textarea') {
    return (
      <Textarea label={field.label} required={field.required}
        placeholder={field.placeholder || field.default} description={field.description}
        value={value} onChange={(e) => onChange(e.target.value)}
        minRows={6} autosize
        styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }} />
    );
  }
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
