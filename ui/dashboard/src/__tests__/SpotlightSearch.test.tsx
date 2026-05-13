/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import SpotlightSearch from '@/components/common/SpotlightSearch';
import { api } from '@/lib/api';

const push = jest.fn();

jest.mock('next/navigation', () => ({
  useRouter: () => ({ push }),
  useParams: () => ({ id: 'route-proj' }),
}));

jest.mock('@/lib/api', () => ({
  api: {
    searchInsights: jest.fn(),
    listSearchHistory: jest.fn(),
  },
}));

const mockedApi = api as jest.Mocked<typeof api>;

function mount() {
  return render(
    <MantineProvider>
      <SpotlightSearch />
    </MantineProvider>,
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  mockedApi.listSearchHistory.mockResolvedValue([]);
});

describe('SpotlightSearch click navigation', () => {
  it('pushes the insight detail URL when an insight result is clicked', async () => {
    mockedApi.searchInsights.mockResolvedValue({
      results: [
        {
          id: 'ins-1',
          type: 'insight',
          score: 0.9,
          name: 'High churn',
          description: 'detail',
          discovery_id: 'run-1',
          discovered_at: '2026-05-13T00:00:00Z',
          project_id: 'proj-1',
        },
      ],
      embedding_model: 'm',
    });

    mount();

    const user = userEvent.setup();
    const input = screen.getByRole('combobox');
    await user.type(input, 'churn');

    // The component debounces the search by 300ms before invoking the
    // API; wait for the row to render before clicking.
    const row = await screen.findByText('High churn');
    await user.click(row);

    await waitFor(() => {
      expect(push).toHaveBeenCalledWith(
        '/projects/proj-1/discoveries/run-1/insights/ins-1',
      );
    });
  });

  it('pushes the recommendation detail URL when a recommendation result is clicked', async () => {
    mockedApi.searchInsights.mockResolvedValue({
      results: [
        {
          id: 'rec-7',
          type: 'recommendation',
          score: 0.8,
          name: 'Reduce friction',
          description: 'detail',
          discovery_id: 'run-2',
          discovered_at: '2026-05-13T00:00:00Z',
          project_id: 'proj-1',
        },
      ],
      embedding_model: 'm',
    });

    mount();

    const user = userEvent.setup();
    const input = screen.getByRole('combobox');
    await user.type(input, 'friction');

    const row = await screen.findByText('Reduce friction');
    await user.click(row);

    await waitFor(() => {
      expect(push).toHaveBeenCalledWith(
        '/projects/proj-1/discoveries/run-2/recommendations/rec-7',
      );
    });
  });

  it('falls back to the route project when the result omits project_id', async () => {
    // Same-project search endpoint may return items without
    // project_id (the field is optional on SearchResultItem); the
    // route's projectId fills in via the helper.
    mockedApi.searchInsights.mockResolvedValue({
      results: [
        {
          id: 'ins-2',
          type: 'insight',
          score: 0.9,
          name: 'Drop in retention',
          description: 'detail',
          discovery_id: 'run-3',
          discovered_at: '2026-05-13T00:00:00Z',
        },
      ],
      embedding_model: 'm',
    });

    mount();

    const user = userEvent.setup();
    const input = screen.getByRole('combobox');
    await user.type(input, 'retention');

    const row = await screen.findByText('Drop in retention');
    await user.click(row);

    await waitFor(() => {
      expect(push).toHaveBeenCalledWith(
        '/projects/route-proj/discoveries/run-3/insights/ins-2',
      );
    });
  });
});
