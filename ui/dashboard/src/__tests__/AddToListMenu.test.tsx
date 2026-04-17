/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import AddToListMenu from '@/components/lists/AddToListMenu';
import { api } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: {
    listBookmarkLists: jest.fn(),
    listsContaining: jest.fn(),
    addBookmark: jest.fn(),
    removeBookmark: jest.fn(),
    createBookmarkList: jest.fn(),
    getBookmarkList: jest.fn(),
  },
}));

const mockedApi = api as jest.Mocked<typeof api>;

function mountMenu({ onChange }: { onChange?: (ids: string[]) => void } = {}) {
  return render(
    <MantineProvider>
      <AddToListMenu
        projectId="p1"
        discoveryId="d1"
        targetType="insight"
        targetId="i1"
        onMembershipChange={onChange}
      >
        <button>trigger</button>
      </AddToListMenu>
    </MantineProvider>
  );
}

beforeEach(() => jest.clearAllMocks());

describe('AddToListMenu', () => {
  it('opens and fetches lists + membership on first open', async () => {
    mockedApi.listBookmarkLists.mockResolvedValue([
      { id: 'l1', name: 'Retention', project_id: 'p1', user_id: 'anonymous', item_count: 0, created_at: '', updated_at: '' },
      { id: 'l2', name: 'Monetization', project_id: 'p1', user_id: 'anonymous', item_count: 0, created_at: '', updated_at: '' },
    ]);
    mockedApi.listsContaining.mockResolvedValue(['l1']);

    mountMenu();
    fireEvent.click(screen.getByText('trigger'));

    await waitFor(() => {
      expect(screen.getByRole('checkbox', { name: 'Retention' })).toBeChecked();
      expect(screen.getByRole('checkbox', { name: 'Monetization' })).not.toBeChecked();
    });
    expect(mockedApi.listBookmarkLists).toHaveBeenCalledWith('p1');
    expect(mockedApi.listsContaining).toHaveBeenCalledWith('p1', 'insight', 'i1');
  });

  it('adding to a new list calls addBookmark and reports membership up', async () => {
    mockedApi.listBookmarkLists.mockResolvedValue([
      { id: 'l1', name: 'L1', project_id: 'p1', user_id: 'anonymous', item_count: 0, created_at: '', updated_at: '' },
    ]);
    mockedApi.listsContaining.mockResolvedValue([]);
    mockedApi.addBookmark.mockResolvedValue({
      id: 'b1', list_id: 'l1', project_id: 'p1', user_id: 'anonymous', discovery_id: 'd1',
      target_type: 'insight', target_id: 'i1', created_at: '',
    });

    const onChange = jest.fn();
    mountMenu({ onChange });
    fireEvent.click(screen.getByText('trigger'));

    const box = await screen.findByRole('checkbox', { name: 'L1' });
    fireEvent.click(box);

    await waitFor(() => expect(mockedApi.addBookmark).toHaveBeenCalledWith('p1', 'l1', {
      discovery_id: 'd1', target_type: 'insight', target_id: 'i1',
    }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(['l1']));
  });

  it('creating a new list via the inline form adds the item in one flow', async () => {
    mockedApi.listBookmarkLists.mockResolvedValue([]);
    mockedApi.listsContaining.mockResolvedValue([]);
    mockedApi.createBookmarkList.mockResolvedValue({
      id: 'new-list', name: 'My ideas', project_id: 'p1', user_id: 'anonymous',
      item_count: 0, created_at: '', updated_at: '',
    });
    mockedApi.addBookmark.mockResolvedValue({
      id: 'b1', list_id: 'new-list', project_id: 'p1', user_id: 'anonymous', discovery_id: 'd1',
      target_type: 'insight', target_id: 'i1', created_at: '',
    });

    mountMenu();
    fireEvent.click(screen.getByText('trigger'));

    // First render after open fetches (empty list case). Click "New list…".
    fireEvent.click(await screen.findByRole('button', { name: /New list/i }));

    const input = await screen.findByPlaceholderText('List name');
    fireEvent.change(input, { target: { value: 'My ideas' } });
    // Mantine Button's accessible name is computed from the nested label span,
    // which jsdom's role resolver sometimes misses — fall back to text match.
    fireEvent.click((await screen.findByText('Create')).closest('button')!);

    await waitFor(() => {
      expect(mockedApi.createBookmarkList).toHaveBeenCalledWith('p1', { name: 'My ideas' });
      expect(mockedApi.addBookmark).toHaveBeenCalledWith('p1', 'new-list', {
        discovery_id: 'd1', target_type: 'insight', target_id: 'i1',
      });
    });
  });

  it('does not fire Create with an empty name', async () => {
    mockedApi.listBookmarkLists.mockResolvedValue([]);
    mockedApi.listsContaining.mockResolvedValue([]);

    mountMenu();
    fireEvent.click(screen.getByText('trigger'));
    fireEvent.click(await screen.findByRole('button', { name: /New list/i }));
    const createBtn = (await screen.findByText('Create')).closest('button')!;
    expect(createBtn).toBeDisabled();
  });
});
