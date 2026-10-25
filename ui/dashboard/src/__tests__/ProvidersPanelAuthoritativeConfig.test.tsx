/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import ProvidersPanel from '@/components/projects/ProvidersPanel';

// The panel does not own every llm.config key. Update replaces the LLM block
// wholesale, so a key edited elsewhere after this panel mounted must win over
// the copy the panel loaded — otherwise saving providers silently reverts it.
jest.mock('@/lib/api', () => ({
  api: {
    getProject: jest.fn(),
    updateProject: jest.fn(),
    listLLMProviders: jest.fn(),
    listEmbeddingProviders: jest.fn(),
    listSecrets: jest.fn().mockResolvedValue([]),
    listLiveLLMModelsForProject: jest.fn().mockResolvedValue({ models: [] }),
    setSecret: jest.fn(),
  },
}));
jest.mock('@/components/common/LLMModelField', () => ({
  DynamicField: () => <div />,
  LiveModelCombobox: () => <div />,
  modelWireIsKnown: () => true,
}));

import { api } from '@/lib/api';

const PROJECT = {
  id: 'proj-1', name: 'Acme', state: 'ready',
  llm: { provider: 'bedrock', model: 'm', config: { region: 'us-east-1', prompt_cache: 'true' } },
  embedding: { provider: 'bedrock', model: 'titan', config: {} },
  warehouse: { provider: 'postgres', datasets: ['public'] },
};

beforeEach(() => {
  jest.clearAllMocks();
  (api.getProject as jest.Mock).mockResolvedValue(PROJECT);
  (api.updateProject as jest.Mock).mockResolvedValue(PROJECT);
  (api.listLLMProviders as jest.Mock).mockResolvedValue([
    { id: 'bedrock', name: 'Bedrock', config_fields: [{ key: 'region', label: 'Region', type: 'string' }], auth_methods: [] },
  ]);
  (api.listEmbeddingProviders as jest.Mock).mockResolvedValue([
    { id: 'bedrock', name: 'Bedrock', config_fields: [], auth_methods: [] },
  ]);
});

it('authoritativeLLMConfig wins over the config the panel loaded', async () => {
  render(
    <MantineProvider>
      <ProvidersPanel projectId="proj-1" variant="page" authoritativeLLMConfig={{ prompt_cache: 'false' }} />
    </MantineProvider>,
  );
  const save = await screen.findByRole('button', { name: /save/i });
  fireEvent.click(save);
  await waitFor(() => expect(api.updateProject).toHaveBeenCalled());
  const [, payload] = (api.updateProject as jest.Mock).mock.calls[0];
  expect(payload.llm.config.prompt_cache).toBe('false');
  // and the panel's own keys still go through
  expect(payload.llm.config.region).toBe('us-east-1');
});
