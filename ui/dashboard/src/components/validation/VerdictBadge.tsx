'use client';

import { Badge } from '@mantine/core';
import { IconCheck, IconX, IconAlertTriangle, IconHelp, IconMinus } from '@tabler/icons-react';
import { statusMeta, toneToMantineColor } from './statusMeta';
import type { ValidationStatus } from '@/lib/api';

function iconFor(status: ValidationStatus | string | undefined) {
  switch (status) {
    case 'confirmed':
    case 'supported':
      return <IconCheck size={12} />;
    case 'rejected':
      return <IconX size={12} />;
    case 'partial':
    case 'unverifiable':
    case 'adjusted':
      return <IconAlertTriangle size={12} />;
    case 'validation_disabled':
    case 'skipped_budget_cap':
    case 'unverified':
      return <IconMinus size={12} />;
    default:
      return <IconHelp size={12} />;
  }
}

export function VerdictBadge({
  status,
  size = 'sm',
}: {
  status: ValidationStatus | string | undefined;
  size?: 'xs' | 'sm' | 'md';
}) {
  const meta = statusMeta(status);
  return (
    <Badge
      size={size}
      variant="light"
      color={toneToMantineColor(meta.tone)}
      leftSection={iconFor(status)}
    >
      {meta.label}
    </Badge>
  );
}
