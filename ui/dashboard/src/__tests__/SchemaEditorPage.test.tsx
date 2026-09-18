/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import SchemaEditorPage from '@/app/projects/[id]/settings/schema-editor/page';
import type { SchemaEditorTable } from '@/lib/api';

jest.mock('next/navigation', () => ({
  useParams: () => ({ id: 'p1' }),
  useRouter: () => ({ push: jest.fn() }),
}));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true,
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

// Gate control — toggled per test to exercise the read-only path.
let mockCanEdit = true;
jest.mock('@/components/PermissionProvider', () => ({
  __esModule: true,
  usePermissions: () => ({ hasRole: () => mockCanEdit, loading: false }),
}));

const getProject = jest.fn();
const listSchemaEditorTables = jest.fn();
const listSchemaEdits = jest.fn();
const updateSchemaEditorTable = jest.fn();
const deleteSchemaEditorTable = jest.fn();

jest.mock('@/lib/api', () => ({
  __esModule: true,
  resolvePrimaryDatasourceId: () => 'default',
  api: {
    getProject: (...a: unknown[]) => getProject(...a),
    listSchemaEditorTables: (...a: unknown[]) => listSchemaEditorTables(...a),
    listSchemaEdits: (...a: unknown[]) => listSchemaEdits(...a),
    updateSchemaEditorTable: (...a: unknown[]) => updateSchemaEditorTable(...a),
    deleteSchemaEditorTable: (...a: unknown[]) => deleteSchemaEditorTable(...a),
  },
}));

const ordersTable: SchemaEditorTable = {
  table: 'dbo.orders',
  row_count: 100,
  columns: [
    { name: 'id', type: 'int', nullable: false, category: 'primary_key' },
    { name: 'amount', type: 'numeric', nullable: true, category: 'metric' },
  ],
  blurb: 'All customer orders.',
  keywords: ['orders', 'revenue'],
  has_blurb: true,
};

function mount() {
  return render(
    <MantineProvider>
      <SchemaEditorPage />
    </MantineProvider>,
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  mockCanEdit = true;
  getProject.mockResolvedValue({ id: 'p1', name: 'P1', warehouse: { provider: 'postgres' } });
  listSchemaEditorTables.mockResolvedValue({ tables: [ordersTable], total: 1, truncated: false, datasource_id: 'default' });
  listSchemaEdits.mockResolvedValue({ edits: [], since_last_index: 0 });
});

describe('SchemaEditorPage', () => {
  it('lists tables and shows a table detail on selection', async () => {
    mount();
    const tableBtn = await screen.findByRole('button', { name: /dbo\.orders/ });
    fireEvent.click(tableBtn);
    expect(await screen.findByDisplayValue('All customer orders.')).toBeInTheDocument();
    // Columns render.
    expect(screen.getByText('amount')).toBeInTheDocument();
  });

  it('saves a blurb edit', async () => {
    updateSchemaEditorTable.mockResolvedValue({ ...ordersTable, blurb: 'Rewritten.' });
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /dbo\.orders/ }));
    const blurb = await screen.findByDisplayValue('All customer orders.');
    fireEvent.change(blurb, { target: { value: 'Rewritten.' } });
    fireEvent.click(screen.getByRole('button', { name: /Save changes/ }));
    await waitFor(() => expect(updateSchemaEditorTable).toHaveBeenCalledWith(
      'p1', 'dbo.orders', { blurb: 'Rewritten.' }, 'default',
    ));
  });

  it('sends the kept column set when a column is removed', async () => {
    updateSchemaEditorTable.mockResolvedValue(ordersTable);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /dbo\.orders/ }));
    await screen.findByDisplayValue('All customer orders.');
    // Each editable column row has a remove button; remove "amount" (2nd row).
    const removeButtons = screen.getAllByRole('button', { name: /Remove column/ });
    fireEvent.click(removeButtons[1]);
    fireEvent.click(screen.getByRole('button', { name: /Save changes/ }));
    await waitFor(() => {
      const call = updateSchemaEditorTable.mock.calls[0];
      expect(call[2].columns).toHaveLength(1);
      expect(call[2].columns[0].name).toBe('id');
    });
  });

  it('is read-only without member role (no Save, no remove)', async () => {
    mockCanEdit = false;
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /dbo\.orders/ }));
    await screen.findByDisplayValue('All customer orders.');
    expect(screen.queryByRole('button', { name: /Save changes/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Remove column/ })).not.toBeInTheDocument();
    expect(screen.getByText(/read-only access/i)).toBeInTheDocument();
  });

  it('warns when there are manual edits a rebuild would discard', async () => {
    listSchemaEdits.mockResolvedValue({ edits: [], since_last_index: 3 });
    mount();
    await waitFor(() => expect(screen.getByText(/3 manual edits since the last index/i)).toBeInTheDocument());
  });
});
