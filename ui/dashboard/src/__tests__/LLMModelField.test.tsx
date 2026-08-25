/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { useState } from 'react';
import { DynamicField } from '@/components/common/LLMModelField';
import type { ConfigField } from '@/lib/api';

/**
 * Covers the custom-TLS field types added for #338:
 *   - boolean (tls_skip_verify) → Mantine Switch + insecure warning
 *   - file    (tls_ca_cert)     → Textarea + "Load from file" affordance
 */

function field(overrides: Partial<ConfigField>): ConfigField {
  return {
    key: 'k',
    label: 'L',
    required: false,
    type: 'string',
    placeholder: '',
    description: '',
    default: '',
    options: [],
    ...overrides,
  };
}

function Controlled({ f, initial }: { f: ConfigField; initial: string }) {
  const [v, setV] = useState(initial);
  return (
    <MantineProvider>
      <DynamicField field={f} value={v} onChange={setV} />
    </MantineProvider>
  );
}

describe('DynamicField — boolean (tls_skip_verify)', () => {
  test('renders a switch and round-trips true/false', () => {
    const onChange = jest.fn();
    const f = field({ key: 'tls_skip_verify', label: 'Disable TLS verification', type: 'boolean', default: 'false' });
    render(
      <MantineProvider>
        <DynamicField field={f} value="false" onChange={onChange} />
      </MantineProvider>,
    );
    const sw = screen.getByRole('switch');
    expect(sw).not.toBeChecked();
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith('true');
  });

  test('shows an insecure warning only when enabled', () => {
    const f = field({ key: 'tls_skip_verify', label: 'Disable TLS verification', type: 'boolean' });
    const { rerender } = render(
      <MantineProvider>
        <DynamicField field={f} value="false" onChange={() => {}} />
      </MantineProvider>,
    );
    expect(screen.queryByText(/verification is off/i)).not.toBeInTheDocument();

    rerender(
      <MantineProvider>
        <DynamicField field={f} value="true" onChange={() => {}} />
      </MantineProvider>,
    );
    expect(screen.getByText(/verification is off/i)).toBeInTheDocument();
  });
});

describe('DynamicField — file (tls_ca_cert)', () => {
  test('renders a textarea plus a load-from-file button', () => {
    const f = field({ key: 'tls_ca_cert', label: 'Custom CA certificate (PEM)', type: 'file' });
    render(<Controlled f={f} initial="" />);
    // Textarea present.
    expect(screen.getByRole('textbox')).toBeInTheDocument();
    // Upload affordance present.
    expect(screen.getByText(/load from file/i)).toBeInTheDocument();
  });

  test('typing a PEM into the textarea updates the value', () => {
    const onChange = jest.fn();
    const f = field({ key: 'tls_ca_cert', label: 'Custom CA certificate (PEM)', type: 'file' });
    render(
      <MantineProvider>
        <DynamicField field={f} value="" onChange={onChange} />
      </MantineProvider>,
    );
    fireEvent.change(screen.getByRole('textbox'), { target: { value: '-----BEGIN CERTIFICATE-----' } });
    expect(onChange).toHaveBeenCalledWith('-----BEGIN CERTIFICATE-----');
  });

  test('Clear button appears when a value is present and empties it', () => {
    const f = field({ key: 'tls_ca_cert', label: 'Custom CA certificate (PEM)', type: 'file' });
    const onChange = jest.fn();
    render(
      <MantineProvider>
        <DynamicField field={f} value="some-pem" onChange={onChange} />
      </MantineProvider>,
    );
    fireEvent.click(screen.getByText(/clear/i));
    expect(onChange).toHaveBeenCalledWith('');
  });
});
