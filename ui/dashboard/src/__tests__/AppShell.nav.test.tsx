/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import Shell from '@/components/layout/AppShell';

// Mutable route mocks so a single test file can render the root nav and a
// project nav independently.
let mockPath = '/';
let mockParams: { id?: string } = {};
jest.mock('next/navigation', () => ({
  usePathname: () => mockPath,
  useParams: () => mockParams,
}));
jest.mock('@/lib/api', () => ({
  api: { getProject: jest.fn().mockResolvedValue({ id: 'p1', name: 'Acme', domain: 'gaming' }) },
}));
// Probe for the topbar search so we can assert it's absent on the root page.
jest.mock('@/components/common/SpotlightSearch', () => ({
  __esModule: true,
  default: () => <div data-testid="spotlight" />,
}));
jest.mock('next/image', () => ({
  __esModule: true,
  // eslint-disable-next-line @next/next/no-img-element -- test stub for next/image
  default: (props: { alt?: string }) => <img alt={props.alt ?? ''} />,
}));

function hrefOf(label: string): string | null {
  const el = screen.getByText(label).closest('a');
  return el ? el.getAttribute('href') : null;
}

describe('AppShell nav (data-driven, #337)', () => {
  describe('root nav (no project)', () => {
    beforeEach(() => {
      mockPath = '/';
      mockParams = {};
      render(<Shell><div>content</div></Shell>);
    });

    it('groups the root nav into Workspace + Administration categories', () => {
      expect(screen.getByText('Workspace')).toBeInTheDocument();
      expect(screen.getByText('Administration')).toBeInTheDocument();
      expect(hrefOf('Projects')).toBe('/');
      expect(hrefOf('Playbooks')).toBe('/domain-packs');
      expect(hrefOf('System')).toBe('/system');
    });

    it('hides the spotlight search on the root page', () => {
      expect(screen.queryByTestId('spotlight')).not.toBeInTheDocument();
    });
  });

  describe('project nav', () => {
    beforeEach(async () => {
      mockPath = '/projects/p1';
      mockParams = { id: 'p1' };
      render(<Shell><div>content</div></Shell>);
      // Flush the async getProject → setProject so the render settles before we
      // assert (avoids act warnings for the project-selector update).
      await screen.findByText('Acme');
    });

    it('renders the Discover / Intelligence / Configure categories with project-scoped hrefs', () => {
      expect(screen.getByText('Discover')).toBeInTheDocument();
      expect(screen.getByText('Intelligence')).toBeInTheDocument();
      expect(screen.getByText('Configure')).toBeInTheDocument();
      expect(hrefOf('Discovery runs')).toBe('/projects/p1');
      expect(hrefOf('Insights')).toBe('/projects/p1/insights');
      expect(hrefOf('Ask Insights')).toBe('/projects/p1/ask');
      expect(hrefOf('Playbook')).toBe('/projects/p1/prompts');
    });

    it('shows the spotlight search on a project page', () => {
      expect(screen.getByTestId('spotlight')).toBeInTheDocument();
    });
  });
});
