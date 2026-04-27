/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { useState } from 'react';
import {
  WarehouseFormFields,
  WarehouseFormState,
  emptyWarehouseFormState,
  buildDefaults,
  DynamicField,
} from '@/components/projects/WarehouseFormFields';
import type { ConfigField, ProviderMeta } from '@/lib/api';

/**
 * WarehouseFormFields is the single source of truth for warehouse-form
 * rendering. Consumed by:
 *   - projects/new/page.tsx       (new-project wizard)
 *   - WarehouseConfigPanel.tsx    (settings tab + pack-gen wizard)
 *
 * These tests cover the full provider/auth/credential/filter contract
 * and lock in the metadata-driven behavior so the BigQuery-flavored
 * "Datasets" copy never reappears for non-BigQuery providers.
 */

const bigqueryMeta: ProviderMeta = {
  id: 'bigquery',
  name: 'BigQuery',
  description: 'Google BigQuery',
  config_fields: [
    { key: 'project_id', label: 'GCP Project ID', required: true, type: 'string', placeholder: 'my-gcp-project', description: '', default: '', options: [] },
    { key: 'dataset', label: 'Datasets', required: true, type: 'string', placeholder: 'events_prod, features_prod', description: 'Comma-separated dataset names', default: '', options: [] },
    { key: 'location', label: 'Location', required: false, type: 'string', placeholder: 'US', description: '', default: 'US', options: [] },
  ],
  auth_methods: [
    { id: 'adc', name: 'Application Default Credentials', description: 'Use the workload identity assigned to the pod', fields: [] },
    {
      id: 'service_account',
      name: 'Service Account Key',
      description: 'Paste a JSON key',
      fields: [
        { key: 'credentials_json', label: 'Credentials JSON', required: true, type: 'credential', placeholder: '', description: '', default: '', options: [] },
      ],
    },
  ],
};

const mssqlMeta: ProviderMeta = {
  id: 'mssql',
  name: 'Microsoft SQL Server',
  description: 'SQL Server',
  config_fields: [
    { key: 'host', label: 'Host', required: true, type: 'string', placeholder: 'mssql.example.com', description: '', default: '', options: [] },
    { key: 'port', label: 'Port', required: false, type: 'string', placeholder: '', description: '', default: '1433', options: [] },
    { key: 'database', label: 'Database', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
    { key: 'user', label: 'Username', required: true, type: 'string', placeholder: '', description: '', default: '', options: [] },
    { key: 'dataset', label: 'Schema', required: true, type: 'string', placeholder: '', description: 'SQL Server schema to explore', default: 'dbo', options: [] },
    { key: 'encrypt', label: 'Encrypt', required: false, type: 'string', placeholder: '', description: 'Encrypt TDS connection', default: 'true', options: [] },
    { key: 'trust_server_certificate', label: 'Trust Server Certificate', required: false, type: 'string', placeholder: '', description: 'Skip TLS cert validation', default: 'false', options: [] },
  ],
  auth_methods: [
    {
      id: 'sql_auth',
      name: 'SQL Authentication',
      description: 'Username + password',
      fields: [
        { key: 'password', label: 'Password', required: true, type: 'credential', placeholder: '', description: '', default: '', options: [] },
      ],
    },
  ],
};

function ControlledHarness({
  providers,
  initial,
  hasSavedCredential,
}: {
  providers: ProviderMeta[];
  initial: WarehouseFormState;
  hasSavedCredential?: boolean;
}) {
  const [v, setV] = useState<WarehouseFormState>(initial);
  return (
    <MantineProvider>
      <div data-testid="state-dump">{JSON.stringify(v)}</div>
      <WarehouseFormFields providers={providers} value={v} onChange={setV} hasSavedCredential={hasSavedCredential} />
    </MantineProvider>
  );
}

function getDump() {
  return JSON.parse(screen.getByTestId('state-dump').textContent || '{}');
}

describe('buildDefaults', () => {
  test('extracts non-empty default values', () => {
    const fields: ConfigField[] = [
      { key: 'a', label: 'A', required: false, type: 'string', placeholder: '', description: '', default: 'aval', options: [] },
      { key: 'b', label: 'B', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] },
      { key: 'c', label: 'C', required: false, type: 'string', placeholder: '', description: '', default: 'cval', options: [] },
    ];
    expect(buildDefaults(fields)).toEqual({ a: 'aval', c: 'cval' });
  });

  test('returns empty object for no fields', () => {
    expect(buildDefaults([])).toEqual({});
  });
});

describe('DynamicField', () => {
  test('renders TextInput for string field', () => {
    const field: ConfigField = { key: 'host', label: 'Host', required: true, type: 'string', placeholder: 'h', description: '', default: '', options: [] };
    render(<MantineProvider><DynamicField field={field} value="" onChange={() => {}} /></MantineProvider>);
    expect(screen.getByLabelText(/Host/)).toBeInTheDocument();
  });

  test('renders Textarea for textarea field', () => {
    const field: ConfigField = { key: 'json', label: 'JSON', required: false, type: 'textarea', placeholder: '', description: '', default: '', options: [] };
    const { container } = render(<MantineProvider><DynamicField field={field} value="" onChange={() => {}} /></MantineProvider>);
    expect(container.querySelector('textarea')).toBeInTheDocument();
  });

  test('uses field.default as placeholder when no placeholder set', () => {
    const field: ConfigField = { key: 'port', label: 'Port', required: false, type: 'string', placeholder: '', description: '', default: '5432', options: [] };
    render(<MantineProvider><DynamicField field={field} value="" onChange={() => {}} /></MantineProvider>);
    expect(screen.getByPlaceholderText('5432')).toBeInTheDocument();
  });

  test('fires onChange when typed into', () => {
    const field: ConfigField = { key: 'host', label: 'Host', required: false, type: 'string', placeholder: '', description: '', default: '', options: [] };
    const onChange = jest.fn();
    render(<MantineProvider><DynamicField field={field} value="" onChange={onChange} /></MantineProvider>);
    fireEvent.change(screen.getByLabelText(/Host/), { target: { value: 'db.local' } });
    expect(onChange).toHaveBeenCalledWith('db.local');
  });
});

describe('WarehouseFormFields — provider rendering', () => {
  test('with no provider selected, renders only the provider Select + filter fields', () => {
    render(<ControlledHarness providers={[bigqueryMeta, mssqlMeta]} initial={emptyWarehouseFormState()} />);
    expect(screen.getAllByLabelText(/Warehouse Provider/).length).toBeGreaterThan(0);
    // No config/auth/credential fields yet
    expect(screen.queryByLabelText(/Schema/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Authentication/)).not.toBeInTheDocument();
    // Filter fields ARE always rendered
    expect(screen.getByLabelText(/Filter Field/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Filter Value/)).toBeInTheDocument();
  });

  test('MSSQL: renders Schema (NOT "Datasets") and pre-fills encrypt/trust_server_certificate from defaults', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'mssql',
      config: buildDefaults(mssqlMeta.config_fields),
      authMethod: 'sql_auth',
    };
    render(<ControlledHarness providers={[mssqlMeta]} initial={initial} />);
    // Schema field is rendered (not "Datasets")
    const schemaInput = screen.getByLabelText(/Schema/) as HTMLInputElement;
    expect(schemaInput.value).toBe('dbo');
    // BigQuery-flavored "Datasets" label is NOT present for MSSQL (regression)
    expect(screen.queryByLabelText('Datasets')).not.toBeInTheDocument();
    expect((screen.getByLabelText(/Encrypt/) as HTMLInputElement).value).toBe('true');
    expect((screen.getByLabelText(/Trust Server Certificate/) as HTMLInputElement).value).toBe('false');
  });

  test('BigQuery: renders the BigQuery-flavored Datasets label', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} />);
    expect(screen.getByLabelText(/Datasets/)).toBeInTheDocument();
    // Schema label belongs to other warehouses
    expect(screen.queryByLabelText(/^Schema$/)).not.toBeInTheDocument();
  });

  test('config field changes propagate via onChange', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'mssql',
      config: buildDefaults(mssqlMeta.config_fields),
      authMethod: 'sql_auth',
    };
    render(<ControlledHarness providers={[mssqlMeta]} initial={initial} />);
    fireEvent.change(screen.getByLabelText(/^Host/), { target: { value: 'db.local' } });
    expect(getDump().config.host).toBe('db.local');
  });
});

describe('WarehouseFormFields — auth methods', () => {
  test('renders auth method Select only when provider declares >0 methods', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} />);
    expect(screen.getAllByLabelText(/Authentication/).length).toBeGreaterThan(0);
  });

  test('selected auth method (with credential) renders the credential textarea', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
      authMethod: 'service_account',
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} />);
    expect(screen.getByLabelText(/Credentials JSON/)).toBeInTheDocument();
  });

  test('selected auth method (without credential) does not render a credential textarea', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
      authMethod: 'adc',
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} />);
    expect(screen.queryByLabelText(/Credentials JSON/)).not.toBeInTheDocument();
  });

  test('hasSavedCredential changes the credential label to "Update <X>"', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
      authMethod: 'service_account',
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} hasSavedCredential />);
    expect(screen.getByLabelText(/Update Credentials JSON/)).toBeInTheDocument();
  });

  test('credential value changes propagate via onChange', () => {
    const initial: WarehouseFormState = {
      ...emptyWarehouseFormState(),
      provider: 'bigquery',
      config: buildDefaults(bigqueryMeta.config_fields),
      authMethod: 'service_account',
    };
    render(<ControlledHarness providers={[bigqueryMeta]} initial={initial} />);
    fireEvent.change(screen.getByLabelText(/Credentials JSON/), { target: { value: 'secret-json' } });
    expect(getDump().credential).toBe('secret-json');
  });
});

describe('WarehouseFormFields — filter fields', () => {
  test('filter field changes propagate via onChange', () => {
    render(<ControlledHarness providers={[mssqlMeta]} initial={emptyWarehouseFormState()} />);
    fireEvent.change(screen.getByLabelText(/Filter Field/), { target: { value: 'app_id' } });
    fireEvent.change(screen.getByLabelText(/Filter Value/), { target: { value: 'my-app' } });
    const dump = getDump();
    expect(dump.filterField).toBe('app_id');
    expect(dump.filterValue).toBe('my-app');
  });
});
