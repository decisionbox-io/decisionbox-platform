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
      config: { model: '' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);

    // The Model field is rendered by LiveModelCombobox; in jsdom
    // Mantine's Autocomplete renders an <input> with the field label.
    const modelInputs = screen.getAllByLabelText(/Model/);
    const input = modelInputs.find((el) => el.tagName === 'INPUT') as HTMLInputElement | undefined;
    expect(input).toBeDefined();
    if (!input) return;
    fireEvent.change(input, { target: { value: 'typed-model' } });
    // The new value should land in config.model
    expect(getDump().value.config.model).toBe('typed-model');
  });

  test('typing into wire_override updates state via setConfigField', () => {
    // Use the inline-render variant (unknown model) so wire_override is
    // rendered without going through the Advanced disclosure.
    const initial: LLMFormState = {
      provider: 'wire-aware',
      config: { model: 'unknown-model', wire_override: '' },
      apiKey: 'sk-test',
    };
    render(<ControlledHarness providers={[wireOnlyMeta]} initial={initial} initialPhase="model" />);
    const wireInput = screen.getByLabelText(/Wire override/) as HTMLInputElement;
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
      config: { region: 'us-east-1' },
      apiKey: '',
    };
    const { container } = render(<ControlledHarness providers={[bedrockMeta]} initial={initial} />);
    const regionInput = within(container).getByLabelText(/Region/) as HTMLInputElement;
    fireEvent.change(regionInput, { target: { value: 'eu-west-1' } });
    expect(getDump().value.config.region).toBe('eu-west-1');
  });
});
