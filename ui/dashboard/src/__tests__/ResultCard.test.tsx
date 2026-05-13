/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ResultCard } from '@/components/search/ResultCard';
import type { SearchResultItem } from '@/lib/api';

// next/link renders a real anchor in jsdom by default, but the test
// environment can flake on prefetch — stub it to a plain <a> so we only
// assert what we care about (href + visible name).
jest.mock('next/link', () => {
  const Link = ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  );
  Link.displayName = 'Link';
  return { __esModule: true, default: Link };
});

const baseInsight: SearchResultItem = {
  id: 'ins-1',
  type: 'insight',
  score: 0.91,
  name: 'High churn at L45',
  description: 'desc',
  discovery_id: 'run-1',
  discovered_at: '2026-05-13T00:00:00Z',
  project_id: 'proj-1',
};

const baseRec: SearchResultItem = {
  ...baseInsight,
  id: 'rec-7',
  type: 'recommendation',
  name: 'Reduce friction',
  discovery_id: 'run-2',
};

function mount(item: SearchResultItem, projectId = 'route-proj') {
  return render(
    <MantineProvider>
      <ResultCard item={item} projectId={projectId} />
    </MantineProvider>,
  );
}

describe('ResultCard', () => {
  it('links insights to the insight detail page', () => {
    mount(baseInsight);
    const link = screen.getByRole('link', { name: /High churn at L45/ });
    expect(link).toHaveAttribute('href', '/projects/proj-1/discoveries/run-1/insights/ins-1');
  });

  it('links recommendations to the recommendation detail page (plural segment)', () => {
    mount(baseRec);
    const link = screen.getByRole('link', { name: /Reduce friction/ });
    expect(link).toHaveAttribute(
      'href',
      '/projects/proj-1/discoveries/run-2/recommendations/rec-7',
    );
  });

  it('uses the route project id when item.project_id is omitted (per-project search)', () => {
    mount({ ...baseInsight, project_id: undefined }, 'route-proj');
    const link = screen.getByRole('link', { name: /High churn at L45/ });
    expect(link).toHaveAttribute(
      'href',
      '/projects/route-proj/discoveries/run-1/insights/ins-1',
    );
  });

  it('uses the item project id when present (cross-project search)', () => {
    mount({ ...baseInsight, project_id: 'other-proj' }, 'route-proj');
    const link = screen.getByRole('link');
    expect(link).toHaveAttribute(
      'href',
      '/projects/other-proj/discoveries/run-1/insights/ins-1',
    );
  });

  it('renders the project-name badge when present', () => {
    // The conditional rendering of severity/area badges is owned by
    // SeverityBadge / AreaBadge — covered by their own tests. Here we
    // only need to confirm the conditional branches inside ResultCard
    // are reached; we look for the project_name span specifically since
    // it is rendered inline by the card.
    mount({
      ...baseInsight,
      severity: 'high',
      analysis_area: 'retention',
      project_name: 'Acme',
    });
    expect(screen.getByText('Acme')).toBeInTheDocument();
  });

  it('renders the score match prefix', () => {
    mount({ ...baseInsight, score: 0.876 });
    expect(screen.getByText('88% match')).toBeInTheDocument();
  });

  it('falls back to title when type is recommendation and name is empty', () => {
    // The recommendation API response uses `title` as the canonical
    // field; legacy clients may also see `name` empty. The card prefers
    // `name` if set, then `title`.
    mount({ ...baseRec, name: '', title: 'From title field' });
    expect(screen.getByRole('link', { name: /From title field/ })).toBeInTheDocument();
  });

  it('truncates descriptions longer than 200 chars with an ellipsis', () => {
    const longDesc = 'd'.repeat(250);
    const { container } = mount({ ...baseInsight, description: longDesc });
    // textContent includes the truncated description followed by "...".
    expect(container.textContent).toMatch(/d{200}\.{3}/);
    expect(container.textContent).not.toMatch(/d{201}/);
  });

  it('handles an empty discovered_at without rendering an Invalid Date', () => {
    const { container } = mount({ ...baseInsight, discovered_at: '' });
    expect(container.textContent).not.toMatch(/Invalid Date/);
  });

  it('renders an empty link label when recommendation has neither name nor title', () => {
    // Defensive branch — the API populates one or the other, but the
    // card's fallback chain should not crash when both are absent.
    mount({ ...baseRec, name: '', title: undefined });
    const link = screen.getByRole('link');
    expect(link.textContent).toBe('');
  });
});
