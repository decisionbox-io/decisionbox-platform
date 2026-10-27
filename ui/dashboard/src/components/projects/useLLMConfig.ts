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
  LiveModel, SecretEntryResponse, api, isValidLLMProviderId,
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
  /**
   * The entries AS STORED, which is what the server answers about.
   *
   * Separate from `entries` (the in-flight form) because the live-model
   * endpoint reads the persisted connection: a list fetched while the form
   * said something else describes the stored entry, not the edited one, and
   * caching it against the edited values is how a repointed entry kept
   * offering its old provider's models.
   */
  savedEntries: LLMProviderEntry[];
  /**
   * Incremented on every successful save.
   *
   * A credential-only save changes neither the provider nor the config, so
   * nothing about the stored connection moves — and anything downstream
   * that caches per connection would keep serving what it fetched with the
   * old key. This is the fact that a save happened, which no amount of
   * comparing the entries can tell you.
   */
  savedGeneration: number;
  /**
   * Models the Providers tab discovered with the credential in its form,
   * keyed by the CONNECTION they were discovered against — see
   * connectionKeyFor.
   *
   * The Models tab cannot ask the server about a connection that has not
   * been saved — the server would answer about the stored one — but the
   * Providers tab's own check CAN, because it posts the in-flight values.
   * Sharing the answer is what keeps "change a provider, pick its model,
   * save" possible in one pass: otherwise the model is cleared, the list
   * cannot load, and the save is blocked until a model is chosen.
   *
   * Keyed by connection rather than by entry id because an entry can be
   * repointed again: keyed by id, the list discovered for Ollama was still
   * offered after the entry was switched to vLLM, and those model ids could
   * be selected and saved against a connection that cannot serve them.
   */
  discoveredModels: Record<string, LiveModel[]>;
  reportDiscoveredModels: (connectionKey: string, models: LiveModel[]) => void;
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
  const [savedEntries, setSavedEntries] = useState<LLMProviderEntry[]>([]);
  const [savedGeneration, setSavedGeneration] = useState(0);
  const [discoveredModels, setDiscoveredModels] = useState<Record<string, LiveModel[]>>({});
  const reportDiscoveredModels = useCallback((connectionKey: string, models: LiveModel[]) => {
    setDiscoveredModels((prev) => ({ ...prev, [connectionKey]: models }));
  }, []);

  const loadOnce = useRef(false);
  useEffect(() => {
    if (loadOnce.current || !projectId) return;
    loadOnce.current = true;
    Promise.all([api.getProjectLLMConfig(projectId), api.listLLMProviders()])
      .then(([cfg, provs]) => {
        setProviders(provs);
        setSeeded(cfg.seeded);
        setConfigured(cfg.configured);
        // The registry metadata goes in: a single-method provider's entry
        // needs its sole auth method pre-selected, or its credential input
        // never renders.
        setEntryForms(entryFormsFrom(cfg.providers, provs));
        setLayerModels(cfg.layer_models || {});
        // Only a PERSISTED entry can be connection-tested: the test reads
        // stored config and the stored secret, not in-flight form state.
        setSavedIds(cfg.seeded ? [] : cfg.providers.map((e) => e.id || DEFAULT_LLM_PROVIDER_ID));
        // Normalised through the SAME round trip the in-flight entries take,
        // so the two are comparable. entryFormsFrom pre-selects a
        // single-method provider's sole auth_method and entriesFrom writes
        // it into the config, so a raw stored entry differs from an
        // untouched edited one — which made every such connection look
        // unsaved, and its model list never load.
        setSavedEntries(cfg.seeded ? [] : entriesFrom(entryFormsFrom(cfg.providers, provs)));
        api.listSecrets(projectId).then((s) => setSecrets(s || [])).catch(() => {});
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [projectId]);

  /**
   * editEntries replaces the provider-entry forms, and follows the two
   * row-level consequences an entry edit has.
   *
   * A changed PROVIDER clears the models of every layer on that entry. A
   * model belongs to the provider it was picked from: repointing an entry
   * from OpenAI to Claude left every layer on it holding a model the new
   * provider does not serve, and validation only checks that a model is
   * non-empty, so the save went through and those layers called a model
   * that does not exist. The row-level picker already clears the model when
   * its entry changes; this is the same rule seen from the Providers tab.
   *
   * Gaining an ENDPOINT clears those layers' wire_override. An endpoint
   * serves its own model over its own wire — vertex-ai refuses to build at
   * all when both are set — and the two now live apart: the endpoint on the
   * entry, the override on the row. The single-column form used to delete
   * the override as the endpoint was typed; nothing did after the split, so
   * a Vertex project with a layer override saved cleanly and then failed to
   * construct that layer.
   */
  const editEntries = useCallback((next: ProviderEntryForm[]) => {
    const repointed = new Set<string>();
    const endpointed = new Set<string>();
    const endpointOf = (f: ProviderEntryForm | undefined) => (f?.config?.['endpoint_id'] ?? '').trim();
    for (const f of next) {
      const before = entryForms.find((p) => p.id === f.id);
      if (!before) continue;
      if (before.provider && f.provider && before.provider !== f.provider) {
        repointed.add(f.id);
      }
      if (!endpointOf(before) && endpointOf(f)) {
        endpointed.add(f.id);
      }
    }
    setEntryForms(next);
    if (repointed.size === 0 && endpointed.size === 0) return;
    setLayerModels((rows) => {
      const out: Record<string, LayerModel> = {};
      for (const [layer, row] of Object.entries(rows)) {
        const id = row.provider_id || DEFAULT_LLM_PROVIDER_ID;
        let edited = row;
        if (repointed.has(id)) edited = { ...edited, model: '' };
        if (endpointed.has(id) && edited.config?.['wire_override']) {
          const config = { ...edited.config };
          delete config['wire_override'];
          edited = { ...edited, config };
        }
        out[layer] = edited;
      }
      return out;
    });
  }, [entryForms]);

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
      // The CONFIGURATION is written first, and only then the credentials.
      //
      // The other order is tempting — it means a saved entry is never
      // briefly without a credential — but it is wrong where it matters. A
      // secret write is an unrecoverable upsert over the live value, so if
      // the project update is then rejected (a reasoning default with no
      // table-description override is rejected server-side, for one), the
      // entry still points at its OLD endpoint while holding the key the
      // user typed for the NEW one: every call then sends that key to a
      // third party.
      //
      // This way a rejected update leaves the project exactly as it was, and
      // the window this opens is one request long and fails closed — the new
      // endpoint with the old key is an authentication error, not a
      // disclosure.
      const saved = await api.updateProject(projectId, {
        llm_providers: entries,
        layer_models: layerModels,
      });
      await Promise.all(
        pendingCredentialWrites(entryForms).map(({ key, value }) => api.setSecret(projectId, key, value)),
      );
      setEntryForms((prev) => prev.map((f) => ({ ...f, apiKey: '', isNew: false })));
      setSavedIds(entries.map((e) => e.id));
      setSavedEntries(entries);
      setSavedGeneration((n) => n + 1);
      setSeeded(false);
      setConfigured(true);
      // Best-effort, and deliberately NOT part of whether the save
      // succeeded: listing secrets needs admin, while editing the
      // configuration does not. A member's valid save would otherwise be
      // reported as failed — and a wizard would refuse to advance past it —
      // because of a 403 on a metadata refresh that only feeds a badge.
      try {
        setSecrets((await api.listSecrets(projectId)) || []);
      } catch {
        /* the "credentials saved" badge stays as it was */
      }
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
    entryForms, setEntryForms: editEntries, layerModels, setLayerModels,
    secrets, savedIds, savedEntries, savedGeneration,
    discoveredModels, reportDiscoveredModels,
    layersByProviderId, validationError, save, entries,
  };
}
