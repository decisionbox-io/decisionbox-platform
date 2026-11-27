import type { CSSProperties } from 'react';

// A band label is the whole point of the badge, so it must not be the thing
// that gives way when the column is narrow.
//
// Mantine renders a Badge as a grid whose label carries
// `overflow:hidden; text-overflow:ellipsis`. Inside a table cell the badge is
// a flex item, so the cell's width wins: the badge shrinks, the label shrinks
// with it, and the reader gets `ACT …` — or, for the second chip in a cell,
// a label measured at ZERO pixels and an empty grey oval with no text in it at
// all. Measured in the insights list: `ACT NOW` rendered into 34px of a 53px
// label, `HIGH CONFIDENCE` into 56px of 101px, and `LEGACY` into 0px of 12px.
//
// Refusing to shrink pushes the width back onto the column, which is the
// correct trade in a table whose first column is a wrapping sentence: the
// sentence re-wraps and every label stays whole.
//
// `flexShrink` alone does NOT do it, which cost a round to learn: it preserves
// the item's flex BASE size, and that base was already collapsible, because
// the label Mantine puts inside the badge is a grid item carrying
// `min-width: 0`. The automatic minimum size is therefore zero and the badge
// still gave way. `minWidth: max-content` is the floor that actually holds —
// measured live, `HIGH CONFIDENCE` went from a 56px label in a 74px badge to
// the whole 101px in 119px, with the table still inside the window.
//
// Both are kept: the floor is what fixes it, and not shrinking is what stops
// the badge being chosen as the thing to squeeze in the first place.
export const noShrink: CSSProperties = { flexShrink: 0, minWidth: 'max-content' };
