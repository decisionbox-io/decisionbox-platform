/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import BookmarkButton from '@/components/lists/BookmarkButton';
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

function mount() {
  return render(
    <MantineProvider>
      <BookmarkButton
        projectId="p1"
        discoveryId="d1"
        targetType="insight"
        targetId="i1"
      />
    </MantineProvider>
  );
}

beforeEach(() => jest.clearAllMocks());

describe('BookmarkButton', () => {
  it('shows an empty bookmark icon when not in any list', async () => {
    mockedApi.listsContaining.mockResolvedValue([]);
    mount();
    const btn = await screen.findByRole('button');
    expect(btn).toHaveAttribute('aria-label', 'Add to list');
  });

  it('shows filled icon + count in aria-label when bookmarked', async () => {
    mockedApi.listsContaining.mockResolvedValue(['l1', 'l2']);
    mount();
    await waitFor(() => {
      const btn = screen.getByRole('button');
      expect(btn.getAttribute('aria-label')).toMatch(/Bookmarked in 2 lists/);
    });
  });

  it('falls back to the empty state when listsContaining errors', async () => {
    mockedApi.listsContaining.mockRejectedValue(new Error('nope'));
    mount();
    await waitFor(() => {
      expect(screen.getByRole('button')).toHaveAttribute('aria-label', 'Add to list');
    });
  });
});
