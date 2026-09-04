import type { Metadata } from 'next';
import { ColorSchemeScript, MantineProvider, createTheme } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { NextIntlClientProvider } from 'next-intl';
import { getLocale } from 'next-intl/server';
import { DM_Sans } from 'next/font/google';
import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import '@mantine/dates/styles.css';
import '@/styles/tokens.css';
import { dirForLocale } from '@/i18n/locales';
import I18nProvider from '@/components/providers/I18nProvider';
import { ChatDrawerProvider } from '@/components/ask/ChatDrawerProvider';
import ChatDrawer from '@/components/ask/ChatDrawer';
import ChatLauncher from '@/components/ask/ChatLauncher';

const dmSans = DM_Sans({
  subsets: ['latin'],
  variable: '--font-dm-sans',
});

const theme = createTheme({
  fontFamily: 'var(--font-dm-sans), -apple-system, BlinkMacSystemFont, sans-serif',
  headings: {
    fontFamily: 'var(--font-dm-sans), -apple-system, BlinkMacSystemFont, sans-serif',
  },
  primaryColor: 'dark',
  defaultRadius: 'md',
  components: {
    Badge: {
      defaultProps: {
        variant: 'light',
      },
    },
  },
});

export const metadata: Metadata = {
  title: 'DecisionBox',
  description: 'AI-powered data discovery platform',
};

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  // Locale resolved per request (cookie → Accept-Language → default) by
  // i18n/request.ts. NextIntlClientProvider (no props) inherits the locale +
  // messages from that request config and forwards them to client components.
  const locale = await getLocale();

  return (
    <html lang={locale} dir={dirForLocale(locale)} className={dmSans.variable} suppressHydrationWarning>
      <head>
        <ColorSchemeScript />
      </head>
      <body>
        <NextIntlClientProvider>
          <MantineProvider theme={theme}>
            <Notifications position="top-right" />
            <I18nProvider locale={locale}>
              <ChatDrawerProvider>
                {children}
                <ChatDrawer />
                <ChatLauncher />
              </ChatDrawerProvider>
            </I18nProvider>
          </MantineProvider>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
