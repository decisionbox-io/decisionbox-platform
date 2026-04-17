'use client';

import { useState, ReactNode } from 'react';
import { Button, Collapse } from '@mantine/core';
import { IconCode, IconChevronDown, IconChevronUp } from '@tabler/icons-react';

// TechnicalDetails wraps SQL queries, exploration steps, token counts, and
// other engine internals in a collapsed-by-default section. Non-technical
// users see a clean narrative page; power users click to reveal the details.
//
// State is intentionally component-local and defaults to collapsed on every
// mount — no persistence. A user who expanded this section on one detail page
// will still see a collapsed section on the next, which keeps the default
// experience uniform for everyone.
export default function TechnicalDetails({
  children,
  label = 'technical details',
}: {
  children: ReactNode;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <Button
        variant="subtle"
        size="sm"
        leftSection={<IconCode size={14} />}
        rightSection={open ? <IconChevronUp size={14} /> : <IconChevronDown size={14} />}
        onClick={() => setOpen(o => !o)}
        aria-expanded={open}
      >
        {open ? `Hide ${label}` : `Show ${label}`}
      </Button>
      <Collapse in={open}>
        <div style={{ marginTop: 12 }}>{children}</div>
      </Collapse>
    </div>
  );
}
