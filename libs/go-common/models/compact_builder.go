package models

import (
	"math"
	"sort"
	"time"
)

// BuildCompactResult turns a slice of warehouse rows (one map per row,
// keyed by column name) into a CompactResult digest.
//
// The function is pure: same input, byte-identical output across runs
// (modulo Go map iteration order, which is paved over with sorted key
// walks where it matters). It performs no IO and pulls in only the
// standard library.
//
// Behavior summary:
//   - Empty / nil input → CompactResult with RowCount 0 and no rows.
//   - RowCount <= CompactInlineThreshold → AllRows holds every row;
//     HeadRows mirrors the head of AllRows; TailRows is omitted.
//   - RowCount > CompactInlineThreshold → HeadRows + TailRows hold
//     boundary rows; AllRows is omitted.
//   - Per column: type inferred from non-nil values; numerics get
//     min/p25/median/p75/max; timestamps get min/max ISO strings;
//     low-cardinality strings get top-3 frequency counts.
//   - NaN / +Inf / -Inf floats are excluded from numeric percentiles
//     and counted toward NullCount so a single bad row cannot poison
//     the statistics.
func BuildCompactResult(rows []map[string]any) CompactResult {
	out := CompactResult{
		RowCount: len(rows),
		Columns:  []ColumnSummary{},
		HeadRows: []map[string]any{},
	}
	if len(rows) == 0 {
		return out
	}

	headLimit := HeadTailRowCount
	if headLimit > len(rows) {
		headLimit = len(rows)
	}
	out.HeadRows = cloneRows(rows[:headLimit])

	// AllRows wins over TailRows. When the entire result fits inline
	// (RowCount <= CompactInlineThreshold) AllRows carries every row,
	// so emitting TailRows in addition would just duplicate them.
	if len(rows) <= CompactInlineThreshold {
		out.AllRows = cloneRows(rows)
	} else if len(rows) > 2*HeadTailRowCount {
		tailStart := len(rows) - HeadTailRowCount
		out.TailRows = cloneRows(rows[tailStart:])
	}

	out.Columns = summarizeColumns(rows)
	return out
}

// summarizeColumns walks every row, picks up the union of column
// names in first-seen order (per-row map iteration order is
// non-deterministic so we lock the order to the first row that
// introduces each column), then derives one ColumnSummary per column.
func summarizeColumns(rows []map[string]any) []ColumnSummary {
	type colSlot struct {
		name   string
		values []any
	}

	var slots []colSlot
	idx := make(map[string]int, len(rows))
	for _, row := range rows {
		// Stable per-row key walk so a single row never re-orders the
		// global column list.
		keys := sortedKeys(row)
		for _, k := range keys {
			if _, ok := idx[k]; !ok {
				idx[k] = len(slots)
				slots = append(slots, colSlot{name: k, values: make([]any, 0, len(rows))})
			}
		}
	}

	for _, row := range rows {
		for _, slot := range slots {
			slots[idx[slot.name]].values = append(slots[idx[slot.name]].values, row[slot.name])
		}
	}

	summaries := make([]ColumnSummary, 0, len(slots))
	for _, slot := range slots {
		summaries = append(summaries, summarizeColumn(slot.name, slot.values))
	}
	return summaries
}

// summarizeColumn produces the ColumnSummary for one column's full
// value slice. The slice has one entry per row (nil for missing keys
// or actual nil values) so NullCount and the per-kind statistics line
// up with RowCount.
func summarizeColumn(name string, values []any) ColumnSummary {
	cs := ColumnSummary{Name: name}

	nonNil := make([]any, 0, len(values))
	for _, v := range values {
		if v == nil {
			cs.NullCount++
			continue
		}
		nonNil = append(nonNil, v)
	}
	if len(nonNil) == 0 {
		cs.Kind = ColumnKindNull
		return cs
	}

	cs.Kind = inferKind(nonNil)
	switch cs.Kind {
	case ColumnKindNumber:
		summarizeNumeric(&cs, nonNil)
	case ColumnKindTimestamp:
		summarizeTimestamp(&cs, nonNil)
	case ColumnKindString:
		summarizeString(&cs, nonNil)
	case ColumnKindBoolean:
		summarizeBoolean(&cs, nonNil)
	case ColumnKindMixed:
		// No statistics for mixed-type columns — they're a sign the
		// LLM should look at HeadRows / TailRows directly anyway.
	case ColumnKindNull:
		// Unreachable when len(nonNil) > 0 but listed for completeness.
	}
	return cs
}

// inferKind picks the column kind by scanning every non-nil value. A
// single value of a different kind drops the column to Mixed —
// statistics over a heterogeneous column would mislead more than help.
func inferKind(values []any) ColumnKind {
	var seen ColumnKind
	for _, v := range values {
		k := kindOf(v)
		if seen == "" {
			seen = k
			continue
		}
		if k != seen {
			return ColumnKindMixed
		}
	}
	if seen == "" {
		return ColumnKindNull
	}
	return seen
}

// kindOf maps a single Go value to the ColumnKind it represents.
// All numeric widths collapse to ColumnKindNumber so a column of
// mixed int / float values stays "number" (the warehouse drivers
// regularly mix widths inside one result set).
func kindOf(v any) ColumnKind {
	switch v.(type) {
	case bool:
		return ColumnKindBoolean
	case time.Time:
		return ColumnKindTimestamp
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return ColumnKindNumber
	case string:
		return ColumnKindString
	default:
		return ColumnKindMixed
	}
}

// summarizeNumeric fills the numeric percentile fields. NaN / +Inf /
// -Inf are dropped from the percentile pool and counted toward
// NullCount so callers get a clean Min/Max range even when a single
// row has a divide-by-zero.
func summarizeNumeric(cs *ColumnSummary, values []any) {
	floats := make([]float64, 0, len(values))
	for _, v := range values {
		f, ok := toFloat64(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			cs.NullCount++
			continue
		}
		floats = append(floats, f)
	}
	if len(floats) == 0 {
		return
	}
	sort.Float64s(floats)
	min := floats[0]
	max := floats[len(floats)-1]
	cs.Min = &min
	cs.Max = &max
	p25 := percentile(floats, 0.25)
	med := percentile(floats, 0.50)
	p75 := percentile(floats, 0.75)
	cs.P25 = &p25
	cs.Median = &med
	cs.P75 = &p75
}

// percentile returns the q-th percentile of an already-sorted slice
// using linear interpolation (a.k.a. type-7 in R / numpy's default).
// Caller guarantees len(sorted) >= 1.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	low := int(math.Floor(pos))
	high := int(math.Ceil(pos))
	if low == high {
		return sorted[low]
	}
	frac := pos - float64(low)
	return sorted[low]*(1-frac) + sorted[high]*frac
}

// summarizeTimestamp tracks the min and max time as RFC3339 strings.
// Callers see ISO-8601 — same shape every warehouse driver round-
// trips through json.Marshal for time.Time values.
func summarizeTimestamp(cs *ColumnSummary, values []any) {
	var minT, maxT time.Time
	for i, v := range values {
		t, ok := v.(time.Time)
		if !ok {
			cs.NullCount++
			continue
		}
		if i == 0 || t.Before(minT) {
			minT = t
		}
		if i == 0 || t.After(maxT) {
			maxT = t
		}
	}
	cs.MinTime = minT.UTC().Format(time.RFC3339)
	cs.MaxTime = maxT.UTC().Format(time.RFC3339)
}

// summarizeString fills Distinct + Top for string columns. Top is
// emitted only when Distinct <= TopValueCardinality so high-
// cardinality columns (user ids, free-text comments) don't leak
// individual values into the prompt.
func summarizeString(cs *ColumnSummary, values []any) {
	counts := make(map[string]int, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			cs.NullCount++
			continue
		}
		counts[s]++
	}
	cs.Distinct = len(counts)
	if cs.Distinct == 0 || cs.Distinct > TopValueCardinality {
		return
	}

	type kv struct {
		k string
		n int
	}
	pairs := make([]kv, 0, len(counts))
	for k, n := range counts {
		pairs = append(pairs, kv{k: k, n: n})
	}
	// Higher count first; lexical tie-break for determinism.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].k < pairs[j].k
	})

	limit := 3
	if limit > len(pairs) {
		limit = len(pairs)
	}
	cs.Top = make([]ValueCount, 0, limit)
	for i := 0; i < limit; i++ {
		cs.Top = append(cs.Top, ValueCount{Value: pairs[i].k, Count: pairs[i].n})
	}
}

// summarizeBoolean fills Distinct (1 or 2) and a Top list with
// frequency counts for true / false. Boolean columns are always
// low-cardinality so Top is unconditional.
func summarizeBoolean(cs *ColumnSummary, values []any) {
	var trueN, falseN int
	for _, v := range values {
		b, ok := v.(bool)
		if !ok {
			cs.NullCount++
			continue
		}
		if b {
			trueN++
		} else {
			falseN++
		}
	}
	if trueN == 0 && falseN == 0 {
		return
	}
	distinct := 0
	if trueN > 0 {
		distinct++
	}
	if falseN > 0 {
		distinct++
	}
	cs.Distinct = distinct

	// Determinism: emit in (count desc, value asc-by-bool-string) order.
	pairs := []ValueCount{}
	if trueN > 0 {
		pairs = append(pairs, ValueCount{Value: true, Count: trueN})
	}
	if falseN > 0 {
		pairs = append(pairs, ValueCount{Value: false, Count: falseN})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		// false < true lexically when stringified — match that ordering.
		bi, _ := pairs[i].Value.(bool)
		bj, _ := pairs[j].Value.(bool)
		return !bi && bj
	})
	cs.Top = pairs
}

// toFloat64 promotes any numeric Go value to float64. Returns ok=false
// for non-numeric values; callers treat those as nulls.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// sortedKeys returns the keys of m in lexical order. Used to make the
// column-discovery walk deterministic (Go map iteration order is
// randomised).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cloneRows shallow-copies a slice of row maps. The digest is meant to
// be persisted and serialized after the warehouse driver may have
// reused its row buffers, so we don't want references back into
// caller-owned memory.
func cloneRows(in []map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, row := range in {
		clone := make(map[string]any, len(row))
		for k, v := range row {
			clone[k] = v
		}
		out[i] = clone
	}
	return out
}
