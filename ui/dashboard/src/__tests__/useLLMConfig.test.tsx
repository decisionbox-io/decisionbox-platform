/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { renderHook, act, waitFor } from '@testing-library/react';
import { useLLMConfig } from '@/components/projects/useLLMConfig';

// The save path writes two things that cannot be written atomically: the
// project configuration, and the per-entry credentials. Which goes first
// decides what a rejected save leaves behind, and whose failure counts as
// the save failing.

jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: {
      getProjectLLMConfig: jest.fn(),
      listLLMProviders: jest.fn().mockResolvedValue([]),
      listSecrets: jest.fn().mockResolvedValue([]),
      setSecret: jest.fn().mockResolvedValue({}),
      updateProject: jest.fn().mockResolvedValue({ id: 'p1' }),
    },
  };
});

import { api } from '@/lib/api';

const CONFIG = {
  seeded: false,
  configured: true,
  providers: [{ id: 'default', provider: 'bedrock', config: {} }],
  layer_models: { default: { provider_id: 'default', model: 'opus' } },
};

/** A loaded hook with a credential typed into the default entry. */
async function loadedWithTypedKey() {
  const hook = renderHook(() => useLLMConfig('p1'));
  await waitFor(() => expect(hook.result.current.loading).toBe(false));
  act(() => {
    hook.result.current.setEntryForms(
      hook.result.current.entryForms.map((f) => ({ ...f, apiKey: 'the-new-key' })),
    );
  });
  return hook;
}

beforeEach(() => {
  jest.clearAllMocks();
  (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(CONFIG);
  (api.listLLMProviders as jest.Mock).mockResolvedValue([]);
  (api.listSecrets as jest.Mock).mockResolvedValue([]);
  (api.setSecret as jest.Mock).mockResolvedValue({});
  (api.updateProject as jest.Mock).mockResolvedValue({ id: 'p1' });
});

describe('useLLMConfig.save', () => {
  // A secret write is an unrecoverable upsert over the live value. Writing
  // it before the configuration is validated meant a REJECTED save left the
  // entry pointing at its old endpoint while holding the key the user typed
  // for the new one — so every call then sent that key to a third party.
  it('does not touch a live credential when the configuration is rejected', async () => {
    (api.updateProject as jest.Mock).mockRejectedValue(new Error('layer_models[default]: reasoning-class'));
    const hook = await loadedWithTypedKey();

    let ok: boolean | undefined;
    await act(async () => { ok = await hook.result.current.save(); });

    expect(ok).toBe(false);
    expect(api.setSecret).not.toHaveBeenCalled();
  });

  it('writes the credential once the configuration is accepted', async () => {
    const hook = await loadedWithTypedKey();

    let ok: boolean | undefined;
    await act(async () => { ok = await hook.result.current.save(); });

    expect(ok).toBe(true);
    expect(api.setSecret).toHaveBeenCalledWith('p1', expect.any(String), 'the-new-key');
    // And in that order, so a rejection can never reach the secret store.
    const updateAt = (api.updateProject as jest.Mock).mock.invocationCallOrder[0];
    const secretAt = (api.setSecret as jest.Mock).mock.invocationCallOrder[0];
    expect(updateAt).toBeLessThan(secretAt);
  });

  // Listing secrets needs admin; editing the configuration does not. A
  // member's valid save was reported as failed — and a wizard refused to
  // advance past it — because of a 403 on a refresh that only feeds a badge.
  it('still succeeds when the secret-metadata refresh is forbidden', async () => {
    (api.listSecrets as jest.Mock).mockRejectedValue(new Error('403 forbidden'));
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    let ok: boolean | undefined;
    await act(async () => { ok = await hook.result.current.save(); });

    expect(ok).toBe(true);
    expect(api.updateProject).toHaveBeenCalled();
  });
});

// savedEntries is compared against the in-flight entries to decide whether a
// connection has been edited. The in-flight ones go through
// entryFormsFrom -> entriesFrom, which pre-selects a single-method
// provider's sole auth_method and writes it into the config — so comparing
// against the RAW stored entries marked every such connection as unsaved,
// and its model list never loaded.
describe('useLLMConfig.savedEntries', () => {
  it('is normalised the same way the edited entries are', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue({
      seeded: false,
      configured: true,
      // Stored without an explicit auth_method, as a real OpenAI entry is.
      providers: [{ id: 'default', provider: 'openai', config: {} }],
      layer_models: { default: { provider_id: 'default', model: 'gpt-4o' } },
    });
    (api.listLLMProviders as jest.Mock).mockResolvedValue([
      { id: 'openai', name: 'OpenAI', description: '', config_fields: [], auth_methods: [{ id: 'api_key', name: 'API Key', fields: [] }] },
    ]);

    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    expect(hook.result.current.savedEntries).toEqual(hook.result.current.entries);
  });
});

// A model belongs to the provider it was picked from. Repointing an entry
// from one provider to another left every layer on it holding a model the
// new provider does not serve — and validation only checks that a model is
// non-empty, so the save went through and those layers called a model that
// does not exist.
describe('useLLMConfig — repointing an entry', () => {
  it('clears the models of the layers that use it', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue({
      seeded: false,
      configured: true,
      providers: [
        { id: 'default', provider: 'openai', config: {} },
        { id: 'other', provider: 'bedrock', config: {} },
      ],
      layer_models: {
        default: { provider_id: 'default', model: 'gpt-4o' },
        analysis: { provider_id: 'other', model: 'opus' },
      },
    });

    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    act(() => {
      hook.result.current.setEntryForms(
        hook.result.current.entryForms.map((f) => (f.id === 'default' ? { ...f, provider: 'claude' } : f)),
      );
    });

    // The layer on the repointed entry loses its model; the other keeps it.
    expect(hook.result.current.layerModels.default.model).toBe('');
    expect(hook.result.current.layerModels.analysis.model).toBe('opus');
    // And the configuration cannot be saved until the user picks one.
    expect(hook.result.current.validationError).toMatch(/Pick a model/i);
  });

  it('leaves the models alone when only the label changes', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue({
      seeded: false,
      configured: true,
      providers: [{ id: 'default', provider: 'openai', config: {} }],
      layer_models: { default: { provider_id: 'default', model: 'gpt-4o' } },
    });

    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    act(() => {
      hook.result.current.setEntryForms(
        hook.result.current.entryForms.map((f) => ({ ...f, label: 'Renamed' })),
      );
    });
    expect(hook.result.current.layerModels.default.model).toBe('gpt-4o');
  });
});

// An endpoint serves its own model over its own wire, so the two settings
// are a contradiction — vertex-ai refuses to build at all when both are
// set. The single-column form deleted wire_override as the endpoint was
// typed; after the split (endpoint on the entry, override on the row)
// nothing did, so a Vertex project with a layer override saved cleanly and
// then failed to construct that layer on the next run.
describe('useLLMConfig.setEntryForms — entering endpoint mode', () => {
  const VERTEX = {
    seeded: false,
    configured: true,
    providers: [{ id: 'default', provider: 'vertex-ai', config: { project_id: 'p' } }],
    layer_models: {
      default: { provider_id: 'default', model: 'gemini-2.5-flash', config: { wire_override: 'google-native' } },
      ask: { provider_id: 'other', model: 'x', config: { wire_override: 'anthropic-messages' } },
    },
  };

  it('drops the wire override of every layer on the entry that gained one', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(VERTEX);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    act(() => {
      hook.result.current.setEntryForms(hook.result.current.entryForms.map((f) => (
        f.id === 'default' ? { ...f, config: { ...f.config, endpoint_id: '123456789' } } : f
      )));
    });

    expect(hook.result.current.layerModels.default.config?.wire_override).toBeUndefined();
    // A layer on ANOTHER entry is not in endpoint mode and keeps its own.
    expect(hook.result.current.layerModels.ask.config?.wire_override).toBe('anthropic-messages');
  });

  // The endpoint serves its own deployed model, and the OpenAI-compat wire
  // forwards the model id verbatim — so a leftover publisher id like
  // gemini-2.5-flash is sent to a serving container that has never heard of
  // it. The single-column form persisted an empty model in endpoint mode;
  // after the split nothing did. Validation already accepts a blank model
  // where an endpoint is set.
  it('clears the publisher model of every layer on the entry that gained one', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(VERTEX);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    act(() => {
      hook.result.current.setEntryForms(hook.result.current.entryForms.map((f) => (
        f.id === 'default' ? { ...f, config: { ...f.config, endpoint_id: '123456789' } } : f
      )));
    });

    expect(hook.result.current.layerModels.default.model).toBe('');
    // And a layer elsewhere keeps the model it was given.
    expect(hook.result.current.layerModels.ask.model).toBe('x');
  });

  it('leaves the overrides alone when the entry has no endpoint', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(VERTEX);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    act(() => {
      hook.result.current.setEntryForms(hook.result.current.entryForms.map((f) => (
        f.id === 'default' ? { ...f, config: { ...f.config, project_id: 'q' } } : f
      )));
    });

    expect(hook.result.current.layerModels.default.config?.wire_override).toBe('google-native');
  });
});

// The migration screen is the one screen every legacy project has to pass
// through, and it asks the user to pick a model per layer. A seeded entry
// is synthesised from project.llm / project.blurb_llm, which ARE persisted:
// the server resolves it through EffectiveProviders and reads its stored
// credential. Reporting nothing as saved made LayerModelsPanel treat every
// connection as unsaved — so no model list loaded for any layer — and sent
// the connection check in flight without the stored, write-only key. The
// only way through was to retype a credential, to browse the models the
// save then requires you to choose from.
describe('useLLMConfig — a seeded project has a saved baseline', () => {
  const SEEDED = {
    seeded: true,
    configured: false,
    providers: [
      { id: 'default', provider: 'bedrock', config: { region: 'us-east-1' } },
      { id: 'blurb', provider: 'openai', config: {} },
    ],
    layer_models: {
      default: { provider_id: 'default', model: 'opus' },
      table_descriptions: { provider_id: 'blurb', model: 'gpt-4.1-nano' },
    },
  };

  it('reports the seeded entries as saved and server-answerable', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(SEEDED);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    expect(hook.result.current.seeded).toBe(true);
    expect(hook.result.current.savedIds).toEqual(['default', 'blurb']);
    // And the baseline is comparable to the in-flight forms, so an
    // untouched seeded connection does not read as edited.
    expect(hook.result.current.savedEntries.map((e) => e.id)).toEqual(['default', 'blurb']);
    expect(hook.result.current.savedEntries).toEqual(hook.result.current.entries);
  });
});

// entriesFrom strips model-scoped keys from an entry, because that is where
// they belong — but only the dashboard partitions them. An entry written
// through the API can carry a token limit or a wire override, and the
// backend merges an entry's config UNDER every row on it, so they are live.
// Loading such a project and saving anything dropped them: a silent loss of
// a budget, or of the override a custom deployment needs to dispatch at all.
describe('useLLMConfig — entry-level model settings survive the round trip', () => {
  const API_WRITTEN = {
    seeded: false,
    configured: true,
    providers: [
      {
        id: 'default',
        provider: 'vertex-ai',
        config: { project_id: 'p', wire_override: 'anthropic', max_output_tokens: '8192' },
      },
    ],
    layer_models: {
      default: { provider_id: 'default', model: 'claude-opus' },
      analysis: { provider_id: 'default', model: 'claude-opus', config: { max_output_tokens: '2048' } },
    },
  };

  it('moves them onto the rows that use the entry', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(API_WRITTEN);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));

    // Both settings now live on the default row, where the control is.
    expect(hook.result.current.layerModels.default.config?.wire_override).toBe('anthropic');
    expect(hook.result.current.layerModels.default.config?.max_output_tokens).toBe('8192');
    // A row that sets the key already won the backend's merge, so it keeps
    // its own value rather than being overwritten by the entry's.
    expect(hook.result.current.layerModels.analysis.config?.max_output_tokens).toBe('2048');
    expect(hook.result.current.layerModels.analysis.config?.wire_override).toBe('anthropic');
    // And the entry the next save writes carries the connection half only.
    const [entry] = hook.result.current.entries;
    expect(entry.config?.project_id).toBe('p');
    expect(entry.config?.wire_override).toBeUndefined();
    expect(entry.config?.max_output_tokens).toBeUndefined();
  });

  it('leaves a project whose entries hold no model settings alone', async () => {
    (api.getProjectLLMConfig as jest.Mock).mockResolvedValue(CONFIG);
    const hook = renderHook(() => useLLMConfig('p1'));
    await waitFor(() => expect(hook.result.current.loading).toBe(false));
    expect(hook.result.current.layerModels).toEqual(CONFIG.layer_models);
  });
});
