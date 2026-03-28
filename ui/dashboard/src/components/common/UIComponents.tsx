'use client';

import React from 'react';

/* ========== Stat Card ========== */

export function StatCard({ label, value, subtitle, valueColor }: {
  label: string; value: number | string; subtitle?: string; valueColor?: string;
}) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: 16,
    }}>
      <div style={{
        fontSize: 11, fontWeight: 500, textTransform: 'uppercase',
        letterSpacing: '0.5px', color: 'var(--db-text-tertiary)', marginBottom: 4,
      }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums',
        color: valueColor || 'var(--db-text-primary)', lineHeight: 1.3,
      }}>{typeof value === 'number' ? value.toLocaleString() : value}</div>
      {subtitle && (
        <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginTop: 2 }}>{subtitle}</div>
      )}
    </div>
  );
}

/* ========== Section Header ========== */

export function SectionHeader({ title, count, right }: { title: string; count?: number; right?: React.ReactNode }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      marginBottom: 12, marginTop: 8,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <span style={{ fontSize: 15, fontWeight: 500, color: 'var(--db-text-primary)' }}>{title}</span>
        {count !== undefined && (
          <span style={{ fontSize: 13, color: 'var(--db-text-tertiary)' }}>{count}</span>
        )}
      </div>
      {right}
    </div>
  );
}

/* ========== Table Header Cell ========== */

export function Th({ children, width, align }: { children: React.ReactNode; width?: string; align?: string }) {
  return (
    <th style={{
      fontSize: 11, fontWeight: 500, color: 'var(--db-text-tertiary)',
      textTransform: 'uppercase', letterSpacing: '0.5px',
      padding: '8px 12px', borderBottom: '1px solid var(--db-border-default)',
      textAlign: (align as 'left' | 'right') || 'left', width,
    }}>{children}</th>
  );
}

/* ========== Severity Badge ========== */

const severityColors: Record<string, { bg: string; color: string }> = {
  critical: { bg: 'var(--db-severity-critical-bg)', color: 'var(--db-severity-critical-text)' },
  high: { bg: 'var(--db-severity-high-bg)', color: 'var(--db-severity-high-text)' },
  medium: { bg: 'var(--db-severity-medium-bg)', color: 'var(--db-severity-medium-text)' },
  low: { bg: 'var(--db-severity-low-bg)', color: 'var(--db-severity-low-text)' },
};

const statusColors: Record<string, { bg: string; color: string }> = {
  Complete: { bg: 'var(--db-green-bg)', color: 'var(--db-green-text)' },
  Partial: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
  Failed: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
  confirmed: { bg: 'var(--db-green-bg)', color: 'var(--db-green-text)' },
  adjusted: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
  rejected: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
  error: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
};

export function SeverityBadge({ severity, type }: { severity: string; type: 'severity' | 'status' | 'validation' }) {
  const colors = type === 'severity'
    ? severityColors[severity.toLowerCase()] || { bg: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)' }
    : statusColors[severity] || { bg: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)' };

  return (
    <span style={{
      fontSize: 11, fontWeight: 500, padding: '1px 6px',
      borderRadius: 'var(--db-radius)',
      background: colors.bg, color: colors.color,
      display: 'inline-block',
    }}>{severity}</span>
  );
}

/* ========== Area Badge ========== */

export function AreaBadge({ area }: { area: string }) {
  return (
    <span style={{
      fontSize: 11, padding: '1px 6px', borderRadius: 'var(--db-radius)',
      background: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)',
    }}>{area}</span>
  );
}

/* ========== Confidence Bar ========== */

export function ConfidenceBar({ confidence }: { confidence: number }) {
  const pct = confidence <= 1 ? Math.round(confidence * 100) : Math.round(confidence);
  const color = pct >= 80 ? 'var(--db-green-text)'
    : pct >= 60 ? 'var(--db-amber-text)' : 'var(--db-red-text)';

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
      <span style={{
        width: 48, height: 4, background: 'var(--db-border-default)', borderRadius: 2,
        display: 'inline-block', position: 'relative', overflow: 'hidden',
      }}>
        <span style={{
          position: 'absolute', left: 0, top: 0, height: '100%', borderRadius: 2,
          width: `${pct}%`, background: color,
        }} />
      </span>
      <span style={{ fontSize: 11, color: 'var(--db-text-secondary)' }}>{pct}%</span>
    </span>
  );
}

/* ========== Pill Badge ========== */

export function Pill({ bg, color, children }: { bg: string; color: string; children: React.ReactNode }) {
  return (
    <span style={{
      fontSize: 11, fontWeight: 500, padding: '2px 8px',
      borderRadius: 'var(--db-radius)', whiteSpace: 'nowrap',
      background: bg, color: color,
    }}>{children}</span>
  );
}

/* ========== Empty State ========== */

export function EmptyState({ icon, title, description }: { icon: React.ReactNode; title: string; description: string }) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '2px dashed var(--db-border-strong)',
      borderRadius: 'var(--db-radius-lg)',
      padding: 48, textAlign: 'center',
    }}>
      <div style={{ opacity: 0.3, marginBottom: 8 }}>{icon}</div>
      <div style={{ fontSize: 15, fontWeight: 500, color: 'var(--db-text-secondary)', marginBottom: 4 }}>{title}</div>
      <div style={{ fontSize: 13, color: 'var(--db-text-tertiary)' }}>{description}</div>
    </div>
  );
}

/* ========== Search Input ========== */

export function SearchInput({ value, onChange, placeholder }: {
  value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  return (
    <input
      type="text"
      value={value}
      onChange={e => onChange(e.target.value)}
      placeholder={placeholder || 'Search...'}
      style={{
        fontSize: 13, padding: '6px 12px',
        border: '1px solid var(--db-border-strong)',
        borderRadius: 'var(--db-radius)',
        background: 'var(--db-bg-white)',
        color: 'var(--db-text-primary)',
        fontFamily: 'inherit',
        width: 240,
        outline: 'none',
        transition: 'border-color 120ms ease',
      }}
      onFocus={e => { e.currentTarget.style.borderColor = 'var(--db-blue-text)'; }}
      onBlur={e => { e.currentTarget.style.borderColor = 'var(--db-border-strong)'; }}
    />
  );
}

/* ========== Pagination ========== */

export function Pagination({ page, totalPages, onChange }: {
  page: number; totalPages: number; onChange: (page: number) => void;
}) {
  if (totalPages <= 1) return null;

  return (
    <div style={{
      display: 'flex', justifyContent: 'center', alignItems: 'center',
      gap: 4, marginTop: 20,
    }}>
      <button onClick={() => onChange(page - 1)} disabled={page <= 1} style={paginationBtnStyle(false, page <= 1)}>
        ←
      </button>
      {Array.from({ length: totalPages }, (_, i) => i + 1)
        .filter(p => p === 1 || p === totalPages || Math.abs(p - page) <= 2)
        .reduce<(number | '...')[]>((acc, p) => {
          const last = acc[acc.length - 1];
          if (typeof last === 'number' && p - last > 1) acc.push('...');
          acc.push(p);
          return acc;
        }, [])
        .map((p, i) =>
          p === '...'
            ? <span key={`dots-${i}`} style={{ fontSize: 12, color: 'var(--db-text-tertiary)', padding: '0 4px' }}>…</span>
            : <button key={p} onClick={() => onChange(p)} style={paginationBtnStyle(p === page, false)}>
                {p}
              </button>
        )}
      <button onClick={() => onChange(page + 1)} disabled={page >= totalPages} style={paginationBtnStyle(false, page >= totalPages)}>
        →
      </button>
    </div>
  );
}

function paginationBtnStyle(active: boolean, disabled: boolean): React.CSSProperties {
  return {
    fontSize: 12, fontWeight: active ? 600 : 400, fontFamily: 'inherit',
    padding: '4px 10px', borderRadius: 'var(--db-radius)',
    border: active ? '1px solid var(--db-blue-text)' : '1px solid var(--db-border-strong)',
    background: active ? 'var(--db-blue-bg)' : 'var(--db-bg-white)',
    color: active ? 'var(--db-blue-text)' : disabled ? 'var(--db-text-tertiary)' : 'var(--db-text-secondary)',
    cursor: disabled ? 'default' : 'pointer',
    opacity: disabled ? 0.5 : 1,
    transition: 'all 120ms ease',
  };
}

/* ========== Normalize Confidence ========== */

export function normalizeConfidence(confidence: number): number {
  return confidence <= 1 ? Math.round(confidence * 100) : Math.round(confidence);
}
