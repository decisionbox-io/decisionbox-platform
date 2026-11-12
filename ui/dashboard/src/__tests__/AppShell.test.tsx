/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import Shell from '@/components/layout/AppShell';

jest.mock('next/navigation', () => ({
  usePathname: () => '/',
  useParams: () => ({}),
  useRouter: () => ({ push: jest.fn() }),
}));

jest.mock('@/lib/api', () => ({
  api: {
    getProject: jest.fn(),
    listSearchHistory: jest.fn().mockResolvedValue([]),
    searchInsights: jest.fn().mockResolvedValue({ results: [] }),
  },
}));

function mount() {
  return render(
    <MantineProvider>
      <Shell>
        <div>content</div>
      </Shell>
    </MantineProvider>,
  );
}

describe('AppShell sidebar logo', () => {
  it('renders the single combined SVG logo with the DecisionBox alt text', () => {
    mount();
    const logo = screen.getByAltText('DecisionBox');
    expect(logo.tagName).toBe('IMG');
    expect(logo).toHaveAttribute('src', '/logo.svg');
  });

  it('renders exactly one logo image (no separate icon image)', () => {
    mount();
    expect(screen.getAllByAltText('DecisionBox')).toHaveLength(1);
  });

  it('no longer renders the separate "DecisionBox" wordmark text', () => {
    mount();
    // The old layout had a <span>DecisionBox</span> next to the icon; the
    // wordmark is now baked into the image, so only the img alt carries the
    // name (getByText does not match alt attributes).
    expect(screen.queryByText('DecisionBox')).toBeNull();
  });

  it('sizes by height with auto width so the aspect ratio is preserved', () => {
    mount();
    const logo = screen.getByAltText('DecisionBox');
    expect(logo).toHaveStyle({ height: '24px' });
    expect(logo).toHaveStyle({ width: 'auto' });
    expect(logo).toHaveStyle({ maxWidth: '100%' });
  });
});
