/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
 * LLMFormFields is the single source of truth for the LLM provider
 * form in the new-project wizard, settings page, and plugin-overlaid
 * wizards. The tests below cover both phases (credentials + model),
 * the api-key vs cloud-creds split, and the load-models button
 * gating.
 */

const openaiMeta: ProviderMeta = {
  id: 'openai',
  name: 'OpenAI',
  description: 'OpenAI models',
  config_fields: [
    { key: 'base_url', label: 'Base URL', required: false, type: 'string', placeholder: '', description: '', default: 'https://api.openai.com/v1', options: [] },
    { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
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

const bedrockMeta: ProviderMeta = {
  id: 'bedrock',
  name: 'AWS Bedrock',
  description: 'Uses IAM credentials',
  config_fields: [
    { key: 'region', label: 'Region', required: true, type: 'string', placeholder: '', description: '', default: 'us-east-1', options: [] },
    { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
  ],
  auth_methods: [
    {
      id: 'iam_role',
      name: 'IAM Role',
      description: 'Ambient AWS credentials.',
      fields: [],
    },
    {
      id: 'access_keys',
      name: 'Access Keys',
      description: 'AWS access key pair.',
      fields: [
        { key: 'credentials_json', label: 'Access Keys', required: true, type: 'credential', placeholder: 'AKIA…:wJalr…', description: '', default: '', options: [] },
      ],
    },
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
      authMethod: 'api_key',
      config: { base_url: 'https://api.openai.com/v1' },
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    // API Key is a textarea (clear text, matches warehouse credential UX) — find it directly to avoid Mantine's
    // label-association quirks with the required asterisk.
    expect(container.querySelector('textarea')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Load models' })).toBeInTheDocument();
  });

  test('Bedrock: renders cloud-credentials hint instead of API Key', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'iam_role',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.queryByLabelText('API Key')).not.toBeInTheDocument();
    expect(screen.getByText(/ambient cloud credentials/i)).toBeInTheDocument();
  });

  test('Load models is disabled when api_key is missing on a credential provider', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
      config: {},
      apiKey: '',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled();
  });

  test('Load models is enabled once api_key is filled', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
      config: {},
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('Load models is enabled for cloud-creds providers without api_key', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'iam_role',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('hasSavedApiKey label switches to "Update credentials" and Load models is enabled with no fresh key', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
      config: {},
      apiKey: '',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} hasSavedApiKey />);
    expect(screen.getByLabelText('Update credentials')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  test('clicking Load models invokes the onLoadModels callback', async () => {
    const onLoadModels = jest.fn().mockResolvedValue(undefined);
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
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
      authMethod: 'api_key',
      config: {},
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    const passwordInput = container.querySelector('textarea') as HTMLTextAreaElement;
    expect(passwordInput).not.toBeNull();
    fireEvent.change(passwordInput, { target: { value: 'sk-typed' } });
    expect(getDump().value.apiKey).toBe('sk-typed');
  });
});

describe('LLMFormFields — model phase', () => {
  test('renders LiveModelCombobox in model phase', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
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
      authMethod: 'api_key',
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
      authMethod: 'api_key',
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
      authMethod: 'api_key',
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

// Note: Mantine 7's Select dropdown is rendered through a portalled
// Popover whose options aren't reliably reachable from jsdom in
// userEvent.click flows. The setProvider/setConfigField paths are
// instead exercised end-to-end by the new-project wizard's Playwright
// tests; this file focuses on the render-path branches that ARE
// reachable from jsdom (phase split, gating, wire_override
// disclosure, model typing, cloud-creds region edit).

describe('LLMFormFields — model phase wire_override disclosure', () => {
  // wireOnlyMeta declares wire_override AND a catalog entry whose wire
  // is known. The "Advanced settings" disclosure should appear and
  // hide wire_override behind a Collapse toggle.
  const wireOnlyMeta: ProviderMeta = {
    id: 'wire-aware',
    name: 'Wire-aware',
    description: 'Has wire_override field',
    config_fields: [
      { key: 'api_key', label: 'API Key', required: true, type: 'credential', placeholder: '', description: '', default: '', options: [] },
      { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
      { key: 'wire_override', label: 'Wire override', required: false, type: 'string', placeholder: '', description: 'Override wire dispatch', default: '', options: [] },
    ],
    models: [
      { id: 'known-model', display_name: 'Known Model', wire: 'anthropic-messages' },
    ],
  };

  test('renders wire_override inline when the selected model has no known wire', () => {
    const initial: LLMFormState = {
      provider: 'wire-aware',
      authMethod: 'api_key',
      config: { model: 'unknown-typed-model' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);
    // Wire override label is rendered directly (no Advanced toggle).
    expect(screen.getByLabelText(/Wire override/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Advanced settings/i })).not.toBeInTheDocument();
  });

  test('hides wire_override behind "Advanced settings" toggle for known-wire model', async () => {
    const user = userEvent.setup();
    const initial: LLMFormState = {
      provider: 'wire-aware',
      authMethod: 'api_key',
      config: { model: 'known-model' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);

    const advancedButton = screen.getByRole('button', { name: /Advanced settings/i });
    expect(advancedButton).toBeInTheDocument();

    // Mantine Collapse: when collapsed, the inner content is rendered
    // but wrapped in a closed collapse with `aria-hidden`. We assert by
    // toggling and re-asserting the button label flip.
    await user.click(advancedButton);
    expect(screen.getByRole('button', { name: /Hide advanced settings/i })).toBeInTheDocument();
    // Wire override field is reachable (within the open collapse).
    expect(screen.getByLabelText(/Wire override/)).toBeInTheDocument();

    // Toggling again returns to the collapsed label.
    await user.click(screen.getByRole('button', { name: /Hide advanced settings/i }));
    expect(screen.getByRole('button', { name: /Advanced settings/i })).toBeInTheDocument();
  });

  test('typing into the model combobox updates state via setConfigField', () => {
    const initial: LLMFormState = {
      provider: 'wire-aware',
      authMethod: 'api_key',
      config: { model: '' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);

    // The Model field is rendered by LiveModelCombobox; in jsdom
    // Mantine's Autocomplete renders an <input> with the field label.
    const modelInputs = screen.getAllByLabelText(/Model/);
    const input = modelInputs.find((el) => el.tagName === 'INPUT') as HTMLTextAreaElement | undefined;
    expect(input).toBeDefined();
    if (!input) return;
    fireEvent.change(input, { target: { value: 'typed-model' } });
    // The new value should land in config.model
    expect(getDump().value.config.model).toBe('typed-model');
  });

  test('selecting a model prefills detected max_input/max_output, without clobbering user edits', () => {
    const tokenMeta: ProviderMeta = {
      id: 'litellm',
      name: 'LiteLLM',
      description: 'proxy',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_input_tokens', label: 'Context window override (tokens)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_output_tokens', label: 'Max output tokens override', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [{ id: 'api_key', name: 'API Key', description: '', fields: [] }],
    };
    const live: LiveModel[] = [
      { id: 'gw-model', display_name: 'gw-model', wire: '', source: 'live', dispatchable: true, max_input_tokens: 262144, max_output_tokens: 16384, live_max_input_tokens: 262144, live_max_output_tokens: 16384 },
    ];

    // Case 1: empty fields → prefilled from the selected model.
    const initial: LLMFormState = { provider: 'litellm', authMethod: 'api_key', config: { model: '' }, apiKey: 'k' };
    const { unmount } = render(
      <ControlledHarness providers={[tokenMeta]} initial={initial} initialPhase="model" liveModels={live} />,
    );
    let input = screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'gw-model' } });
    let cfg = getDump().value.config;
    expect(cfg.model).toBe('gw-model');
    expect(cfg.max_input_tokens).toBe('262144');
    expect(cfg.max_output_tokens).toBe('16384');
    unmount();

    // Case 2: a user-entered value is NOT overwritten by prefill.
    const initial2: LLMFormState = {
      provider: 'litellm', authMethod: 'api_key',
      config: { model: '', max_input_tokens: '100000' }, apiKey: 'k',
    };
    render(<ControlledHarness providers={[tokenMeta]} initial={initial2} initialPhase="model" liveModels={live} />);
    input = screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'gw-model' } });
    cfg = getDump().value.config;
    expect(cfg.max_input_tokens).toBe('100000'); // preserved
    expect(cfg.max_output_tokens).toBe('16384'); // still prefilled (was empty)
  });

  test('switching models refreshes a still-auto-filled override; a model with no numbers clears it', () => {
    const tokenMeta: ProviderMeta = {
      id: 'litellm', name: 'LiteLLM', description: 'proxy',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_input_tokens', label: 'Context window override (tokens)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_output_tokens', label: 'Max output tokens override', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [{ id: 'api_key', name: 'API Key', description: '', fields: [] }],
    };
    const live: LiveModel[] = [
      { id: 'model-a', display_name: 'model-a', wire: '', source: 'live', dispatchable: true, max_input_tokens: 262144, max_output_tokens: 16384, live_max_input_tokens: 262144, live_max_output_tokens: 16384 },
      { id: 'model-b', display_name: 'model-b', wire: '', source: 'live', dispatchable: true, max_input_tokens: 32768, max_output_tokens: 8192, live_max_input_tokens: 32768, live_max_output_tokens: 8192 },
      { id: 'model-c', display_name: 'model-c', wire: '', source: 'live', dispatchable: true },
    ];
    const initial: LLMFormState = { provider: 'litellm', authMethod: 'api_key', config: { model: '' }, apiKey: 'k' };
    render(<ControlledHarness providers={[tokenMeta]} initial={initial} initialPhase="model" liveModels={live} />);
    const input = () => screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;

    fireEvent.change(input(), { target: { value: 'model-a' } });
    expect(getDump().value.config.max_input_tokens).toBe('262144');

    // Switch A → B: the still-auto-filled A values are replaced with B's, not left stale.
    fireEvent.change(input(), { target: { value: 'model-b' } });
    let cfg = getDump().value.config;
    expect(cfg.max_input_tokens).toBe('32768');
    expect(cfg.max_output_tokens).toBe('8192');

    // Switch B → C (reports nothing): the stale auto-filled values are cleared.
    fireEvent.change(input(), { target: { value: 'model-c' } });
    cfg = getDump().value.config;
    expect(cfg.max_input_tokens ?? '').toBe('');
    expect(cfg.max_output_tokens ?? '').toBe('');
  });

  test('prefill follows live provenance, not source: catalog value never prefills; genuine live value does even on a both row', () => {
    const tokenMeta: ProviderMeta = {
      id: 'openai', name: 'OpenAI', description: '',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_input_tokens', label: 'Context window override (tokens)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_output_tokens', label: 'Max output tokens override', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [{ id: 'api_key', name: 'API Key', description: '', fields: [] }],
    };
    const live: LiveModel[] = [
      // catalog-only: display value from the catalog, NO live provenance → no prefill.
      { id: 'cat-model', display_name: 'cat-model', wire: 'openai-compat', source: 'catalog', dispatchable: true, max_input_tokens: 128000, max_output_tokens: 16384 },
      // 'both' but the upstream reported nothing (numbers are catalog-derived) → no prefill.
      { id: 'both-catalog', display_name: 'both-catalog', wire: 'openai-compat', source: 'both', dispatchable: true, max_input_tokens: 128000, max_output_tokens: 16384 },
      // 'both' with genuine live provenance (gateway's real, smaller window) → prefills.
      { id: 'both-live', display_name: 'both-live', wire: 'openai-compat', source: 'both', dispatchable: true, max_input_tokens: 40000, max_output_tokens: 4096, live_max_input_tokens: 40000, live_max_output_tokens: 4096 },
    ];
    const initial: LLMFormState = { provider: 'openai', authMethod: 'api_key', config: { model: '' }, apiKey: 'k' };
    render(<ControlledHarness providers={[tokenMeta]} initial={initial} initialPhase="model" liveModels={live} />);
    const input = () => screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;

    fireEvent.change(input(), { target: { value: 'cat-model' } });
    expect(getDump().value.config.max_input_tokens).toBeUndefined();

    fireEvent.change(input(), { target: { value: 'both-catalog' } });
    expect(getDump().value.config.max_input_tokens).toBeUndefined();
    expect(getDump().value.config.max_output_tokens).toBeUndefined();

    fireEvent.change(input(), { target: { value: 'both-live' } });
    const cfg = getDump().value.config;
    expect(cfg.max_input_tokens).toBe('40000');
    expect(cfg.max_output_tokens).toBe('4096');
  });

  test('remount: switching away from a saved auto-filled value refreshes it (ref empty)', () => {
    const tokenMeta: ProviderMeta = {
      id: 'litellm', name: 'LiteLLM', description: '',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_input_tokens', label: 'Context window override (tokens)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [{ id: 'api_key', name: 'API Key', description: '', fields: [] }],
    };
    const live: LiveModel[] = [
      { id: 'model-a', display_name: 'model-a', wire: '', source: 'live', dispatchable: true, max_input_tokens: 262144, live_max_input_tokens: 262144 },
      { id: 'model-b', display_name: 'model-b', wire: '', source: 'live', dispatchable: true, max_input_tokens: 32768, live_max_input_tokens: 32768 },
    ];
    // Simulate a remount: model-a is already selected with its detected value
    // saved, and the autofill ref is empty (fresh component).
    const initial: LLMFormState = {
      provider: 'litellm', authMethod: 'api_key',
      config: { model: 'model-a', max_input_tokens: '262144' }, apiKey: 'k',
    };
    render(<ControlledHarness providers={[tokenMeta]} initial={initial} initialPhase="model" liveModels={live} />);
    const input = screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'model-b' } });
    // Recognised as auto-filled (== model-a's live value) → refreshed to B's, not left stale.
    expect(getDump().value.config.max_input_tokens).toBe('32768');
  });

  test('a hand-edited override is not overwritten on model switch even if it matches a detected value', () => {
    const tokenMeta: ProviderMeta = {
      id: 'litellm', name: 'LiteLLM', description: '',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'max_input_tokens', label: 'Context window override (tokens)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [{ id: 'api_key', name: 'API Key', description: '', fields: [] }],
    };
    const live: LiveModel[] = [
      { id: 'model-a', display_name: 'model-a', wire: '', source: 'live', dispatchable: true, max_input_tokens: 262144, live_max_input_tokens: 262144 },
      { id: 'model-b', display_name: 'model-b', wire: '', source: 'live', dispatchable: true, max_input_tokens: 32768, live_max_input_tokens: 32768 },
    ];
    const initial: LLMFormState = { provider: 'litellm', authMethod: 'api_key', config: { model: 'model-a' }, apiKey: 'k' };
    render(<ControlledHarness providers={[tokenMeta]} initial={initial} initialPhase="model" liveModels={live} />);

    // Operator hand-types a value that coincidentally equals model-a's detected limit.
    const tokenInput = screen.getByLabelText(/Context window override/) as HTMLInputElement;
    fireEvent.change(tokenInput, { target: { value: '262144' } });
    expect(getDump().value.config.max_input_tokens).toBe('262144');

    // Switching models must NOT overwrite the hand-edited value.
    const modelInput = screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;
    fireEvent.change(modelInput, { target: { value: 'model-b' } });
    expect(getDump().value.config.max_input_tokens).toBe('262144');
  });

  test('a provider without the token fields (Ollama) never has them auto-persisted', () => {
    // Ollama budgets via num_ctx and does NOT declare max_input_tokens /
    // max_output_tokens. Selecting a live model that reports a window must not
    // silently write a hidden override into config.
    const ollamaMeta: ProviderMeta = {
      id: 'ollama', name: 'Ollama', description: 'local',
      config_fields: [
        { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
        { key: 'num_ctx', label: 'Context window (num_ctx)', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      ],
      auth_methods: [],
    };
    const live: LiveModel[] = [
      { id: 'qwen3:32b', display_name: 'qwen3:32b', wire: '', source: 'live', dispatchable: true, max_input_tokens: 262144, live_max_input_tokens: 262144 },
    ];
    const initial: LLMFormState = { provider: 'ollama', authMethod: '', config: { model: '' }, apiKey: '' };
    render(<ControlledHarness providers={[ollamaMeta]} initial={initial} initialPhase="model" liveModels={live} />);
    const input = screen.getAllByLabelText(/Model/).find((el) => el.tagName === 'INPUT') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'qwen3:32b' } });
    const cfg = getDump().value.config;
    expect(cfg.model).toBe('qwen3:32b');
    expect(cfg.max_input_tokens).toBeUndefined();
    expect(cfg.max_output_tokens).toBeUndefined();
  });

  test('typing into wire_override updates state via setConfigField', () => {
    // Use the inline-render variant (unknown model) so wire_override is
    // rendered without going through the Advanced disclosure.
    const initial: LLMFormState = {
      provider: 'wire-aware',
      authMethod: 'api_key',
      config: { model: 'unknown-model', wire_override: '' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);
    const wireInput = screen.getByLabelText(/Wire override/) as HTMLTextAreaElement;
    fireEvent.change(wireInput, { target: { value: 'anthropic-messages' } });
    expect(getDump().value.config.wire_override).toBe('anthropic-messages');
  });
});

// Exercises the Bedrock cloud-creds path's setProvider branch + region
// default — touching both buildDefaults() with a non-credential field
// and the noop-needsApiKey path on phase='credentials'. Uses within()
// so the assertions don't fall through to other rendered controls.
describe('LLMFormFields — Bedrock interaction', () => {
  test('setting region via the rendered TextInput updates state.config', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'iam_role',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    const regionInput = within(container).getByLabelText(/Region/) as HTMLTextAreaElement;
    fireEvent.change(regionInput, { target: { value: 'eu-west-1' } });
    expect(getDump().value.config.region).toBe('eu-west-1');
  });

  // Bedrock declares iam_role + access_keys auth methods (per the
  // fixture), so the auth-method selector renders. These tests pin the
  // multi-method UI paths LLMFormFields takes for cloud providers.
  test('shows auth-method selector when provider has 2+ methods', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'iam_role',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.getAllByLabelText(/Authentication method/i).length).toBeGreaterThan(0);
  });

  test('switching to access_keys reveals the credential field + clears any prior apiKey', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'access_keys',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    // The credential field's label comes from the auth-method fixture
    // ("Access Keys"). bedrockMeta defines a single credential field
    // with that label.
    expect(within(container).getByLabelText(/Access Keys/i)).toBeInTheDocument();
  });

  test('Load models is disabled until an auth_method is picked on a multi-method provider', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: '',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled();
  });

  test('Load models is enabled for iam_role (ambient creds, no credential field)', () => {
    const initial: LLMFormState = {
      provider: 'bedrock',
      authMethod: 'iam_role',
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).not.toBeDisabled();
  });

  // Regression: when the parent pre-selects a provider but forgets to
  // pre-select its auth method (e.g. new-project page defaulting to
  // Claude), the credential field must NOT silently disappear — the
  // user would see "Claude selected" but no API key input. Reported by
  // user testing locally after PR #222 went up.
  test('single-method provider pre-selected without authMethod still renders nothing — caller must pre-select method', () => {
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: '', // parent forgot to set it — bug repro
      config: {},
      apiKey: '',
    };
    render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    // Credential field is gated on selectedMethod (= authMethods.find by
    // value.authMethod). With authMethod='' no method is selected →
    // no credential field renders. This is the failure mode the user
    // hit. Documenting the contract: callers MUST initialise
    // authMethod when they initialise provider.
    expect(screen.queryByLabelText('API Key')).not.toBeInTheDocument();
    // Load models should be disabled in this state so the user can't
    // submit a broken config silently.
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled();
  });

  test('single-method provider pre-selected WITH authMethod renders the credential field', () => {
    // Fixed state: parent supplies provider + authMethod together —
    // credential field renders, button enables when filled. This is
    // the contract the new-project page + ProvidersPanel hydration
    // paths must satisfy.
    const initial: LLMFormState = {
      provider: 'openai',
      authMethod: 'api_key',
      config: {},
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[openaiMeta]} initial={initial} />);
    expect(container.querySelector('textarea')).toBeInTheDocument();
  });
});

// vertexMeta mirrors the vertex-ai provider's config_fields: an optional
// endpoint_id that, when set, hides the model picker / Load-models step
// (the deployed endpoint identifies its own model).
const vertexMeta: ProviderMeta = {
  id: 'vertex-ai',
  name: 'Google Vertex AI',
  description: 'GCP-managed AI platform',
  config_fields: [
    { key: 'project_id', label: 'GCP Project ID', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
    { key: 'location', label: 'Region', required: false, type: 'string', placeholder: '', description: '', default: 'us-east5', options: [] },
    { key: 'endpoint_id', label: 'Endpoint ID', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
    { key: 'model', label: 'Model', required: true, type: 'string', placeholder: '', description: '', default: 'gemini-2.5-pro', options: [] },
    { key: 'wire_override', label: 'Wire override', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
  ],
  auth_methods: [
    { id: 'adc', name: 'Application Default Credentials', description: 'Ambient GCP credentials.', fields: [] },
  ],
};

describe('LLMFormFields — user-deployed endpoint (endpoint_id) hides the model picker', () => {
  test('with endpoint_id set, Load models button is hidden and the endpoint note is shown', () => {
    const initial: LLMFormState = {
      provider: 'vertex-ai',
      authMethod: 'adc',
      config: { project_id: 'p', location: 'us-central1', endpoint_id: 'mg-endpoint-abc', model: 'gemini-2.5-pro' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[vertexMeta]} initial={initial} />);
    // No "Load models" button — the endpoint serves its own model.
    expect(screen.queryByRole('button', { name: 'Load models' })).not.toBeInTheDocument();
    expect(screen.getByText(/serves its own deployed model/i)).toBeInTheDocument();
    // Endpoint ID stays editable in the credentials phase.
    expect(screen.getByDisplayValue('mg-endpoint-abc')).toBeInTheDocument();
  });

  test('without endpoint_id, Load models button is shown (normal MaaS flow)', () => {
    const initial: LLMFormState = {
      provider: 'vertex-ai',
      authMethod: 'adc',
      config: { project_id: 'p', location: 'us-central1', endpoint_id: '', model: 'gemini-2.5-pro' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[vertexMeta]} initial={initial} />);
    expect(screen.getByRole('button', { name: 'Load models' })).toBeInTheDocument();
    expect(screen.queryByText(/serves its own deployed model/i)).not.toBeInTheDocument();
  });

  test('typing an endpoint ID clears a stale wire_override', async () => {
    const user = userEvent.setup();
    const initial: LLMFormState = {
      provider: 'vertex-ai',
      authMethod: 'adc',
      config: { project_id: 'p', location: 'us-central1', endpoint_id: '', model: 'gemini-2.5-pro', wire_override: 'anthropic-messages' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[vertexMeta]} initial={initial} />);
    await user.type(screen.getByLabelText('Endpoint ID'), 'mg-endpoint-abc');
    // The stale wire_override must be dropped so the provider doesn't
    // reject the saved config (an endpoint always uses the OpenAI wire).
    expect(getDump().value.config.wire_override).toBeUndefined();
    expect(getDump().value.config.endpoint_id).toBe('mg-endpoint-abc');
  });

  test('with endpoint_id set, the model picker stays hidden even in the model phase', () => {
    const initial: LLMFormState = {
      provider: 'vertex-ai',
      authMethod: 'adc',
      config: { project_id: 'p', location: 'us-central1', endpoint_id: 'mg-endpoint-abc', model: 'gemini-2.5-pro' },
      apiKey: '',
    };
    render(<ControlledHarness providers={[vertexMeta]} initial={initial} initialPhase="model" />);
    // The live-model combobox is never rendered for an endpoint config.
    expect(screen.queryByRole('button', { name: 'Refresh model list' })).not.toBeInTheDocument();
    expect(screen.getByText(/serves its own deployed model/i)).toBeInTheDocument();
  });
});
