/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import CitationsFooter, {
  numberSources,
  sourceHref,
} from '@/components/citations/CitationsFooter';
import type { SearchResultItem } from '@/lib/api';

function makeSource(overrides: Partial<SearchResultItem>): SearchResultItem {
  return {
    id: 'ins-1',
    type: 'insight',
    score: 0.91,
    name: 'High churn at Level 45',
    description: 'Players churning after tutorial',
    severity: 'high',
    analysis_area: 'churn',
    discovery_id: 'disc-1',
    discovered_at: '2026-05-01T00:00:00Z',
    ...overrides,
  };
}

function mount(props: Parameters<typeof CitationsFooter>[0]) {
  return render(
    <MantineProvider>
      <CitationsFooter {...props} />
    </MantineProvider>,
  );
}

describe('sourceHref', () => {
  test('builds the insight detail URL when discovery_id is present', () => {
    const src = makeSource({ id: 'i-1', type: 'insight', discovery_id: 'd-7' });
    expect(sourceHref('p-1', src)).toBe('/projects/p-1/discoveries/d-7/insights/i-1');
  });

  test('builds the recommendation detail URL when discovery_id is present', () => {
    const src = makeSource({ id: 'r-2', type: 'recommendation', discovery_id: 'd-7' });
    expect(sourceHref('p-1', src)).toBe('/projects/p-1/discoveries/d-7/recommendations/r-2');
  });

  test('falls back to the project-level list when discovery_id is empty', () => {
    const src = makeSource({ id: 'i-3', type: 'insight', discovery_id: '' });
    expect(sourceHref('p-1', src)).toBe('/projects/p-1/insights');
  });

  test('uses the recommendations list as fallback for orphan recommendation citations', () => {
    const src = makeSource({ id: 'r-4', type: 'recommendation', discovery_id: '' });
    expect(sourceHref('p-1', src)).toBe('/projects/p-1/recommendations');
  });
});

describe('numberSources', () => {
  test('returns an empty array for an empty input', () => {
    expect(numberSources([])).toEqual([]);
  });

  test('assigns 1..N in first-seen order to unique (type, id) pairs', () => {
    const out = numberSources([
      makeSource({ id: 'a', type: 'insight' }),
      makeSource({ id: 'b', type: 'recommendation' }),
      makeSource({ id: 'c', type: 'insight' }),
    ]);
    expect(out.map(o => o.number)).toEqual([1, 2, 3]);
    expect(out.map(o => o.src.id)).toEqual(['a', 'b', 'c']);
  });

  test('dedupes a (type, id) repeated in the source list', () => {
    const out = numberSources([
      makeSource({ id: 'a', type: 'insight' }),
      makeSource({ id: 'a', type: 'insight' }),
      makeSource({ id: 'b', type: 'insight' }),
    ]);
    expect(out.map(o => o.number)).toEqual([1, 2]);
    expect(out.map(o => o.src.id)).toEqual(['a', 'b']);
  });

  test('keeps the same id as insight and recommendation separately numbered', () => {
    // An insight and a recommendation that happen to share an _id (rare but
    // not impossible across the two collections) get distinct citation numbers.
    const out = numberSources([
      makeSource({ id: 'same', type: 'insight' }),
      makeSource({ id: 'same', type: 'recommendation' }),
    ]);
    expect(out.map(o => o.number)).toEqual([1, 2]);
    expect(out.map(o => o.src.type)).toEqual(['insight', 'recommendation']);
  });
});

describe('CitationsFooter rendering', () => {
  test('renders nothing when sources is empty', () => {
    mount({ projectId: 'p-1', sources: [] });
    // MantineProvider renders wrapper nodes around children, so we can't
    // check container.firstChild. The footer's own testid is the
    // identifying marker.
    expect(screen.queryByTestId('citations-footer')).not.toBeInTheDocument();
  });

  test('renders the default "Sources" heading', () => {
    mount({ projectId: 'p-1', sources: [makeSource({})] });
    expect(screen.getByText('Sources')).toBeInTheDocument();
  });

  test('renders a custom heading when provided', () => {
    mount({ projectId: 'p-1', sources: [makeSource({})], heading: 'Backing evidence' });
    expect(screen.getByText('Backing evidence')).toBeInTheDocument();
    expect(screen.queryByText('Sources')).not.toBeInTheDocument();
  });

  test('renders one row per unique source with sequential numbering', () => {
    mount({
      projectId: 'p-1',
      sources: [
        makeSource({ id: 'a', name: 'First insight' }),
        makeSource({ id: 'b', name: 'Second insight' }),
      ],
    });
    expect(screen.getByText(/\[1\] First insight/)).toBeInTheDocument();
    expect(screen.getByText(/\[2\] Second insight/)).toBeInTheDocument();
  });

  test('dedupes a repeated source so it shows only once', () => {
    mount({
      projectId: 'p-1',
      sources: [
        makeSource({ id: 'a', name: 'Repeated' }),
        makeSource({ id: 'a', name: 'Repeated' }),
      ],
    });
    expect(screen.getAllByTestId('citations-footer-item')).toHaveLength(1);
  });

  test('links each row to the correct project / discovery / target URL', () => {
    mount({
      projectId: 'p-9',
      sources: [makeSource({ id: 'i-1', type: 'insight', discovery_id: 'd-3' })],
    });
    const link = screen.getByTestId('citations-footer-item') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/projects/p-9/discoveries/d-3/insights/i-1');
  });

  test('shows the score percentage when showScore is true (the default)', () => {
    mount({
      projectId: 'p-1',
      sources: [makeSource({ score: 0.873 })],
    });
    expect(screen.getByText('87%')).toBeInTheDocument();
  });

  test('hides the score when showScore is false', () => {
    mount({
      projectId: 'p-1',
      sources: [makeSource({ score: 0.873 })],
      showScore: false,
    });
    expect(screen.queryByText('87%')).not.toBeInTheDocument();
  });

  test('falls back to title when name is empty', () => {
    mount({
      projectId: 'p-1',
      sources: [makeSource({ name: '', title: 'Title-based' })],
    });
    expect(screen.getByText(/\[1\] Title-based/)).toBeInTheDocument();
  });

  test('renders a severity badge only when severity is set', () => {
    // Use a source name that does NOT contain the severity word so the
    // badge query doesn't collide with the link text.
    const { rerender } = mount({
      projectId: 'p-1',
      sources: [makeSource({ name: 'Unrelated finding', severity: 'critical' })],
    });
    expect(screen.getByText('critical')).toBeInTheDocument();

    rerender(
      <MantineProvider>
        <CitationsFooter
          projectId="p-1"
          sources={[makeSource({ name: 'Unrelated finding', severity: undefined })]}
        />
      </MantineProvider>,
    );
    expect(screen.queryByText('critical')).not.toBeInTheDocument();
  });

  test('renders both insight and recommendation rows', () => {
    mount({
      projectId: 'p-1',
      sources: [
        makeSource({ id: 'i-1', type: 'insight', name: 'I' }),
        makeSource({ id: 'r-1', type: 'recommendation', name: 'R' }),
      ],
    });
    expect(screen.getByText(/\[1\] I/)).toBeInTheDocument();
    expect(screen.getByText(/\[2\] R/)).toBeInTheDocument();
  });

  test('respects the input ordering rather than re-sorting by score', () => {
    // The Ask handler returns sources in score-descending order; the
    // newspaper renderer will hand citations in *reading* order. The
    // footer must respect whichever order the caller chose rather than
    // imposing its own.
    mount({
      projectId: 'p-1',
      sources: [
        makeSource({ id: 'a', name: 'Low score', score: 0.2 }),
        makeSource({ id: 'b', name: 'High score', score: 0.9 }),
      ],
    });
    const items = screen.getAllByTestId('citations-footer-item');
    expect(items[0]).toHaveTextContent('[1] Low score');
    expect(items[1]).toHaveTextContent('[2] High score');
  });
});
