// Global test mock for next-intl.
//
// Components migrated to `useTranslations` would otherwise throw "No intl
// context found" when a unit test renders them without a provider. This manual
// mock (auto-applied by Jest for node modules) backs the hooks with a real
// use-intl translator over the English catalog — so components render their real
// English strings in tests (existing getByText assertions keep working) with
// full ICU + rich-text support, and no per-test provider wrapping is needed.
//
// The catalog is the base + (when present, i.e. the composed enterprise overlay
// tree) the enterprise overlay, matching what the app renders at runtime.
import { createTranslator, createFormatter } from 'use-intl/core';

const actual = jest.requireActual('next-intl');

// eslint-disable-next-line @typescript-eslint/no-var-requires, @typescript-eslint/no-require-imports
const en = require('@/i18n/messages/en.json');
let messages = en;
try {
  // Present only in the composed enterprise-overlay tree.
  // eslint-disable-next-line @typescript-eslint/no-var-requires, @typescript-eslint/no-require-imports
  const entEn = require('@/i18n/messages/en.enterprise.json');
  // eslint-disable-next-line @typescript-eslint/no-var-requires, @typescript-eslint/no-require-imports
  const { deepMerge } = require('@/i18n/deepMerge');
  messages = deepMerge(en, entEn);
} catch {
  // community build — base catalog only
}

module.exports = {
  ...actual,
  useTranslations: (namespace?: string) =>
    createTranslator({ locale: 'en', messages, namespace }),
  useFormatter: () => createFormatter({ locale: 'en' }),
  useLocale: () => 'en',
  NextIntlClientProvider: ({ children }: { children: React.ReactNode }) => children,
};
