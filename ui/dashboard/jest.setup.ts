// Polyfills for jsdom — Mantine components use browser APIs that jsdom omits.
// Runs BEFORE the Jest test framework loads, so no `expect`, `beforeEach`, etc.
//
// Tests that opt into `@jest-environment node` (e.g. timeout / network
// tests) run without a DOM, so each block guards on `window` /
// `Element` existing before installing the polyfill.

// ResizeObserver: used by Mantine ScrollArea and Popover.
class ResizeObserverPolyfill {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
(global as unknown as { ResizeObserver: typeof ResizeObserverPolyfill }).ResizeObserver = ResizeObserverPolyfill;

// matchMedia: used by Mantine's responsive Grid / visibleFrom / hiddenFrom.
if (typeof window !== 'undefined') {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

// scrollIntoView: used by links and Mantine's autofocus scroll logic.
if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}
