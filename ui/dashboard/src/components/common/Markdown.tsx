'use client';

import React from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

/**
 * Markdown renders the small GitHub-Flavored Markdown subset used by insight
 * and recommendation descriptions: emphasis, lists, small sub-headings, simple
 * tables, inline code, and blockquotes. It is deliberately constrained:
 *
 *  - No raw HTML. react-markdown does not render raw HTML unless `rehype-raw`
 *    is added (it is not), so injected `<script>`/`<img onerror>` strings are
 *    shown inert as text — safe by default.
 *  - Links are rendered as plain, non-navigable text. Descriptions are
 *    product-generated from the user's own warehouse; rendering links as text
 *    removes any "surprising or unsafe link" surface entirely.
 *  - Headings are capped at a small sub-heading scale, never page-size, so a
 *    `#`/`##` in the content cannot dominate the layout.
 *
 * Plain text (no Markdown) renders as a tidy paragraph, so unformatted and
 * legacy descriptions look clean with no leftover symbols.
 *
 * Styling comes from the shared design tokens in `styles/tokens.css` so the
 * rendered content matches the surrounding product typography.
 */
export default function Markdown({ children, className }: { children?: string | null; className?: string }) {
  const content = (children ?? '').trim();
  if (!content) return null;

  return (
    <div className={`db-markdown${className ? ` ${className}` : ''}`}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          p: ({ children }) => <p style={{ margin: '0 0 12px', lineHeight: 1.7 }}>{children}</p>,
          // Sub-headings separate ideas but never dominate: h1–h3 render at a
          // small heading scale, h4–h6 a touch smaller. No page-size headings.
          h1: ({ children }) => <h3 style={headingStyle(15)}>{children}</h3>,
          h2: ({ children }) => <h3 style={headingStyle(15)}>{children}</h3>,
          h3: ({ children }) => <h4 style={headingStyle(14)}>{children}</h4>,
          h4: ({ children }) => <h5 style={headingStyle(13)}>{children}</h5>,
          h5: ({ children }) => <h5 style={headingStyle(13)}>{children}</h5>,
          h6: ({ children }) => <h6 style={headingStyle(13)}>{children}</h6>,
          strong: ({ children }) => <strong style={{ fontWeight: 600, color: 'var(--db-text-primary)' }}>{children}</strong>,
          em: ({ children }) => <em>{children}</em>,
          ul: ({ children }) => <ul style={{ margin: '8px 0', paddingLeft: 20 }}>{children}</ul>,
          ol: ({ children }) => <ol style={{ margin: '8px 0', paddingLeft: 20 }}>{children}</ol>,
          li: ({ children }) => <li style={{ marginBottom: 4, lineHeight: 1.6 }}>{children}</li>,
          blockquote: ({ children }) => (
            <blockquote style={{
              margin: '8px 0', paddingLeft: 12, borderLeft: '3px solid var(--db-border-strong)',
              color: 'var(--db-text-secondary)',
            }}>{children}</blockquote>
          ),
          // Links render as plain, non-navigable text (Trust): keep the label,
          // drop the href.
          a: ({ children }) => <span>{children}</span>,
          // Images are out of the supported set. Render the alt text instead of
          // loading external content, so a stray `![alt](url)` can't pull a
          // remote image into the view.
          img: ({ alt }) => (alt ? <span>{alt}</span> : null),
          table: ({ children }) => (
            <div style={{ overflowX: 'auto', margin: '8px 0' }}>
              <table style={{ borderCollapse: 'collapse', fontSize: 13, width: '100%' }}>{children}</table>
            </div>
          ),
          th: ({ children }) => (
            <th style={{ borderBottom: '2px solid var(--db-border-default)', padding: '6px 10px', textAlign: 'left', fontWeight: 600, fontSize: 12 }}>{children}</th>
          ),
          td: ({ children }) => (
            <td style={{ borderBottom: '1px solid var(--db-border-default)', padding: '6px 10px', fontSize: 13 }}>{children}</td>
          ),
          code: ({ children, className: codeClass }) => {
            const isBlock = codeClass?.includes('language-');
            return isBlock ? (
              <pre style={{ background: 'var(--db-bg-muted)', borderRadius: 6, padding: 12, overflow: 'auto', fontSize: 12, margin: '8px 0' }}>
                <code>{children}</code>
              </pre>
            ) : (
              <code style={{ background: 'var(--db-bg-muted)', padding: '1px 5px', borderRadius: 4, fontSize: '0.9em' }}>{children}</code>
            );
          },
        }}
      >
        {content}
      </ReactMarkdown>
      <style>{`
        .db-markdown { font-size: 14px; color: var(--db-text-primary); }
        .db-markdown > *:first-child { margin-top: 0; }
        .db-markdown > *:last-child { margin-bottom: 0; }
      `}</style>
    </div>
  );
}

function headingStyle(fontSize: number): React.CSSProperties {
  return { fontSize, fontWeight: 600, margin: '14px 0 6px', color: 'var(--db-text-primary)' };
}
