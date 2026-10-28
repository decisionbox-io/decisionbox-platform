/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import SettingsPage from '@/app/projects/[id]/settings/page';

// The project request and the LLM-configuration request are independent. If
// the second fails while the first succeeds, useLLMConfig keeps empty
// providers and rows and reports the reason in `error` — and the settings
// tabs used to render that as a project with nothing configured. The
// obvious response to THAT is to type the configuration in again and save,
// writing it over the real one, secondary entries and per-layer overrides
// included.

jest.mock('next/navigation', () => ({
  useParams: () => ({ id: 'proj-1' }),
  useRouter: () => ({ push: jest.fn(), replace: jest.fn() }),
}));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true,
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
jest.mock('@/components/projects/WarehouseConfigPanel', () => ({
  __esModule: true,
  default: () => <div>WarehouseConfigPanel</div>,
  TestConnectionButton: () => <button type="button">Test</button>,
}));
jest.mock('@/components/projects/ProvidersPanel', () => ({
  __esModule: true,
  default: () => <div>EmbeddingHalf</div>,
}));
jest.mock('@/components/projects/LLMProvidersPanel', () => ({
  LLMProvidersPanel: () => <div>LLMProvidersPanelMarker</div>,
}));
jest.mock('@/components/projects/LayerModelsPanel', () => ({
  LayerModelsPanel: () => <div>LayerModelsPanelMarker</div>,
  DEFAULT_LAYER: 'default',
}));

const llmConfig = {
  loading: false,
  error: null as string | null,
  saving: false, seeded: false, configured: false,
  providers: [], entryForms: [], setEntryForms: jest.fn(),
  layerModels: {}, setLayerModels: jest.fn(), secrets: [], savedIds: [],
  savedEntries: [], savedGeneration: 0, discoveredModels: {},
  reportDiscoveredModels: jest.fn(),
  layersByProviderId: {}, validationError: null,
  save: jest.fn(), entries: [],
};
jest.mock('@/components/projects/useLLMConfig', () => ({
  useLLMConfig: () => llmConfig,
}));

jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: {
      getProject: jest.fn(),
      getAppConfig: jest.fn(),
      getProfileSchema: jest.fn(),
      getSchemaCacheInfo: jest.fn(),
      listSchemaEdits: jest.fn(),
      updateProject: jest.fn(),
      invalidateSchemaCache: jest.fn(),
      deleteProject: jest.fn(),
    },
  };
});

import { api } from '@/lib/api';

beforeEach(() => {
  jest.clearAllMocks();
  llmConfig.loading = false;
  llmConfig.error = null;
  (api.getProject as jest.Mock).mockResolvedValue({
    id: 'proj-1', name: 'Acme', domain: 'gaming', category: 'match3',
    llm: { provider: 'bedrock', model: 'opus', config: {} },
    embedding: { provider: 'bedrock', model: 'titan', config: {} },
    warehouse: { provider: 'postgres', datasets: [] },
  });
  (api.getAppConfig as jest.Mock).mockResolvedValue({ ai_config_managed: false });
  (api.getProfileSchema as jest.Mock).mockResolvedValue(null);
  (api.getSchemaCacheInfo as jest.Mock).mockResolvedValue(null);
  (api.listSchemaEdits as jest.Mock).mockResolvedValue([]);
});

function renderPage() {
  return render(<MantineProvider><SettingsPage /></MantineProvider>);
}

describe('settings — the LLM configuration could not be loaded', () => {
  it('says so instead of showing an empty configuration', async () => {
    llmConfig.error = 'network error';
    renderPage();
    // Both AI tabs say it — each is an editor over the same configuration.
    await waitFor(() => expect(screen.getAllByText(/Could not load/).length).toBeGreaterThan(0));
    expect(screen.getAllByText(/network error/).length).toBeGreaterThan(0);
    // And offers no way to type a replacement over the stored one.
    expect(screen.queryByText('LLMProvidersPanelMarker')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save LLM configuration' })).not.toBeInTheDocument();
  });

  it('withholds the editors while it is still loading', async () => {
    llmConfig.loading = true;
    renderPage();
    await waitFor(() => expect(screen.getByText('EmbeddingHalf')).toBeInTheDocument());
    expect(screen.queryByText('LLMProvidersPanelMarker')).not.toBeInTheDocument();
  });

  it('renders the editors once it has loaded', async () => {
    renderPage();
    await waitFor(() => expect(screen.getByText('LLMProvidersPanelMarker')).toBeInTheDocument());
    expect(screen.queryAllByText(/Could not load/)).toHaveLength(0);
    expect(screen.getByText('LayerModelsPanelMarker')).toBeInTheDocument();
  });
});
