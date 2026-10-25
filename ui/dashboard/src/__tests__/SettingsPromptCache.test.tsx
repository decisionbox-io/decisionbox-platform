/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import ProjectSettingsPage from '@/app/projects/[id]/settings/page';

// Prompt caching is reachable in Settings → Advanced, and is a project field
// so saving it cannot disturb the provider, model or credentials the AI tab
// owns. It used to be rendered only in the LLM form's credentials phase, which
// a configured project skips entirely.

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

// A provider that implements caching declares the field; one that does not
// omits it. The switch keys off that, not a hardcoded provider list.
const CACHING_PROVIDER = {
  id: 'bedrock', name: 'Bedrock', auth_methods: [],
  config_fields: [
    { key: 'region', label: 'Region', type: 'string' },
    { key: 'prompt_cache', label: 'Prompt caching (Claude models)', type: 'boolean', default: 'true' },
  ],
};
const NON_CACHING_PROVIDER = {
  id: 'openai', name: 'OpenAI', auth_methods: [],
  config_fields: [{ key: 'organization', label: 'Organization', type: 'string' }],
};

function project(extra: Record<string, unknown> = {}, llmConfig: Record<string, string> = {}) {
  return {
    id: 'proj-1', name: 'Acme', state: 'ready', description: '',
    llm: { provider: 'bedrock', model: 'anthropic.claude-opus-5-5', config: { region: 'us-east-1', ...llmConfig } },
    embedding: { provider: 'bedrock', model: 'titan', config: {} },
    warehouse: { provider: 'postgres', datasets: ['public'] },
    ...extra,
  };
}

async function renderAdvanced() {
  render(<MantineProvider><ProjectSettingsPage /></MantineProvider>);
  fireEvent.click(await screen.findByRole('tab', { name: /advanced/i }));
}

const theSwitch = () => screen.getByRole('switch', { name: /prompt caching/i });

beforeEach(() => {
  jest.clearAllMocks();
  (api.listLLMProviders as jest.Mock).mockResolvedValue([CACHING_PROVIDER, NON_CACHING_PROVIDER]);
});

describe('Settings → Advanced: prompt caching', () => {
  it('is on when nothing is set — caching is opt-out', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project());
    await renderAdvanced();
    expect(await screen.findByRole('switch', { name: /prompt caching/i })).toBeChecked();
  });

  it('reads the project field when it is set', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project({ prompt_cache_enabled: false }));
    await renderAdvanced();
    expect(await screen.findByRole('switch', { name: /prompt caching/i })).not.toBeChecked();
  });

  // The wizard still writes the legacy provider config key, and projects
  // created before the field only have that. Reading the field as a plain
  // boolean would show caching as off for every one of them.
  it('falls back to the legacy llm.config value, case-insensitively', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project({}, { prompt_cache: 'FALSE' }));
    await renderAdvanced();
    expect(await screen.findByRole('switch', { name: /prompt caching/i })).not.toBeChecked();
  });

  it('the project field wins over a conflicting legacy value', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project({ prompt_cache_enabled: true }, { prompt_cache: 'false' }));
    await renderAdvanced();
    expect(await screen.findByRole('switch', { name: /prompt caching/i })).toBeChecked();
  });

  // The point of the project field: the save touches one field, so it cannot
  // restate — and therefore cannot revert — the provider, model or
  // credentials that the AI tab owns and writes separately.
  it('saves one field and nothing else', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project());
    (api.updateProject as jest.Mock).mockResolvedValue(project({ prompt_cache_enabled: false }));
    await renderAdvanced();
    fireEvent.click(await screen.findByRole('switch', { name: /prompt caching/i }));

    await waitFor(() => expect(api.updateProject).toHaveBeenCalled());
    const [, payload] = (api.updateProject as jest.Mock).mock.calls[0];
    expect(payload).toEqual({ prompt_cache_enabled: false });
    expect(payload.llm).toBeUndefined();
    await waitFor(() => expect(theSwitch()).not.toBeChecked());
  });

  it('turning it back on sends true', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project({ prompt_cache_enabled: false }));
    (api.updateProject as jest.Mock).mockResolvedValue(project({ prompt_cache_enabled: true }));
    await renderAdvanced();
    fireEvent.click(await screen.findByRole('switch', { name: /prompt caching/i }));
    await waitFor(() => expect(api.updateProject).toHaveBeenCalledWith('proj-1', { prompt_cache_enabled: true }));
  });

  it('rolls back when the save fails', async () => {
    (api.getProject as jest.Mock).mockResolvedValue(project());
    (api.updateProject as jest.Mock).mockRejectedValue(new Error('nope'));
    await renderAdvanced();
    const sw = await screen.findByRole('switch', { name: /prompt caching/i });
    expect(sw).toBeChecked();
    fireEvent.click(sw);
    await waitFor(() => expect(theSwitch()).toBeChecked());
  });

  it('is not offered for a provider that does not implement caching', async () => {
    (api.getProject as jest.Mock).mockResolvedValue({
      ...project(), llm: { provider: 'openai', model: 'gpt-4.1', config: {} },
    });
    await renderAdvanced();
    // Another Advanced switch proves the tab rendered.
    expect(await screen.findByRole('switch', { name: /suggested questions/i })).toBeInTheDocument();
    expect(screen.queryByRole('switch', { name: /prompt caching/i })).not.toBeInTheDocument();
  });
});
