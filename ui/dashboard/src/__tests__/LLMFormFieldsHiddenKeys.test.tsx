/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { LLMFormFields, emptyLLMFormState } from '@/components/projects/LLMFormFields';

// One setting, one control. A config field whose authoritative control lives
// elsewhere must not get a second switch here: on the settings page the
// project's prompt_cache_enabled field overrides llm.config.prompt_cache, so a
// switch bound to the config key would report success while changing nothing.
const PROVIDER = {
  id: 'bedrock',
  name: 'Bedrock',
  auth_methods: [{ id: 'access_keys', name: 'Access keys', fields: [] }],
  config_fields: [
    { key: 'region', label: 'Region', type: 'string' },
    { key: 'prompt_cache', label: 'Prompt caching (Claude models)', type: 'boolean', default: 'true' },
  ],
};

function renderForm(hiddenConfigKeys?: string[]) {
  render(
    <MantineProvider>
      <LLMFormFields
        providers={[PROVIDER] as never}
        value={{ ...emptyLLMFormState(), provider: 'bedrock', authMethod: 'access_keys' }}
        onChange={() => {}}
        phase="credentials"
        onPhaseChange={() => {}}
        liveModels={null}
        liveError={null}
        loading={false}
        onLoadModels={() => {}}
        hiddenConfigKeys={hiddenConfigKeys}
      />
    </MantineProvider>,
  );
}

it('renders a provider config field by default', () => {
  renderForm();
  expect(screen.getByRole('switch', { name: /prompt caching/i })).toBeInTheDocument();
  expect(screen.getByLabelText(/region/i)).toBeInTheDocument();
});

it('hides a config field the host owns, and only that one', () => {
  renderForm(['prompt_cache']);
  expect(screen.queryByRole('switch', { name: /prompt caching/i })).not.toBeInTheDocument();
  expect(screen.getByLabelText(/region/i)).toBeInTheDocument();
});
