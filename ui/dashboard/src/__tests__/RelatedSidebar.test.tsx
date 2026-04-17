/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import RelatedSidebar, { RelatedChipStrip, RelatedItem } from '@/components/lists/RelatedSidebar';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

const related: RelatedItem[] = [
  { id: 'r1', title: 'First rec', href: '/r/r1', badge: { label: 'P1', color: 'red' } },
  { id: 'r2', title: 'Second rec', href: '/r/r2', subtitle: '+12%' },
];

const similar: RelatedItem[] = [
  { id: 's1', title: 'Similar thing', href: '/s/s1', subtitle: 'churn' },
];

describe('RelatedSidebar', () => {
  it('renders both sections with headers and items', () => {
    wrap(
      <RelatedSidebar
        relatedLabel="Related Recommendations"
        related={related}
        similarLabel="Similar Insights"
        similar={similar}
      />
    );
    expect(screen.getByText('Related Recommendations')).toBeInTheDocument();
    expect(screen.getByText('Similar Insights')).toBeInTheDocument();
    expect(screen.getByText('First rec')).toBeInTheDocument();
    expect(screen.getByText('Similar thing')).toBeInTheDocument();
  });

  it('links each item to its href', () => {
    wrap(<RelatedSidebar relatedLabel="Related" related={related} />);
    const link = screen.getByText('First rec').closest('a');
    expect(link).toHaveAttribute('href', '/r/r1');
  });

  it('renders nothing when both arrays are empty', () => {
    wrap(<RelatedSidebar relatedLabel="Related" related={[]} similar={[]} />);
    // No label, no items — the component returned null.
    expect(screen.queryByText('Related')).not.toBeInTheDocument();
  });

  it('omits the similar section when no similarLabel is given', () => {
    wrap(<RelatedSidebar relatedLabel="Related" related={related} similar={similar} />);
    // similarLabel was not passed — the similar block is not rendered.
    expect(screen.queryByText('Similar thing')).not.toBeInTheDocument();
  });
});

describe('RelatedChipStrip', () => {
  it('flattens both lists into a single row of chips', () => {
    wrap(
      <RelatedChipStrip
        relatedLabel="Related"
        related={related}
        similar={similar}
      />
    );
    expect(screen.getByText('First rec')).toBeInTheDocument();
    expect(screen.getByText('Second rec')).toBeInTheDocument();
    expect(screen.getByText('Similar thing')).toBeInTheDocument();
  });

  it('renders nothing when both lists are empty', () => {
    wrap(<RelatedChipStrip relatedLabel="Related" related={[]} similar={[]} />);
    // No chip links — the strip returned null.
    expect(screen.queryAllByRole('link')).toHaveLength(0);
  });
});
