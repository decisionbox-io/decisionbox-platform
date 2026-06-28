/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import Markdown from '@/components/common/Markdown';

describe('Markdown', () => {
  it('renders bold and italic emphasis', () => {
    const { container } = render(<Markdown>{'**67%** of *new* players'}</Markdown>);
    const strong = container.querySelector('strong');
    const em = container.querySelector('em');
    expect(strong).toHaveTextContent('67%');
    expect(em).toHaveTextContent('new');
    // No raw markers leak into the text.
    expect(container.textContent).not.toContain('**');
    expect(container.textContent).not.toContain('*new*');
  });

  it('renders bulleted lists as list items', () => {
    const { container } = render(<Markdown>{'- onboarding length\n- difficulty spike'}</Markdown>);
    const items = container.querySelectorAll('li');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent('onboarding length');
  });

  it('renders sub-headings at a small scale, never as a page-size h1', () => {
    const { container } = render(<Markdown>{'### Why it matters'}</Markdown>);
    expect(container.querySelector('h1')).toBeNull();
    expect(container.querySelector('h2')).toBeNull();
    // ### maps to an h4-scale element.
    const heading = container.querySelector('h4');
    expect(heading).toHaveTextContent('Why it matters');
  });

  it('caps an oversized top-level heading to a small heading', () => {
    const { container } = render(<Markdown>{'# Big heading'}</Markdown>);
    // # is remapped to h3 (small), not a literal page-size h1.
    expect(container.querySelector('h1')).toBeNull();
    expect(container.querySelector('h3')).toHaveTextContent('Big heading');
  });

  it('renders a GFM table', () => {
    const md = '| Segment | Rate |\n|---------|------|\n| iOS | 28% |\n| Android | 19% |';
    const { container } = render(<Markdown>{md}</Markdown>);
    expect(container.querySelector('table')).not.toBeNull();
    expect(container.querySelectorAll('td').length).toBeGreaterThanOrEqual(4);
    expect(screen.getByText('Segment')).toBeInTheDocument();
  });

  it('renders a plain paragraph with no leftover symbols', () => {
    const { container } = render(<Markdown>{'Players churn at level 45 at 67 percent.'}</Markdown>);
    // Assert on the rendered paragraph, not the whole container (which also
    // holds the component's scoped <style> block).
    const p = container.querySelector('p');
    expect(p).toHaveTextContent('Players churn at level 45 at 67 percent.');
    expect(p?.textContent).not.toContain('#');
    expect(p?.textContent).not.toContain('*');
  });

  it('returns nothing for empty/whitespace content', () => {
    const { container } = render(<Markdown>{'   '}</Markdown>);
    expect(container.firstChild).toBeNull();
  });

  it('does not render raw HTML (XSS-safe)', () => {
    const { container } = render(
      <Markdown>{'<script>alert(1)</script> and <img src=x onerror=alert(2)>'}</Markdown>,
    );
    // No live script/img elements created from the string.
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('img')).toBeNull();
  });

  it('does not render Markdown images as live <img> (renders alt text)', () => {
    const { container } = render(
      <Markdown>{'![a chart](https://evil.example.com/x.png) and text'}</Markdown>,
    );
    // No external image element is created; the alt text survives.
    expect(container.querySelector('img')).toBeNull();
    expect(container.textContent).toContain('a chart');
    expect(container.textContent).not.toContain('evil.example.com');
  });

  it('renders links as plain, non-navigable text', () => {
    const { container } = render(
      <Markdown>{'see [the dashboard](javascript:alert(1)) now'}</Markdown>,
    );
    // No anchor element — the label survives as text.
    expect(container.querySelector('a')).toBeNull();
    expect(container.textContent).toContain('the dashboard');
    expect(container.textContent).not.toContain('javascript:');
  });
});
