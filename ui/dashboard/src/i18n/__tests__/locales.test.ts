import {
  DEFAULT_LOCALE,
  dirForLocale,
  localeLabel,
  matchAcceptLanguage,
  resolveLocale,
} from '@/i18n/locales';

const SUPPORTED = ['en', 'tr'];

describe('resolveLocale', () => {
  it('prefers a valid cookie', () => {
    expect(resolveLocale('tr', 'en-US,en;q=0.9', SUPPORTED)).toBe('tr');
  });

  it('ignores an unsupported cookie and falls through', () => {
    expect(resolveLocale('de', 'tr;q=0.9', SUPPORTED)).toBe('tr');
  });

  it('falls back to Accept-Language when no cookie', () => {
    expect(resolveLocale(undefined, 'tr-TR,tr;q=0.9,en;q=0.8', SUPPORTED)).toBe('tr');
  });

  it('falls back to the default when nothing matches', () => {
    expect(resolveLocale(undefined, 'fr-FR,fr;q=0.9', SUPPORTED)).toBe(DEFAULT_LOCALE);
    expect(resolveLocale(undefined, undefined, SUPPORTED)).toBe(DEFAULT_LOCALE);
  });
});

describe('matchAcceptLanguage', () => {
  it('matches the full tag first', () => {
    expect(matchAcceptLanguage('en-US,en;q=0.9', ['en', 'tr'])).toBe('en');
  });

  it('matches the primary subtag when the full tag is unsupported', () => {
    expect(matchAcceptLanguage('tr-TR', ['en', 'tr'])).toBe('tr');
  });

  it('honours q-weight ordering', () => {
    expect(matchAcceptLanguage('en;q=0.3, tr;q=0.9', ['en', 'tr'])).toBe('tr');
  });

  it('returns null when nothing matches or header is empty', () => {
    expect(matchAcceptLanguage('fr,de', ['en', 'tr'])).toBeNull();
    expect(matchAcceptLanguage('', ['en', 'tr'])).toBeNull();
    expect(matchAcceptLanguage(null, ['en', 'tr'])).toBeNull();
  });
});

describe('dirForLocale', () => {
  it('is ltr for en/tr', () => {
    expect(dirForLocale('en')).toBe('ltr');
    expect(dirForLocale('tr')).toBe('ltr');
  });

  it('is rtl for Arabic/Hebrew (RTL-readiness)', () => {
    expect(dirForLocale('ar')).toBe('rtl');
    expect(dirForLocale('he-IL')).toBe('rtl');
  });
});

describe('localeLabel', () => {
  it('returns the language autonym', () => {
    // Intl.DisplayNames in Node returns "English" / "Türkçe" (or the code as a
    // safe fallback in a stripped ICU build).
    expect(localeLabel('en')).toMatch(/English|en/);
    expect(localeLabel('tr')).toMatch(/Türkçe|Turkish|tr/);
  });
});
