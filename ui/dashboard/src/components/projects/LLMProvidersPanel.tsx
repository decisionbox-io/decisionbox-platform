'use client';

/**
 * The project's LLM provider entries: one or more named connections the
 * per-layer model picker chooses from.
 *
 * An entry is a CONNECTION — provider, auth method, region / base URL / TLS —
 * and nothing else. The model is a per-layer choice and lives in
 * <LayerModelsPanel>; keeping the two apart is what lets several layers share
 * one connection, which is what keeps a project on one model down to a single
 * provider instance at run time.
 *
 * The reserved "default" entry is a SLOT, not a pointer: "make this the
 * default" rewrites it. An agent, sources-worker or fileingestion-worker
 * image older than this release resolves its credential from the fixed
 * llm-credentials key, so if any entry could be the default, promoting a
 * secondary would leave those images reading the wrong key — and failing
 * rather than degrading.
 *
 * Nothing here re-implements provider rendering: each entry composes the
 * shared <ProviderCredentialsPhase>, the same component the embedding editor
 * and the creation wizard use.
 */

import { useState } from 'react';
import {
  ActionIcon, Alert, Badge, Button, Card, Group, Stack, Text, TextInput, Tooltip,
} from '@mantine/core';
import { IconAlertCircle, IconPlus, IconShieldCheck, IconTrash } from '@tabler/icons-react';
import {
  api, DEFAULT_LLM_PROVIDER_ID, LLMProviderEntry, LiveModel, MAX_LLM_PROVIDER_ID_LEN,
  ProviderMeta, SecretEntryResponse, isValidLLMProviderId, llmProviderSecretKey,
} from '@/lib/api';
import {
  CredentialsPhaseValue, ProviderCredentialsPhase, emptyCredentials,
} from '@/components/ProviderCredentialsPhase';
import { TestConnectionButton } from './WarehouseConfigPanel';

/** One entry's form state. `apiKey` is write-only — never read back. */
export interface ProviderEntryForm {
  id: string;
  label: string;
  provider: string;
  authMethod: string;
  config: Record<string, string>;
  apiKey: string;
  /** True until saved: only then is the id frozen (it keys the secret). */
  isNew: boolean;
}

export function entryFormsFrom(entries: LLMProviderEntry[]): ProviderEntryForm[] {
  const forms = entries.map((e) => ({
    id: e.id || DEFAULT_LLM_PROVIDER_ID,
    label: e.label || '',
    provider: e.provider,
    authMethod: e.config?.['auth_method'] || '',
    config: { ...(e.config || {}) },
    apiKey: '',
    isNew: false,
  }));
  if (!forms.some((f) => f.id === DEFAULT_LLM_PROVIDER_ID)) {
    forms.unshift({
      id: DEFAULT_LLM_PROVIDER_ID, label: '', provider: '', authMethod: '',
      config: {}, apiKey: '', isNew: true,
    });
  }
  // The default entry first: it is what every layer inherits.
  return forms.sort((a, b) => Number(b.id === DEFAULT_LLM_PROVIDER_ID) - Number(a.id === DEFAULT_LLM_PROVIDER_ID));
}

export function entriesFrom(forms: ProviderEntryForm[]): LLMProviderEntry[] {
  return forms
    .filter((f) => f.provider)
    .map((f) => {
      const config: Record<string, string> = Object.fromEntries(
        // `model` never belongs on an entry — it is the layer's choice — and
        // a credential never belongs in the project document.
        Object.entries(f.config).filter(([k]) => k !== 'model' && k !== 'credentials_json'),
      );
      if (f.authMethod) config.auth_method = f.authMethod;
      return { id: f.id, label: f.label || undefined, provider: f.provider, config };
    });
}

/** Secret writes for the entries whose credential the user just typed. */
export function pendingCredentialWrites(forms: ProviderEntryForm[]): { key: string; value: string }[] {
  return forms
    .filter((f) => f.apiKey)
    .map((f) => ({ key: llmProviderSecretKey(f.id), value: f.apiKey }));
}

export interface LLMProvidersPanelProps {
  projectId: string;
  providers: ProviderMeta[];
  value: ProviderEntryForm[];
  onChange: (next: ProviderEntryForm[]) => void;
  secrets: SecretEntryResponse[];
  /** Layer keys using each entry id, so a removal can name what blocks it. */
  layersByProviderId: Record<string, string[]>;
  /** Entry ids the project has already saved — only those can be tested. */
  savedIds: string[];
}

export function LLMProvidersPanel({
  projectId, providers, value, onChange, secrets, layersByProviderId, savedIds,
}: LLMProvidersPanelProps) {
  const [liveModels, setLiveModels] = useState<Record<string, LiveModel[] | null>>({});

  const update = (index: number, next: Partial<ProviderEntryForm>) => {
    onChange(value.map((f, i) => (i === index ? { ...f, ...next } : f)));
  };

  const addEntry = () => {
    // A suggested id the user can rename while it is new. Frozen on save,
    // because it keys the entry's secret.
    let n = 2;
    const taken = new Set(value.map((f) => f.id));
    while (taken.has(`provider-${n}`)) n += 1;
    onChange([...value, {
      id: `provider-${n}`, label: '', provider: '', authMethod: '',
      config: {}, apiKey: '', isNew: true,
    }]);
  };

  const removeEntry = (index: number) => {
    onChange(value.filter((_, i) => i !== index));
  };

  const duplicateIds = value
    .map((f) => f.id)
    .filter((id, i, all) => all.indexOf(id) !== i);

  return (
    <Stack gap="md">
      <Stack gap={2}>
        <Text fw={600}>LLM providers</Text>
        <Text size="xs" c="dimmed">
          One or more connections the per-layer model settings choose from. Each
          has its own credentials. The <b>default</b> entry is what a layer uses
          unless it names another.
        </Text>
      </Stack>

      {value.map((form, index) => {
        const meta = providers.find((p) => p.id === form.provider) || null;
        const inUseBy = layersByProviderId[form.id] || [];
        const isDefault = form.id === DEFAULT_LLM_PROVIDER_ID;
        const secretKey = llmProviderSecretKey(form.id);
        const savedSecret = secrets.find((s) => s.key === secretKey);
        const idInvalid = !isValidLLMProviderId(form.id);
        const credentials: CredentialsPhaseValue = form.provider
          ? { provider: form.provider, authMethod: form.authMethod, config: form.config, apiKey: form.apiKey }
          : emptyCredentials();

        return (
          <Card key={`${form.id}-${index}`} withBorder padding="md" radius="md">
            <Stack gap="sm">
              <Group justify="space-between" align="flex-start">
                <Group gap="xs">
                  {isDefault ? (
                    <Badge color="blue" variant="light">default</Badge>
                  ) : (
                    <Badge variant="outline">{form.id}</Badge>
                  )}
                  {inUseBy.length > 0 && (
                    <Tooltip label={inUseBy.join(', ')}>
                      <Badge variant="light" color="gray">
                        used by {inUseBy.length} {inUseBy.length === 1 ? 'layer' : 'layers'}
                      </Badge>
                    </Tooltip>
                  )}
                </Group>
                {!isDefault && (
                  <Tooltip
                    disabled={inUseBy.length === 0}
                    label={`Reassign ${inUseBy.join(', ')} first`}
                  >
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      aria-label={`Remove ${form.id}`}
                      disabled={inUseBy.length > 0}
                      onClick={() => removeEntry(index)}
                    >
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Tooltip>
                )}
              </Group>

              {!isDefault && (
                <Group grow align="flex-start">
                  <TextInput
                    label="Id"
                    description={
                      form.isNew
                        ? 'Letters, digits, underscores and hyphens. Fixed once saved — it keys this entry’s credential.'
                        : 'Fixed: it keys this entry’s stored credential.'
                    }
                    value={form.id}
                    disabled={!form.isNew}
                    maxLength={MAX_LLM_PROVIDER_ID_LEN}
                    error={form.isNew && idInvalid ? 'Use 1–32 letters, digits, underscores or hyphens' : undefined}
                    onChange={(e) => update(index, { id: e.currentTarget.value })}
                  />
                  <TextInput
                    label="Name"
                    description="Shown in the model pickers."
                    placeholder="e.g. DeepInfra — open weights"
                    value={form.label}
                    onChange={(e) => update(index, { label: e.currentTarget.value })}
                  />
                </Group>
              )}

              {savedSecret && (
                <Group gap="xs">
                  <IconShieldCheck size={14} color="var(--db-green-text)" />
                  <Text size="xs" fw={500}>Credentials saved</Text>
                  <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>{savedSecret.masked}</Text>
                </Group>
              )}

              <ProviderCredentialsPhase<ProviderMeta>
                providers={providers}
                label="Provider"
                value={credentials}
                onChange={(next) => update(index, {
                  provider: next.provider,
                  authMethod: next.authMethod,
                  config: next.config,
                  apiKey: next.apiKey,
                })}
                loadButtonLabel="Check connection"
                onLoad={async () => {
                  try {
                    // Read through the ENTRY, so the server uses that entry's
                    // own config and credential chain — a secondary entry
                    // with no secret of its own fails here exactly as it
                    // would fail a run.
                    const resp = await api.listLiveLLMModelsForProvider(projectId, { provider_id: form.id });
                    setLiveModels((prev) => ({ ...prev, [form.id]: resp.models }));
                    return { ok: true, liveError: resp.live_error };
                  } catch (e: unknown) {
                    setLiveModels((prev) => ({ ...prev, [form.id]: null }));
                    return { ok: true, liveError: e instanceof Error ? e.message : String(e) };
                  }
                }}
              >
                <Text size="xs" c="dimmed">
                  {(liveModels[form.id]?.length ?? 0) > 0
                    ? `${liveModels[form.id]!.length} models reachable on this connection. Pick which layers use them under Models.`
                    : 'Models are chosen per layer under Models.'}
                </Text>
                {meta && savedIds.includes(form.id) && (
                  <TestConnectionButton projectId={projectId} target="llm" />
                )}
              </ProviderCredentialsPhase>
            </Stack>
          </Card>
        );
      })}

      {duplicateIds.length > 0 && (
        <Alert color="red" icon={<IconAlertCircle size={16} />}>
          Duplicate provider id: {duplicateIds.join(', ')}. Each entry needs its own id.
        </Alert>
      )}

      <Group>
        <Button variant="light" leftSection={<IconPlus size={14} />} onClick={addEntry}>
          Add provider
        </Button>
      </Group>
    </Stack>
  );
}

export default LLMProvidersPanel;
