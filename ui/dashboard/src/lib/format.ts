'use client';

import { useLocale } from 'next-intl';
import { useMemo } from 'react';

// Locale-aware formatting bound to the active UI locale, built on Intl.
// Use this instead of ad-hoc `toLocaleString()` / `new Date().toLocaleDateString()`
// so grouping, decimal separators and date order follow the UI locale.
//
// Currency is driven by the value's own currency code (e.g. USD token costs),
// not the UI locale — the locale only changes grouping/symbol placement.
export function useFormat() {
  const locale = useLocale();

  return useMemo(
    () => ({
      number: (value: number, options?: Intl.NumberFormatOptions) =>
        new Intl.NumberFormat(locale, options).format(value),
      dateTime: (value: Date | number | string, options?: Intl.DateTimeFormatOptions) =>
        new Intl.DateTimeFormat(locale, options).format(
          typeof value === 'string' ? new Date(value) : value,
        ),
      currency: (value: number, currency: string) =>
        new Intl.NumberFormat(locale, { style: 'currency', currency }).format(value),
    }),
    [locale],
  );
}
