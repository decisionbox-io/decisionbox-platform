/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import {
  ProviderCredentialsPhase,
  CredentialsPhaseValue,
  ProviderLike,
} from '@/components/ProviderCredentialsPhase';
import { useState } from 'react';

/**
 * Shared phase is the 80% common ground between LLM, blurb LLM, and
 * embedding editors. These tests lock in the contract its consumers
 * rely on:
 *   - provider select + config fields render against the supplied meta
 *   - API key field only when the provider declares a credential
 *   - Load-models button fires onLoad with the complete config map
 *   - phase advances to "model" on success and renders the children slot
 *   - error path still advances to "model" so the user can type manually
 */

const openaiMeta: ProviderLike = {
  id: 'openai',
  name: 'OpenAI',
  description: 'Models from OpenAI',
  config_fields: [
    { key: 'api_key', label: 'API Key', required: true, type: 'credential', placeholder: 'sk-…', description: '', default: '', options: [] },
    { key: 'base_url', label: 'Base URL', required: false, type: 'string', placeholder: '', description: '', default: 'https://api.openai.com/v1', options: [] },
  ],
};
const bedrockMeta: ProviderLike = {
  id: 'bedrock',
  name: 'AWS Bedrock',
  description: 'Uses IAM credentials',
  config_fields: [
    { key: 'region', label: 'Region', required: true, type: 'string', placeholder: '', description: '', default: 'us-east-1', options: [] },
  ],
};

function Harness({ providers, onLoad, initial, modelChild }: {
  providers: ProviderLike[];
  onLoad: (cfg: Record<string, string>) => Promise<{ ok: boolean; liveError?: string }>;
  initial?: CredentialsPhaseValue;
  modelChild?: React.ReactNode;
}) {
  const [value, setValue] = useState<CredentialsPhaseValue>(
    initial ?? { provider: '', config: {}, apiKey: '' }
  );
  return (
    <MantineProvider>
      <ProviderCredentialsPhase
        providers={providers}
        label="Test Provider"
        value={value}
        onChange={setValue}
        onLoad={onLoad}
      >
        {modelChild ?? <div>MODEL-PICKER-CHILD</div>}
      </ProviderCredentialsPhase>
    </MantineProvider>
  );
}

describe('ProviderCredentialsPhase', () => {
  it('renders the provider select and description when provider picked', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(<Harness providers={[openaiMeta, bedrockMeta]} onLoad={onLoad} />);
    // Mantine Select renders both a visible input and a hidden <select>,
    // both labeled — getAllByLabelText just verifies the label is wired up.
    expect(screen.getAllByLabelText('Test Provider').length).toBeGreaterThan(0);
    // Nothing rendered in credentials card until a provider is chosen.
    expect(screen.queryByText(/Load models/)).not.toBeInTheDocument();
  });

  it('shows API key input for providers that declare a credential field', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', config: {}, apiKey: '' }}
      />
    );
    await waitFor(() => expect(screen.getByLabelText('API Key')).toBeInTheDocument());
  });

  it('shows the "cloud credentials" hint for providers without an api_key field', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMeta]}
        onLoad={onLoad}
        initial={{ provider: 'bedrock', config: { region: 'us-east-1' }, apiKey: '' }}
      />
    );
    await waitFor(() =>
      expect(screen.getByText(/uses cloud credentials/i)).toBeInTheDocument()
    );
    expect(screen.queryByLabelText('API Key')).not.toBeInTheDocument();
  });

  it('disables Load models until api_key is entered on credential providers', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', config: {}, apiKey: '' }}
      />
    );
    const btn = await screen.findByRole('button', { name: 'Load models' });
    expect(btn).toBeDisabled();
  });

  it('calls onLoad with the merged config+api_key when Load models clicked', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{
          provider: 'openai',
          config: { base_url: 'https://api.openai.com/v1' },
          apiKey: 'sk-test-123',
        }}
      />
    );
    const btn = await screen.findByRole('button', { name: 'Load models' });
    expect(btn).not.toBeDisabled();
    fireEvent.click(btn);
    await waitFor(() => expect(onLoad).toHaveBeenCalledTimes(1));
    const cfg = onLoad.mock.calls[0][0];
    expect(cfg.api_key).toBe('sk-test-123');
    expect(cfg.base_url).toBe('https://api.openai.com/v1');
  });

  it('advances to the model phase on success and renders the children slot', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', config: {}, apiKey: 'sk-x' }}
      />
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Load models' }));
    await waitFor(() => expect(screen.getByText('MODEL-PICKER-CHILD')).toBeInTheDocument());
    // Back / Refresh controls appear in model phase.
    expect(screen.getByRole('button', { name: /Back to credentials/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Refresh model list/i })).toBeInTheDocument();
  });

  it('surfaces live_error in an alert but still advances to model phase', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true, liveError: 'quota exceeded' });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', config: {}, apiKey: 'sk-x' }}
      />
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Load models' }));
    await waitFor(() =>
      expect(screen.getByText(/Could not fetch live model list/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/quota exceeded/)).toBeInTheDocument();
    expect(screen.getByText('MODEL-PICKER-CHILD')).toBeInTheDocument();
  });

  it('still advances to model phase when onLoad throws (manual-entry fallback)', async () => {
    const onLoad = jest.fn().mockRejectedValue(new Error('network down'));
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', config: {}, apiKey: 'sk-x' }}
      />
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Load models' }));
    await waitFor(() => expect(screen.getByText(/network down/i)).toBeInTheDocument());
    expect(screen.getByText('MODEL-PICKER-CHILD')).toBeInTheDocument();
  });
});
