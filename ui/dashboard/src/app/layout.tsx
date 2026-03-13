import type { Metadata } from 'next';
import { ColorSchemeScript, MantineProvider, createTheme } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { DM_Sans } from 'next/font/google';
import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import '@mantine/dates/styles.css';
import '@/styles/tokens.css';

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

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={dmSans.variable} suppressHydrationWarning>
      <head>
        <ColorSchemeScript />
      </head>
      <body>
        <MantineProvider theme={theme}>
          <Notifications position="top-right" />
          {children}
        </MantineProvider>
      </body>
    </html>
  );
}
