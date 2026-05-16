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
    { key: 'base_url', label: 'Base URL', required: false, type: 'string', placeholder: '', description: '', default: 'https://api.openai.com/v1', options: [] },
  ],
  auth_methods: [
    {
      id: 'api_key',
      name: 'API Key',
      description: 'OpenAI API key.',
      fields: [
        { key: 'credentials_json', label: 'API Key', required: true, type: 'credential', placeholder: 'sk-…', description: '', default: '', options: [] },
      ],
    },
  ],
};
const bedrockMeta: ProviderLike = {
  id: 'bedrock',
  name: 'AWS Bedrock',
  description: 'Uses IAM credentials',
  config_fields: [
    { key: 'region', label: 'Region', required: true, type: 'string', placeholder: '', description: '', default: 'us-east-1', options: [] },
  ],
  auth_methods: [
    {
      id: 'iam_role',
      name: 'IAM Role',
      description: 'Ambient AWS credentials.',
      fields: [],
    },
  ],
};

function Harness({ providers, onLoad, initial, modelChild }: {
  providers: ProviderLike[];
  onLoad: (cfg: Record<string, string>) => Promise<{ ok: boolean; liveError?: string }>;
  initial?: CredentialsPhaseValue;
  modelChild?: React.ReactNode;
}) {
  const [value, setValue] = useState<CredentialsPhaseValue>(
    initial ?? { provider: '', authMethod: '', config: {}, apiKey: '' }
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
        initial={{ provider: 'openai', authMethod: 'api_key', config: {}, apiKey: '' }}
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
        initial={{ provider: 'bedrock', authMethod: 'iam_role', config: { region: 'us-east-1' }, apiKey: '' }}
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
        initial={{ provider: 'openai', authMethod: 'api_key', config: {}, apiKey: '' }}
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
          authMethod: 'api_key',
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
    expect(cfg.credentials_json).toBe('sk-test-123');
    expect(cfg.base_url).toBe('https://api.openai.com/v1');
  });

  it('advances to the model phase on success and renders the children slot', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[openaiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'openai', authMethod: 'api_key', config: {}, apiKey: 'sk-x' }}
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
        initial={{ provider: 'openai', authMethod: 'api_key', config: {}, apiKey: 'sk-x' }}
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
        initial={{ provider: 'openai', authMethod: 'api_key', config: {}, apiKey: 'sk-x' }}
      />
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Load models' }));
    await waitFor(() => expect(screen.getByText(/network down/i)).toBeInTheDocument());
    expect(screen.getByText('MODEL-PICKER-CHILD')).toBeInTheDocument();
  });

  // Multi-method providers (Bedrock LLM, Vertex LLM, Bedrock embedding,
  // Vertex embedding) render an "Authentication method" dropdown
  // alongside the credentials phase. The fixture below mimics Bedrock's
  // three-method shape (iam_role / access_keys / assume_role).
  const bedrockMultiMeta: ProviderLike = {
    id: 'bedrock-multi',
    name: 'AWS Bedrock (multi-method)',
    description: 'Three auth methods',
    config_fields: [
      { key: 'region', label: 'Region', required: true, type: 'string', placeholder: '', description: '', default: 'us-east-1', options: [] },
    ],
    auth_methods: [
      { id: 'iam_role', name: 'IAM Role', description: 'Ambient AWS credentials.', fields: [] },
      {
        id: 'access_keys', name: 'Access Keys', description: 'AWS access key pair.', fields: [
          { key: 'credentials_json', label: 'Access Keys', required: true, type: 'credential', placeholder: 'AKIA…:wJalr…', description: '', default: '', options: [] },
        ],
      },
      {
        id: 'assume_role', name: 'Assume Role', description: 'STS AssumeRole.', fields: [
          { key: 'role_arn', label: 'Role ARN', required: true, type: 'string', placeholder: 'arn:aws:iam::123:role/X', description: '', default: '', options: [] },
          { key: 'external_id', label: 'External ID', required: false, type: 'string', placeholder: '', description: 'optional', default: '', options: [] },
        ],
      },
    ],
  };

  it('renders the auth-method dropdown when the provider declares 2+ methods', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMultiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'bedrock-multi', authMethod: 'iam_role', config: { region: 'us-east-1' }, apiKey: '' }}
      />
    );
    await waitFor(() =>
      expect(screen.getAllByLabelText('Authentication method').length).toBeGreaterThan(0)
    );
  });

  it('renders the per-method credential field when access_keys is selected', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMultiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'bedrock-multi', authMethod: 'access_keys', config: { region: 'us-east-1' }, apiKey: '' }}
      />
    );
    await waitFor(() => expect(screen.getByLabelText('Access Keys')).toBeInTheDocument());
  });

  it('renders the per-method non-credential field (role_arn) when assume_role is selected', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMultiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'bedrock-multi', authMethod: 'assume_role', config: { region: 'us-east-1' }, apiKey: '' }}
      />
    );
    await waitFor(() => expect(screen.getAllByLabelText(/Role ARN/i).length).toBeGreaterThan(0));
    expect(screen.getAllByLabelText(/External ID/i).length).toBeGreaterThan(0);
  });

  it('sends auth_method in the cfg map on Load models', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMultiMeta]}
        onLoad={onLoad}
        initial={{
          provider: 'bedrock-multi',
          authMethod: 'access_keys',
          config: { region: 'us-east-1' },
          apiKey: 'AKIATEST:secret',
        }}
      />
    );
    const btn = await screen.findByRole('button', { name: 'Load models' });
    fireEvent.click(btn);
    await waitFor(() => expect(onLoad).toHaveBeenCalledTimes(1));
    const cfg = onLoad.mock.calls[0][0];
    expect(cfg.auth_method).toBe('access_keys');
    expect(cfg.credentials_json).toBe('AKIATEST:secret');
    expect(cfg.region).toBe('us-east-1');
  });

  it('disables Load models when auth_method is unselected on a multi-method provider', async () => {
    const onLoad = jest.fn().mockResolvedValue({ ok: true });
    render(
      <Harness
        providers={[bedrockMultiMeta]}
        onLoad={onLoad}
        initial={{ provider: 'bedrock-multi', authMethod: '', config: { region: 'us-east-1' }, apiKey: '' }}
      />
    );
    const btn = await screen.findByRole('button', { name: 'Load models' });
    expect(btn).toBeDisabled();
  });
});
