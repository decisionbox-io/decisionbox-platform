/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render } from '@testing-library/react';
import { CitationLink } from '@/components/citations/CitationLink';

// The component is shared by the Ask page (markdown answers) and the
// enterprise executive-summary newspaper. These tests pin the
// behaviour both surfaces rely on so a future change to one doesn't
// silently regress the other:
//
//   - Linked vs non-linked rendering keyed on `href`.
//   - Tooltip content composition (name / severity / description).
//   - Truncation thresholds on long name + description.
//   - Fallback label when `name` is missing.

describe('CitationLink', () => {
  it('renders a Link badge when href is set', () => {
    const { container } = render(
      <CitationLink number={1} href="/projects/p-1/insights/i-1" name="High churn at L45" />,
    );
    const anchor = container.querySelector('a');
    expect(anchor).not.toBeNull();
    expect(anchor?.getAttribute('href')).toBe('/projects/p-1/insights/i-1');
    expect(anchor?.textContent).toBe('1');
  });

  it('renders a focusable static span when href is omitted (unresolved citation)', () => {
    // The Ask page passes href=undefined when sources[idx] is missing.
    // Rendering an <a href="#"> would scroll the page to top on click;
    // a non-interactive <span> keeps the visual without that side effect.
    // The span carries tabIndex=0 so keyboard users can reach it and the
    // CSS :focus-within selector can still reveal the tooltip.
    const { container } = render(<CitationLink number={3} name="Stale Insight" />);
    expect(container.querySelector('a')).toBeNull();
    // The static badge is the inner span carrying the number text.
    const badge = Array.from(container.querySelectorAll('span')).find(
      (el) => el.textContent === '3',
    );
    expect(badge).toBeDefined();
    expect(badge?.getAttribute('tabindex')).toBe('0');
    expect(badge?.getAttribute('role')).toBe('note');
  });

  it('puts the source name in the tooltip', () => {
    const { container } = render(
      <CitationLink number={1} href="/x" name="Rhode Island OOP gap" />,
    );
    expect(container.textContent).toContain('Rhode Island OOP gap');
  });

  it('falls back to "Source N" when name is omitted', () => {
    const { container } = render(<CitationLink number={7} href="/x" />);
    expect(container.textContent).toContain('Source 7');
  });

  it('truncates long names at 80 characters with an ellipsis', () => {
    const longName = 'a'.repeat(120);
    const { container } = render(<CitationLink number={1} href="/x" name={longName} />);
    const strong = container.querySelector('strong');
    expect(strong?.textContent?.length).toBe(83); // 80 + "..."
    expect(strong?.textContent?.endsWith('...')).toBe(true);
  });

  it('truncates long descriptions at 120 characters with an ellipsis', () => {
    const longDesc = 'd'.repeat(200);
    const { container } = render(
      <CitationLink number={1} href="/x" name="short" description={longDesc} />,
    );
    const text = container.textContent ?? '';
    // The tooltip is part of the DOM (display:none initially); textContent
    // includes it. The truncated description ends with "..." after 120 chars.
    expect(text).toMatch(/d{120}\.{3}/);
    expect(text).not.toMatch(/d{121}/);
  });

  it('omits the severity span when severity is not provided', () => {
    const { container } = render(<CitationLink number={1} href="/x" name="x" />);
    // No severity element; the only span without a className is the
    // outer wrapper or the description spot, neither of which we set.
    const text = container.textContent ?? '';
    expect(text).not.toMatch(/\b(high|medium|low|critical)\b/i);
  });

  it('renders the severity inline when provided', () => {
    const { container } = render(
      <CitationLink number={1} href="/x" name="x" severity="high" />,
    );
    expect(container.textContent).toContain('high');
  });

  it('omits the description block when not provided', () => {
    const { container } = render(<CitationLink number={1} href="/x" name="Short" />);
    // The tooltip contains exactly the bold name; nothing on a new
    // line after it.
    expect(container.querySelector('strong')?.textContent).toBe('Short');
  });
});
