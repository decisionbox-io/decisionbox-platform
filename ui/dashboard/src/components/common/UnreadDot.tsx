'use client';

// UnreadDot is a tiny inline indicator rendered before a row's title when the
// underlying insight or recommendation has not been read yet. When the user
// opens the detail page the corresponding read mark is saved server-side and
// the dot disappears on next render. Deliberately minimal: no hover state, no
// click handler — its only job is to signal "new to you" without dimming the
// readable content of the row.
export default function UnreadDot({ unread, size = 8 }: { unread: boolean; size?: number }) {
  return (
    <span
      aria-hidden={!unread}
      title={unread ? 'Unread' : undefined}
      style={{
        display: 'inline-block',
        width: size,
        height: size,
        borderRadius: '50%',
        background: unread ? 'var(--db-text-link)' : 'transparent',
        // Reserve a constant footprint (dot + trailing gap) so reading/unreading
        // a row doesn't jitter the title left/right.
        marginRight: 6,
        flexShrink: 0,
      }}
    />
  );
}
