/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { NextIntlClientProvider } from 'next-intl';
import LanguageSwitcher from '@/components/layout/LanguageSwitcher';
import { api } from '@/lib/api';

const refresh = jest.fn();
jest.mock('next/navigation', () => ({
  useRouter: () => ({ refresh }),
}));

jest.mock('@/lib/api', () => ({
  api: { updatePreferences: jest.fn().mockResolvedValue({ locale: 'tr' }) },
}));

// Two supported locales so the switcher renders.
jest.mock('@/i18n/messages', () => ({ SUPPORTED_LOCALES: ['en', 'tr'] }));

const messages = { language: { label: 'Language', beta: 'beta' } };

function mount(locale = 'en') {
  return render(
    <NextIntlClientProvider locale={locale} messages={messages}>
      <MantineProvider>
        <LanguageSwitcher />
      </MantineProvider>
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  // reset cookies
  document.cookie = 'NEXT_LOCALE=; path=/; max-age=0';
});

describe('LanguageSwitcher', () => {
  it('renders the trigger with the available locales', async () => {
    mount('en');
    fireEvent.click(screen.getByLabelText('Language'));
    await waitFor(() => {
      expect(screen.getByText('Türkçe')).toBeInTheDocument();
      expect(screen.getByText('English')).toBeInTheDocument();
    });
  });

  it('tags in-testing locales (Turkish) with a beta badge, not English', async () => {
    mount('en');
    fireEvent.click(screen.getByLabelText('Language'));
    const beta = await screen.findAllByText('beta');
    // Exactly one beta tag (Turkish), and it sits with the Türkçe row.
    expect(beta).toHaveLength(1);
    expect(screen.getByText('Türkçe').parentElement).toHaveTextContent('beta');
    expect(screen.getByText('English').parentElement).not.toHaveTextContent('beta');
  });

  it('switching persists the cookie, calls the API and refreshes', async () => {
    mount('en');
    fireEvent.click(screen.getByLabelText('Language'));
    fireEvent.click(await screen.findByText('Türkçe'));

    await waitFor(() => {
      expect(api.updatePreferences).toHaveBeenCalledWith('tr');
    });
    expect(document.cookie).toContain('NEXT_LOCALE=tr');
    expect(refresh).toHaveBeenCalled();
  });

  it('selecting the current locale is a no-op', async () => {
    mount('en');
    fireEvent.click(screen.getByLabelText('Language'));
    fireEvent.click(await screen.findByText('English'));
    expect(api.updatePreferences).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
  });
});
