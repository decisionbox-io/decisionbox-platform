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
export const noShrink: CSSProperties = { flexShrink: 0 };
