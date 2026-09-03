/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import QuestionTargetLink from '@/components/common/QuestionTargetLink';

const getDiscoveryById = jest.fn();
jest.mock('@/lib/api', () => ({ api: { getDiscoveryById: (...a: unknown[]) => getDiscoveryById(...a) } }));

beforeEach(() => {
  jest.clearAllMocks();
  getDiscoveryById.mockResolvedValue({
    insights: [{ id: 'i1', name: 'Churn spike in EU', severity: 'high', description: 'Signups down 40%.' }],
    recommendations: [],
  });
});

describe('QuestionTargetLink', () => {
  it('links to the insight detail page and previews it in a hover tooltip', async () => {
    render(<QuestionTargetLink projectId="p1" discoveryId="d1" target={{ type: 'insight', id: 'i1' }} />);
    const link = screen.getByText('view insight').closest('a');
    expect(link).toHaveAttribute('href', '/projects/p1/discoveries/d1/insights/i1');
    // The tooltip content resolves from the discovery's findings.
    await waitFor(() => expect(screen.getByText('Churn spike in EU')).toBeInTheDocument());
    expect(screen.getByText('high')).toBeInTheDocument();
    expect(screen.getByText(/Signups down 40%/)).toBeInTheDocument();
  });

  it('links a recommendation target to its detail route', () => {
    render(<QuestionTargetLink projectId="p1" discoveryId="d9" target={{ type: 'recommendation', id: 'r2' }} />);
    expect(screen.getByText('view recommendation').closest('a'))
      .toHaveAttribute('href', '/projects/p1/discoveries/d9/recommendations/r2');
  });

  it('renders nothing for targets without a detail page (table / area)', () => {
    const { container } = render(
      <QuestionTargetLink projectId="p1" discoveryId="d1" target={{ type: 'table', id: 'public.orders' }} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(getDiscoveryById).not.toHaveBeenCalled();
  });
});
