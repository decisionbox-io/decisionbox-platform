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
jest.mock('@/components/projects/ProvidersPanel', () => ({
  __esModule: true, default: () => <div>ProvidersPanel</div>,
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
