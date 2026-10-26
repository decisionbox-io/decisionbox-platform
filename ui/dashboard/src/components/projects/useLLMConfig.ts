'use client';

/**
 * The per-layer LLM configuration's load/edit/save lifecycle, shared by the
 * two settings tabs that show it and by the creation wizard.
 *
 * It is one hook rather than two panels with two saves because the halves are
 * not independent: a layer row referencing a provider entry that has not been
 * saved yet is a write the API refuses, and rightly. So the entries and the
 * layer rows go out in one PUT, with the credentials the user typed written
 * alongside.
 *
 * For a project that has never been configured the API seeds both halves from
 * its legacy fields, which is what makes the forced trip through settings a
 * review rather than a seventeen-row data-entry exercise. `seeded` says so,
 * so the UI can tell the user these values are derived and not yet saved.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { notifications } from '@mantine/notifications';
import {
  DEFAULT_LLM_PROVIDER_ID, LLMProviderEntry, LayerModel, Project, ProviderMeta,
  SecretEntryResponse, api, isValidLLMProviderId,
} from '@/lib/api';
import {
  ProviderEntryForm, entriesFrom, entryFormsFrom, pendingCredentialWrites,
} from './LLMProvidersPanel';
import { DEFAULT_LAYER } from './LayerModelsPanel';

export interface UseLLMConfig {
  loading: boolean;
  error: string | null;
  saving: boolean;
  /** True while the shown values are derived from the legacy fields. */
  seeded: boolean;
  configured: boolean;
  providers: ProviderMeta[];
  entryForms: ProviderEntryForm[];
  setEntryForms: (next: ProviderEntryForm[]) => void;
  layerModels: Record<string, LayerModel>;
  setLayerModels: (next: Record<string, LayerModel>) => void;
  secrets: SecretEntryResponse[];
  /** Entry ids already persisted, so only those offer a connection test. */
  savedIds: string[];
  /** Layer keys per entry id, for the in-use removal guard. */
  layersByProviderId: Record<string, string[]>;
  /** A user-facing reason the configuration cannot be saved yet, or null. */
  validationError: string | null;
  /**
   * Persists the configuration. Resolves TRUE when it was written and FALSE
   * when it was not — a failed credential write, a rejected project update,
   * or a validation error that stopped it before the request.
   *
   * It reports rather than rejects because the failure is already shown to
   * the user as a notification; the boolean is for a caller that must not act
   * on an unsaved configuration. A wizard that advanced on a resolved promise
   * moved the user past a step whose values were never stored.
   */
  save: () => Promise<boolean>;
  entries: LLMProviderEntry[];
}

export function useLLMConfig(projectId: string, onSaved?: (p: Project) => void): UseLLMConfig {
  // An empty projectId means there is nothing to load — the form a host passes
  // when it owns the lifecycle itself. Reporting `loading` for a fetch that
  // will never happen would strand whatever rendered it.
  const [loading, setLoading] = useState(Boolean(projectId));
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [seeded, setSeeded] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [providers, setProviders] = useState<ProviderMeta[]>([]);
  const [entryForms, setEntryForms] = useState<ProviderEntryForm[]>([]);
  const [layerModels, setLayerModels] = useState<Record<string, LayerModel>>({});
  const [secrets, setSecrets] = useState<SecretEntryResponse[]>([]);
  const [savedIds, setSavedIds] = useState<string[]>([]);

  const loadOnce = useRef(false);
  useEffect(() => {
    if (loadOnce.current || !projectId) return;
    loadOnce.current = true;
    Promise.all([api.getProjectLLMConfig(projectId), api.listLLMProviders()])
      .then(([cfg, provs]) => {
        setProviders(provs);
        setSeeded(cfg.seeded);
        setConfigured(cfg.configured);
        setEntryForms(entryFormsFrom(cfg.providers));
        setLayerModels(cfg.layer_models || {});
        // Only a PERSISTED entry can be connection-tested: the test reads
        // stored config and the stored secret, not in-flight form state.
        setSavedIds(cfg.seeded ? [] : cfg.providers.map((e) => e.id || DEFAULT_LLM_PROVIDER_ID));
        api.listSecrets(projectId).then((s) => setSecrets(s || [])).catch(() => {});
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [projectId]);

  const entries = useMemo(() => entriesFrom(entryForms), [entryForms]);

  const layersByProviderId = useMemo(() => {
    const out: Record<string, string[]> = {};
    for (const [layer, row] of Object.entries(layerModels)) {
      const id = row.provider_id || DEFAULT_LLM_PROVIDER_ID;
      (out[id] ||= []).push(layer === DEFAULT_LAYER ? 'the default' : layer);
    }
    for (const list of Object.values(out)) list.sort();
    return out;
  }, [layerModels]);

  const validationError = useMemo(() => {
    const ids = entries.map((e) => e.id);
    if (!ids.includes(DEFAULT_LLM_PROVIDER_ID)) {
      return 'One provider must be the default — it is what every layer uses unless it names another.';
    }
    const dupes = ids.filter((id, i) => ids.indexOf(id) !== i);
    if (dupes.length > 0) return `Duplicate provider id: ${dupes.join(', ')}.`;
    const badId = ids.find((id) => !isValidLLMProviderId(id));
    if (badId) return `Provider id "${badId}" may only contain letters, digits, underscores and hyphens (max 32).`;
    const noProvider = entryForms.find((f) => !f.provider);
    if (noProvider) return `Choose a provider for "${noProvider.id}".`;

    const defaultRow = layerModels[DEFAULT_LAYER];
    if (!defaultRow) return 'Pick a default model — it is what every layer without its own uses.';
    for (const [layer, row] of Object.entries(layerModels)) {
      const id = row.provider_id || DEFAULT_LLM_PROVIDER_ID;
      if (!ids.includes(id)) return `"${layer}" names provider "${id}", which this project does not have.`;
      const entry = entries.find((e) => e.id === id);
      const endpoint = (row.config?.['endpoint_id'] || entry?.config?.['endpoint_id'] || '').trim();
      // An endpoint serves its own model, so it may legitimately carry none.
      if (!row.model && !endpoint) return `Pick a model for "${layer === DEFAULT_LAYER ? 'the default' : layer}".`;
    }
    return null;
  }, [entries, entryForms, layerModels]);

  const save = useCallback(async (): Promise<boolean> => {
    if (!projectId || validationError) return false;
    setSaving(true);
    try {
      // Credentials first: a layer row may reference an entry whose secret
      // the user has just typed, and the project write is what makes the
      // entry real. Writing the secret first means there is never a saved
      // entry with no credential to read.
      await Promise.all(
        pendingCredentialWrites(entryForms).map(({ key, value }) => api.setSecret(projectId, key, value)),
      );
      const saved = await api.updateProject(projectId, {
        llm_providers: entries,
        layer_models: layerModels,
      });
      setEntryForms((prev) => prev.map((f) => ({ ...f, apiKey: '', isNew: false })));
      setSavedIds(entries.map((e) => e.id));
      setSeeded(false);
      setConfigured(true);
      const updated = await api.listSecrets(projectId);
      setSecrets(updated || []);
      notifications.show({ title: 'Saved', message: 'LLM configuration updated', color: 'green' });
      onSaved?.(saved);
      return true;
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
      return false;
    } finally {
      setSaving(false);
    }
  }, [projectId, entries, entryForms, layerModels, validationError, onSaved]);

  return {
    loading, error, saving, seeded, configured, providers,
    entryForms, setEntryForms, layerModels, setLayerModels,
    secrets, savedIds, layersByProviderId, validationError, save, entries,
  };
}
