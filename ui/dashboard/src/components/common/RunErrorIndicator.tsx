'use client';

import { useState } from 'react';
import { Popover, ScrollArea } from '@mantine/core';
import { IconAlertTriangle } from '@tabler/icons-react';

// Popover geometry — named constants (not inline magic numbers) so every
// call site across the community and enterprise dashboards renders the error
// popover identically.
const POPOVER_WIDTH = 460;
const POPOVER_MAX_HEIGHT = 320;

export interface RunErrorIndicatorProps {
  // A single run-level error (e.g. run.error) or a list of per-area errors
  // (e.g. summary.errors). Falsy / blank entries are ignored.
  errors?: string | string[] | null;
  // Heading shown above the error text in the expanded popover. Defaults to
  // an "N area(s) failed during analysis" summary derived from the count.
  label?: string;
  // Icon size in px — matches the surrounding status icons at each call site.
  iconSize?: number;
}

// normalizeRunErrors flattens the string | string[] input into a trimmed,
// de-blanked list. Exported for unit testing and reuse.
export function normalizeRunErrors(errors?: string | string[] | null): string[] {
  if (!errors) return [];
  const list = Array.isArray(errors) ? errors : [errors];
  return list.map((e) => (e ?? '').trim()).filter(Boolean);
}

// RunErrorIndicator collapses a discovery run's error(s) down to a compact
// warning icon. Clicking the icon expands the full error text in a scrollable,
// word-wrapped popover; closing it (click-away, Escape, or clicking the icon
// again) hides the detail while the icon stays put. The icon is derived purely
// from the passed-in error data, so it survives a page refresh as long as the
// run still carries an error — the indicator persists, only the wall of raw
// text is hidden by default.
export function RunErrorIndicator({ errors, label, iconSize = 16 }: RunErrorIndicatorProps) {
  const [opened, setOpened] = useState(false);
  const list = normalizeRunErrors(errors);
  if (list.length === 0) return null;

  const heading = label
    ?? (list.length === 1 ? '1 area failed during analysis' : `${list.length} areas failed during analysis`);

  return (
    <Popover
      opened={opened}
      onChange={setOpened}
      position="bottom-start"
      width={POPOVER_WIDTH}
      shadow="md"
      withArrow
    >
      <Popover.Target>
        <button
          type="button"
          onClick={() => setOpened((o) => !o)}
          aria-label={heading}
          aria-expanded={opened}
          title={heading}
          style={{
            display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
            padding: 2, border: 'none', background: 'transparent',
            color: 'var(--db-red-text)', cursor: 'pointer', lineHeight: 0,
            borderRadius: 'var(--db-radius)',
          }}
        >
          <IconAlertTriangle size={iconSize} />
        </button>
      </Popover.Target>
      <Popover.Dropdown p="xs">
        <div style={{
          fontSize: 13, fontWeight: 500, color: 'var(--db-red-text)',
          marginBottom: 8, display: 'flex', alignItems: 'center', gap: 6,
        }}>
          <IconAlertTriangle size={14} />
          <span>{heading}</span>
        </div>
        {/* Long raw errors (e.g. a GLM/Bedrock context-length dump) live inside
            this scrollable, word-wrapped container rather than spilling into the
            page body. */}
        <ScrollArea.Autosize mah={POPOVER_MAX_HEIGHT} type="auto">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {list.map((err, i) => (
              <div key={i} style={{
                fontSize: 12, color: 'var(--db-red-text)',
                background: 'var(--db-red-bg)', borderRadius: 'var(--db-radius)',
                padding: '6px 8px',
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                fontFamily: 'var(--db-font-mono, ui-monospace, SFMono-Regular, monospace)',
              }}>
                {err}
              </div>
            ))}
          </div>
        </ScrollArea.Autosize>
      </Popover.Dropdown>
    </Popover>
  );
}
