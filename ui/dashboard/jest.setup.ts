// Polyfills for jsdom — Mantine components use browser APIs that jsdom omits.
// Runs BEFORE the Jest test framework loads, so no `expect`, `beforeEach`, etc.

// ResizeObserver: used by Mantine ScrollArea and Popover.
class ResizeObserverPolyfill {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
(global as unknown as { ResizeObserver: typeof ResizeObserverPolyfill }).ResizeObserver = ResizeObserverPolyfill;

// matchMedia: used by Mantine's responsive Grid / visibleFrom / hiddenFrom.
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

// scrollIntoView: used by links and Mantine's autofocus scroll logic.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}
