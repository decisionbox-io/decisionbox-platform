'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert, Button, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconCheck, IconPlugConnected, IconShieldCheck, IconX } from '@tabler/icons-react';
import { api, Project, ProviderMeta, SecretEntryResponse, TestConnectionResult } from '@/lib/api';
import {
  WarehouseFormFields,
  WarehouseFormState,
  emptyWarehouseFormState,
  buildDefaults,
} from './WarehouseFormFields';

type Variant = 'page' | 'wizard';

export interface WarehouseConfigPanelProps {
  projectId: string;
  variant: Variant;
  // Fired after a successful save. The wizard uses this to advance to the
  // next step; the settings page can leave it undefined.
  onSaved?: (project: Project) => void;
}

function PanelSection({ children }: { children: React.ReactNode }) {
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

// WarehouseConfigPanel manages the load + save lifecycle for a project's
// warehouse configuration. Form rendering is delegated to
// `WarehouseFormFields` so this panel and the new-project wizard share a
// single source of truth for field layout, defaults, and auth-method
// rendering. Used by:
//   - settings/page.tsx          (variant="page")
//   - pack-gen wizard            (variant="wizard")
export default function WarehouseConfigPanel({ projectId, variant, onSaved }: WarehouseConfigPanelProps) {
  const [project, setProject] = useState<Project | null>(null);
  const [providers, setProviders] = useState<ProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const [form, setForm] = useState<WarehouseFormState>(emptyWarehouseFormState);
  const [secretsList, setSecretsList] = useState<SecretEntryResponse[]>([]);

  const loadOnce = useRef(false);
  useEffect(() => {
    if (loadOnce.current) return;
    loadOnce.current = true;
    Promise.all([api.getProject(projectId), api.listWarehouseProviders()])
      .then(([proj, whProvs]) => {
        setProject(proj);
        setProviders(whProvs);
        setForm(projectToFormState(proj, whProvs));
        api.listSecrets(projectId).then((s) => setSecretsList(s || [])).catch(() => {});
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [projectId]);

  const datasetsList = useMemo(
    () => (form.config['dataset'] || '').split(',').map((d) => d.trim()).filter(Boolean),
    [form.config],
  );

  const selected = providers.find((p) => p.id === form.provider);
  const authMethods = selected?.auth_methods || [];
  const selectedAuth = authMethods.find((m) => m.id === form.authMethod);
  const credField = selectedAuth?.fields?.find((f) => f.type === 'credential');
  const hasSavedCredential = secretsList.some((s) => s.key === 'warehouse-credentials');

  const isValid = Boolean(
    form.provider &&
    datasetsList.length > 0 &&
    (authMethods.length === 0 || form.authMethod) &&
    (!credField?.required || form.credential || hasSavedCredential),
  );

  const handleSave = async () => {
    if (!project) return;
    setSaving(true);
    try {
      const saved = await api.updateProject(projectId, {
        warehouse: {
          provider: form.provider,
          project_id: form.config['project_id'] || '',
          datasets: datasetsList,
          location: form.config['location'] || '',
          filter_field: form.filterField,
          filter_value: form.filterValue,
          config: {
            ...Object.fromEntries(
              Object.entries(form.config).filter(([k]) => k !== 'project_id' && k !== 'location' && k !== 'dataset'),
            ),
            ...(form.authMethod ? { auth_method: form.authMethod } : {}),
          },
        },
      });
      if (form.credential) {
        await api.setSecret(projectId, 'warehouse-credentials', form.credential);
        setForm((prev) => ({ ...prev, credential: '' }));
        const updated = await api.listSecrets(projectId);
        setSecretsList(updated || []);
      }
      notifications.show({ title: 'Saved', message: 'Warehouse configuration updated', color: 'green' });
      setProject(saved);
      onSaved?.(saved);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <Loader />;
  if (error) return <Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert>;
  if (!project) return <Text>Project not found</Text>;

  const header = variant === 'page' ? (
    <Stack gap={2} mb="sm">
      <Title order={4}>Data Warehouse</Title>
      <Text size="xs" c="dimmed">Connection details the agent uses to read schemas and run queries.</Text>
    </Stack>
  ) : null;

  return (
    <PanelSection>
      {header}

      {hasSavedCredential && credField && (
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

      <WarehouseFormFields
        providers={providers}
        value={form}
        onChange={setForm}
        hasSavedCredential={hasSavedCredential}
      />

      <TestConnectionButton projectId={projectId} target="warehouse" />

      <Group justify="flex-end" mt="sm">
        <Button onClick={handleSave} loading={saving} disabled={!isValid}>
          {variant === 'wizard' ? 'Save and continue' : 'Save warehouse'}
        </Button>
      </Group>
    </PanelSection>
  );
}

function projectToFormState(proj: Project, providers: ProviderMeta[]): WarehouseFormState {
  const provMeta = providers.find((p) => p.id === proj.warehouse.provider);
  const fieldDefaults = provMeta ? buildDefaults(provMeta.config_fields) : {};
  const datasetsJoined = (proj.warehouse.datasets || []).join(', ');
  const cfg: Record<string, string> = {
    ...fieldDefaults,
    project_id: proj.warehouse.project_id || '',
    location: proj.warehouse.location || '',
    ...(proj.warehouse.config || {}),
    ...(datasetsJoined ? { dataset: datasetsJoined } : {}),
  };
  // auth_method lives inside warehouse.config but the form-fields treat
  // it as a top-level field — peel it off so the form state is shaped
  // correctly.
  const authMethod = cfg['auth_method'] || '';
  delete cfg['auth_method'];
  return {
    provider: proj.warehouse.provider || '',
    config: cfg,
    authMethod,
    credential: '',
    filterField: proj.warehouse.filter_field || '',
    filterValue: proj.warehouse.filter_value || '',
  };
}

function TestConnectionButton({ projectId, target }: { projectId: string; target: 'warehouse' | 'llm' }) {
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
        <Button variant="default" size="xs" onClick={handleTest} disabled={status === 'testing'}
          leftSection={status === 'testing' ? <Loader size={14} /> : <IconPlugConnected size={14} />}>
          {status === 'testing' ? 'Testing...' : label}
        </Button>
        {status === 'success' && <IconCheck size={16} color="var(--db-green-text)" />}
        {status === 'error' && <IconX size={16} color="var(--db-red-text)" />}
      </Group>
      {status === 'error' && errorMsg && (
        <Text size="xs" c="red" mt={6} style={{ maxWidth: 560, wordBreak: 'break-word' }}>{errorMsg}</Text>
      )}
    </div>
  );
}
