/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import Shell from '@/components/layout/AppShell';

// Shell + the SpotlightSearch it renders both read the route via next/navigation.
jest.mock('next/navigation', () => ({
  usePathname: () => '/',
  useParams: () => ({}),
  useRouter: () => ({ push: jest.fn() }),
}));

jest.mock('@/lib/api', () => ({
  api: {
    getProject: jest.fn().mockResolvedValue(null),
    listSearchHistory: jest.fn().mockResolvedValue([]),
    searchInsights: jest.fn().mockResolvedValue({ results: [], embedding_model: 'm' }),
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

// The sidebar logo cell and the top bar must share the same fixed height so
// their bottom borders land on one continuous horizontal line (issue #272).
describe('AppShell — logo cell / top bar border alignment', () => {
  function getCells() {
    // The logo cell is the flex container that wraps the logo image.
    const logoCell = screen.getByAltText('DecisionBox').parentElement as HTMLElement;
    // The top bar is the only <header> in the shell.
    const topBar = document.querySelector('header') as HTMLElement;
    return { logoCell, topBar };
  }

  it('pins the logo cell to the shared --db-topbar-height token, border included', () => {
    mount();
    const { logoCell } = getCells();

    expect(logoCell).toHaveStyle({ height: 'var(--db-topbar-height)' });
    // border-box keeps the 1px bottom border inside the 52px box so the cell's
    // total height equals the token (the original padding-driven cell was ~3px taller).
    expect(logoCell).toHaveStyle({ boxSizing: 'border-box' });
    expect(logoCell).toHaveStyle({ borderBottom: '1px solid var(--db-border-default)' });
  });

  it('drops the vertical padding but keeps the left inset, content vertically centered', () => {
    mount();
    const { logoCell } = getCells();

    expect(logoCell).toHaveStyle({ paddingTop: '0px', paddingBottom: '0px' });
    expect(logoCell).toHaveStyle({ paddingLeft: '20px', paddingRight: '20px' });
    expect(logoCell).toHaveStyle({ alignItems: 'center' });
  });

  it('matches the (unchanged) top bar height so both bottom borders align', () => {
    mount();
    const { logoCell, topBar } = getCells();

    // Top bar is untouched: still the token height with a bottom border.
    expect(topBar).toHaveStyle({ height: 'var(--db-topbar-height)' });
    expect(topBar).toHaveStyle({ borderBottom: '1px solid var(--db-border-default)' });

    // Same height token on both => the two bottom borders sit on one line.
    expect(logoCell.style.height).toBe(topBar.style.height);
  });
});
