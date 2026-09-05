'use client';

import { Badge } from '@mantine/core';
import { IconCheck, IconX, IconAlertTriangle, IconHelp, IconMinus } from '@tabler/icons-react';
import { useTranslations } from 'next-intl';
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

// Known verdict statuses that have a localized display label. Anything
// outside this set falls back to the "unknown" label — matching the
// English fallback in statusMeta.
const KNOWN_VERDICTS = new Set([
  'confirmed',
  'supported',
  'partial',
  'rejected',
  'unverifiable',
  'validation_disabled',
  'skipped_budget_cap',
  'adjusted',
  'unverified',
  'error',
]);

export function VerdictBadge({
  status,
  size = 'sm',
}: {
  status: ValidationStatus | string | undefined;
  size?: 'xs' | 'sm' | 'md';
}) {
  const t = useTranslations('validation');
  const meta = statusMeta(status);
  const label = status && KNOWN_VERDICTS.has(status)
    ? t(`verdict_${status}`)
    : t('verdict_unknown');
  return (
    <Badge
      size={size}
      variant="light"
      color={toneToMantineColor(meta.tone)}
      leftSection={iconFor(status)}
    >
      {label}
    </Badge>
  );
}
