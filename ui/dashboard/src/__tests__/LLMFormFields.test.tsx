/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { useState } from 'react';
import {
  LLMFormFields,
  LLMFormState,
  emptyLLMFormState,
  AIPhase,
} from '@/components/projects/LLMFormFields';
import type { ProviderMeta, LiveModel } from '@/lib/api';

/**
 * LLMFormFields is the single source of truth for the LLM provider form
 * in the new-project wizard, settings page, and pack-gen wizard. The
 * tests below cover both phases (credentials + model), the api-key vs
 * cloud-creds split, and the load-models button gating.
 */

const openaiMeta: ProviderMeta = {
  id: 'openai',
  name: 'OpenAI',
  description: 'OpenAI models',
  config_fields: [
    { key: 'api_key', label: 'API Key', required: true, type: 'credential', placeholder: 'sk-…', description: '', default: '', options: [] },
    { key: 'base_url', label: 'Base URL', required: false, type: 'string', placeholder: '', description: '', default: 'https://api.openai.com/v1', options: [] },
    { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
  ],
};

const bedrockMeta: ProviderMeta = {
  id: 'bedrock',
  name: 'AWS Bedrock',
  description: 'Uses IAM credentials',
  config_fields: [
    { key: 'region', label: 'Region', required: true, type: 'string', placeholder: '', description: '', default: 'us-east-1', options: [] },
    { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
  ],
};

function ControlledHarness({
  providers,
  initial,
  initialPhase = 'credentials',
  liveModels = null,
  liveError = null,
  onLoadModels = jest.fn().mockResolvedValue(undefined),
  hasSavedApiKey = false,
}: {
  providers: ProviderMeta[];
  initial: LLMFormState;
  initialPhase?: AIPhase;
  liveModels?: LiveModel[] | null;
  liveError?: string | null;
  onLoadModels?: jest.Mock;
  hasSavedApiKey?: boolean;
}) {
  const [v, setV] = useState<LLMFormState>(initial);
  const [phase, setPhase] = useState<AIPhase>(initialPhase);
  return (
    <MantineProvider>
      <div data-testid="state-dump">{JSON.stringify({ value: v, phase })}</div>
      <LLMFormFields
        providers={providers}
        value={v}
        onChange={setV}
        phase={phase}
        onPhaseChange={setPhase}
        liveModels={liveModels}
        liveError={liveError}
        loading={false}
        onLoadModels={onLoadModels}
        hasSavedApiKey={hasSavedApiKey}
      />
    </MantineProvider>
  );
}

function getDump() {
  return JSON.parse(screen.getByTestId('state-dump').textContent || '{}');
}

describe('LLMFormFields — credentials phase', () => {
  test('with no provider selected, Load models button is rendered but disabled', () => {
    render(<ControlledHarness providers={[openaiMeta, bedrockMeta]} initial={emptyLLMFormState()} />);
    expect(screen.getAllByLabelText(/LLM Provider/).length).toBeGreaterThan(0);
    expect(screen.queryByLabelText('API Key')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled();
  });

  test('OpenAI: renders API Key field (required) and Load models button', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: { base_url: 'https://api.openai.com/v1' },
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    // API Key is a password input — find it directly to avoid Mantine's
    // label-association quirks with the required asterisk.
    expect(container.querySelector('input[type="password"]')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Load models' })).toBeInTheDocument();
  });

  test('Bedrock: renders cloud-credentials hint instead of API Key', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.queryByLabelText('API Key')).not.toBeInTheDocument();
    expect(screen.getByText(/uses cloud credentials/i)).toBeInTheDocument();
  });

  test('Load models is disabled when api_key is missing on a credential provider', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: '',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled();
  });

  test('Load models is enabled once api_key is filled', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('Load models is enabled for cloud-creds providers without api_key', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('hasSavedApiKey label switches to "Update API Key" and Load models is enabled with no fresh key', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: '',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} hasSavedApiKey />);
    expect(screen.getByLabelText('Update API Key')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('clicking Load models invokes the onLoadModels callback', async () => {
    const onLoadModels = jest.fn().mockResolvedValue(undefined);
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} onLoadModels={onLoadModels} />);
    fireEvent.click(screen.getByRole('button', { name: 'Load models' }));
    await waitFor(() => expect(onLoadModels).toHaveBeenCalledTimes(1));
  });

  test('typing into the API Key field updates state', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    const passwordInput = container.querySelector('input[type="password"]') as HTMLInputElement;
    expect(passwordInput).not.toBeNull();
    fireEvent.change(passwordInput, { target: { value: 'sk-typed' } });
    expect(getDump().value.apiKey).toBe('sk-typed');
  });
});

describe('LLMFormFields — model phase', () => {
  test('renders LiveModelCombobox in model phase', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} initialPhase="model" />);
    expect(screen.getAllByLabelText(/Model/).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'Back to credentials' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Refresh model list' })).toBeInTheDocument();
  });

  test('shows live-error alert when liveError is supplied', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(
      <ControlledHarness
        providers={[openaiMeta]}
        initial={initial}
        initialPhase="model"
        liveError="API key was rejected"
      />
    );
    expect(screen.getByText(/Could not fetch live model list/)).toBeInTheDocument();
    expect(screen.getByText(/API key was rejected/)).toBeInTheDocument();
  });

  test('Back to credentials returns to credentials phase', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} initialPhase="model" />);
    fireEvent.click(screen.getByRole('button', { name: 'Back to credentials' }));
    expect(getDump().phase).toBe('credentials');
  });

  test('Refresh model list invokes onLoadModels', async () => {
    const onLoadModels = jest.fn().mockResolvedValue(undefined);
    const initial: LLMFormState = {
      provider: 'openai',
      config: {},
      apiKey: 'sk-test',
    };
    render(
      <ControlledHarness
        providers={[openaiMeta]}
        initial={initial}
        initialPhase="model"
        onLoadModels={onLoadModels}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: 'Refresh model list' }));
    await waitFor(() => expect(onLoadModels).toHaveBeenCalledTimes(1));
  });
});

describe('LLMFormFields — provider switch', () => {
  test('switching provider clears config and resets to credentials phase', () => {
    function Wrapper() {
      const [v, setV] = useState<LLMFormState>({
        provider: 'openai',
        config: { base_url: 'https://custom.example.com', model: 'gpt-4' },
        apiKey: 'sk-test',
      });
      const [phase, setPhase] = useState<AIPhase>('model');
      return (
        <MantineProvider>
          <div data-testid="dump">{JSON.stringify({ value: v, phase })}</div>
          <LLMFormFields
            providers={[openaiMeta, bedrockMeta]}
            value={v}
            onChange={setV}
            phase={phase}
            onPhaseChange={setPhase}
            liveModels={null}
            liveError={null}
            loading={false}
            onLoadModels={jest.fn()}
          />
        </MantineProvider>
      );
    }
    const { container } = render(<Wrapper />);
    // Find the actual visible Select input (Mantine renders both visible
    // input and a hidden <select>) — we use the hidden select to drive
    // the change because clicking the dropdown is flaky in jsdom.
    const hiddenSelect = container.querySelector('select[aria-hidden="true"]');
    if (hiddenSelect) {
      fireEvent.change(hiddenSelect, { target: { value: 'bedrock' } });
      const dump = JSON.parse(screen.getByTestId('dump').textContent || '{}');
      expect(dump.value.provider).toBe('bedrock');
      expect(dump.value.apiKey).toBe('');
      // Bedrock declares region: us-east-1 as default
      expect(dump.value.config.region).toBe('us-east-1');
      expect(dump.value.config.model).toBeUndefined();
      expect(dump.phase).toBe('credentials');
    }
  });
});
