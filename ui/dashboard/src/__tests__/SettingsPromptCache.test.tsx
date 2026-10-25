/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import ProjectSettingsPage from '@/app/projects/[id]/settings/page';

// Prompt caching is a provider config field (llm.config.prompt_cache), not a
// project field, and it was only ever rendered in the credentials phase of the
// LLM form — which a configured project skips. These cover it in Advanced,
// where it is reachable.

jest.mock('next/navigation', () => ({
  useParams: () => ({ id: 'proj-1' }),
  useRouter: () => ({ push: jest.fn(), replace: jest.fn() }),
}));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true,
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
// The real panel loads the project once and keeps llm.config in its own form
// state. The mock stands in for that: it counts mounts (so a remount after the
// stored value changes is observable) and exposes its onSaved callback, which
// is how a save in the AI tab reaches this page.
const providersMounts = { count: 0 };
jest.mock('@/components/projects/ProvidersPanel', () => ({
  __esModule: true,
  // A class, so componentDidMount counts real MOUNTS — a render counter would
  // be satisfied by any re-render and would not prove the remount.
  default: class ProvidersPanelMock extends (jest.requireActual('react').Component) {
    componentDidMount() { providersMounts.count += 1; }
    render() {
      const { onSaved } = this.props as { onSaved: () => void };
      return <button type="button" onClick={onSaved}>save-providers</button>;
    }
  },
}));
jest.mock('@/components/projects/WarehouseConfigPanel', () => ({
  __esModule: true, default: () => <div>WarehouseConfigPanel</div>,
}));
jest.mock('@/components/BlurbLLMEditor', () => ({
  BlurbLLMEditor: () => <div>BlurbLLMEditor</div>,
  emptyBlurbLLMState: { enabled: false, provider: '', authMethod: '', model: '', config: {}, apiKey: '' },
}));
jest.mock('@/lib/api', () => ({
  api: {
    getProject: jest.fn(),
    updateProject: jest.fn(),
    listLLMProviders: jest.fn(),
    getAppConfig: jest.fn().mockResolvedValue({ ai_config_managed: false }),
    listSecrets: jest.fn().mockResolvedValue([]),
    getProjectProfileSchema: jest.fn().mockResolvedValue(null),
  },
}));

import { api } from '@/lib/api';

// A provider that implements prompt caching declares the field itself; one
// that does not simply omits it. The switch keys off that, not a hardcoded
// provider list.
const CACHING_PROVIDER = {
  id: 'bedrock',
  name: 'Bedrock',
  config_fields: [
    { key: 'region', label: 'Region', type: 'string' },
    { key: 'prompt_cache', label: 'Prompt caching (Claude models)', type: 'boolean', default: 'true' },
  ],
  auth_methods: [],
};
const NON_CACHING_PROVIDER = {
  id: 'openai',
  name: 'OpenAI',
  config_fields: [{ key: 'organization', label: 'Organization', type: 'string' }],
  auth_methods: [],
};

function project(llm: Record<string, unknown>) {
  return {
    id: 'proj-1',
    name: 'Acme',
    state: 'ready',
    description: '',
    llm,
    embedding: { provider: 'bedrock', model: 'titan', config: {} },
    warehouse: { provider: 'postgres', datasets: ['public'] },
  };
}

async function renderAdvanced() {
  render(<MantineProvider><ProjectSettingsPage /></MantineProvider>);
  const tab = await screen.findByRole('tab', { name: /advanced/i });
  fireEvent.click(tab);
  return tab;
}

beforeEach(() => {
  jest.clearAllMocks();
  (api.listLLMProviders as jest.Mock).mockResolvedValue([CACHING_PROVIDER, NON_CACHING_PROVIDER]);
});

describe('Settings → Advanced: prompt caching', () => {
  it('is on when the key is absent — caching is the default, so an unset key must not read as off', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'anthropic.claude-opus-5-5', config: { region: 'us-east-1' } }),
    );
    await renderAdvanced();
    const sw = await screen.findByRole('switch', { name: /prompt caching/i });
    expect(sw).toBeChecked();
  });

  it('is off when the stored value is "false", case-insensitively', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'm', config: { prompt_cache: 'FALSE' } }),
    );
    await renderAdvanced();
    expect(await screen.findByRole('switch', { name: /prompt caching/i })).not.toBeChecked();
  });

  it('turning it off sends the whole LLM block with only prompt_cache changed', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'anthropic.claude-opus-5-5', config: { region: 'us-east-1', auth_method: 'access_keys' } }),
    );
    (api.updateProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'anthropic.claude-opus-5-5', config: { region: 'us-east-1', auth_method: 'access_keys', prompt_cache: 'false' } }),
    );
    await renderAdvanced();
    fireEvent.click(await screen.findByRole('switch', { name: /prompt caching/i }));

    await waitFor(() => expect(api.updateProject).toHaveBeenCalled());
    const [, payload] = (api.updateProject as jest.Mock).mock.calls[0];
    // Update replaces the LLM block wholesale, so provider, model and the
    // other config keys must survive the round trip.
    expect(payload.llm.provider).toBe('bedrock');
    expect(payload.llm.model).toBe('anthropic.claude-opus-5-5');
    expect(payload.llm.config.region).toBe('us-east-1');
    expect(payload.llm.config.auth_method).toBe('access_keys');
    expect(payload.llm.config.prompt_cache).toBe('false');
    await waitFor(() => expect(screen.getByRole('switch', { name: /prompt caching/i })).not.toBeChecked());
  });

  it('turning it back on sends "true"', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'm', config: { prompt_cache: 'false' } }),
    );
    (api.updateProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'm', config: { prompt_cache: 'true' } }),
    );
    await renderAdvanced();
    fireEvent.click(await screen.findByRole('switch', { name: /prompt caching/i }));
    await waitFor(() => expect(api.updateProject).toHaveBeenCalled());
    const [, payload] = (api.updateProject as jest.Mock).mock.calls[0];
    expect(payload.llm.config.prompt_cache).toBe('true');
  });

  it('rolls back and reports when the save fails', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'bedrock', model: 'm', config: {} }),
    );
    (api.updateProject as jest.Mock).mockRejectedValue(new Error('nope'));
    await renderAdvanced();
    const sw = await screen.findByRole('switch', { name: /prompt caching/i });
    expect(sw).toBeChecked();
    fireEvent.click(sw);
    // Optimistically off, then back on once the save fails.
    await waitFor(() => expect(screen.getByRole('switch', { name: /prompt caching/i })).toBeChecked());
  });

  // Regression: llm.config is written by two editors on this page. Turning
  // caching off in Advanced used to leave the AI tab's form holding the old
  // config, so its next save silently re-enabled caching — and this tab's
  // switch kept showing the stale value either way.
  it('stays in step with a save from the AI tab', async () => {
    const off = project({ provider: 'bedrock', model: 'm', config: { region: 'us-east-1', prompt_cache: 'false' } });
    (api.getProject as jest.Mock)
      .mockResolvedValueOnce(project({ provider: 'bedrock', model: 'm', config: { region: 'us-east-1' } }))
      .mockResolvedValue(off);
    (api.updateProject as jest.Mock).mockResolvedValue(off);

    await renderAdvanced();
    const sw = await screen.findByRole('switch', { name: /prompt caching/i });
    expect(sw).toBeChecked();

    providersMounts.count = 0;
    fireEvent.click(sw);
    await waitFor(() => expect(screen.getByRole('switch', { name: /prompt caching/i })).not.toBeChecked());
    // The stored value moved, so the provider form must be rebuilt from it
    // instead of keeping the config it loaded on mount.
    await waitFor(() => expect(providersMounts.count).toBeGreaterThan(0));

    // Now a save from the AI tab, reached the way a user reaches it. (Mantine
    // keeps inactive panels mounted but hidden, which is exactly why the
    // provider form holds stale config in the first place — role queries skip
    // hidden nodes, so switch tabs rather than querying through it.)
    fireEvent.click(screen.getByRole('tab', { name: /ai & embedding/i }));
    fireEvent.click(await screen.findByRole('button', { name: 'save-providers' }));
    await waitFor(() => expect(api.getProject).toHaveBeenCalledTimes(2));

    // Back on Advanced, the switch must show the stored value the refresh
    // just read, not whatever it last set locally.
    fireEvent.click(screen.getByRole('tab', { name: /advanced/i }));
    await waitFor(() => expect(screen.getByRole('switch', { name: /prompt caching/i })).not.toBeChecked());
  });

  it('is not offered for a provider that does not implement it', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(
      project({ provider: 'openai', model: 'gpt-4.1', config: {} }),
    );
    await renderAdvanced();
    // Another Advanced switch proves the tab rendered.
    expect(await screen.findByRole('switch', { name: /suggested questions/i })).toBeInTheDocument();
    expect(screen.queryByRole('switch', { name: /prompt caching/i })).not.toBeInTheDocument();
  });
});
