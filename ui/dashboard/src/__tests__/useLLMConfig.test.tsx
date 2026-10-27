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
