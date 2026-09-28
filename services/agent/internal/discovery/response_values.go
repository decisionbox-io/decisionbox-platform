package discovery

// Reading an answer out of a model response, when the response is not only the answer.
//
// The observed shapes, all from us.anthropic.claude-opus-4-8 on this pipeline:
//
//	{"insights": []}          followed by a paragraph explaining why it is empty
//	{"insights": []}          followed by the real envelope
//	{"insights": []}          followed by a bracketed note, a citation marker, or a
//	                          JSON example quoted inside the prose
//	{"insights":"[{...}]"}    the array as a JSON-encoded string
//	the real envelope         followed by a fenced example of an empty one
//
// Four attempts were made to classify the trailing text -- json.Decoder.More, "does it
// start with { or [", "does a second value decode", "does a composite value decode" --
// and every one of them let a real case through in one direction or the other. The
// question itself was wrong: a parser cannot reliably tell an explanation from an
// answer, and every mistake is expensive in both directions. A false positive
// re-prompts an answer that was already correct, which is common here -- two of five
// analysis areas are legitimately empty in every run, each with an explanation
// attached. A false negative ships an empty area and discards real findings.
//
// So the trailing text is not classified. Every top-level JSON value is decoded, each
// is offered to the item decoder, and the first one that actually produces a finding
// wins. Prose contributes no values, or values that yield no findings, and is ignored
// without being judged. A placeholder in front of the real answer no longer costs a
// re-prompt either -- the real answer is simply used.
//
// Nothing at all is inferred from the unparsed remainder either. The last version
// searched it for the quoted envelope key, to catch a further answer that was trying to
// parse and failed -- and that broke the same common case a sixth time, because an
// explanation may quote the key it is explaining: `The "insights" array is empty
// because this schema has no session-level data.`
//
// The trade is now decided on what has actually been seen rather than on what could
// happen. A correctly empty area with an explanation attached was observed 17 times over
// 12 replays and in every full run; a truncated second answer behind an empty first one
// has never been observed once. Guarding the second at the cost of the first had it
// backwards. A response cut off by a token limit is also not the parser's to detect --
// the stop reason on the LLM result says so directly, and that is where it belongs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// maxResponseValues bounds how many top-level values are read out of one response.
// Real ones carry one or two; the cap is here so a pathological response cannot make
// this loop the expensive part of a run.
const maxResponseValues = 16

// jsonValues decodes the top-level JSON values in s, in order.
//
// err is set when nothing decoded at all, so a caller can report why a response was
// unreadable rather than inventing a reason, and when the response ran past
// maxResponseValues -- a response with that many top-level values is not one this
// understands, and quietly answering from the first of them would hide the rest.
// Trailing text that is not JSON is not an error and is not reported: see the note above
// on why nothing is inferred from it.
func jsonValues(s string) (vals []json.RawMessage, err error) {
	dec := json.NewDecoder(strings.NewReader(s))
	for {
		var v json.RawMessage
		if derr := dec.Decode(&v); derr != nil {
			if errors.Is(derr, io.EOF) || len(vals) > 0 {
				return vals, nil
			}
			return nil, derr
		}
		vals = append(vals, v)
		if len(vals) > maxResponseValues {
			return nil, fmt.Errorf("response holds more than %d top-level JSON values", maxResponseValues)
		}
	}
}

// envelopeItems pulls the item array out of one decoded JSON value: a bare array, or an
// object carrying key under any capitalisation.
//
// Errors are worded per key so each parser reports what its own contract calls things.
// A missing key is an error rather than an empty result, because an object with a
// different key is a response that did not answer -- and a silent empty result there
// would never be retried.
func envelopeItems(val json.RawMessage, key string) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(val))
	var raws []json.RawMessage
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(val, &raws); err != nil {
			return nil, err
		}
		return raws, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(val, &envelope); err != nil {
		return nil, err
	}
	// Matched case-insensitively, as encoding/json does when decoding into a struct
	// tag -- some models capitalise it (`{"Insights":[…]}`). An exact match wins, and
	// among case variants the first in sorted order does: breaking out of a map range on
	// the first EqualFold hit made the result depend on Go's randomised iteration order,
	// so `{"Insights":[],"insights":[{...}]}` returned either one.
	itemsRaw, found := envelope[key]
	if !found {
		variants := make([]string, 0, 2)
		for k := range envelope {
			if strings.EqualFold(k, key) {
				variants = append(variants, k)
			}
		}
		if len(variants) > 0 {
			sort.Strings(variants)
			itemsRaw, found = envelope[variants[0]], true
		}
	}
	if !found {
		return nil, fmt.Errorf("response is missing the %q key", key)
	}
	// A null array decodes into a nil slice without error; that is a malformed
	// response, not a legitimately empty one.
	if strings.TrimSpace(string(itemsRaw)) == "null" {
		return nil, fmt.Errorf("%q is null", key)
	}
	// Some models emit the array as a JSON-encoded string -- `{"insights":"[{...}]"}`.
	// Observed on a re-prompt, carrying a complete and sound insight that was discarded
	// on the wrapper. Unwrap one level; a string that is not an encoded array still
	// fails below, so a refusal written in prose cannot become a silent empty result.
	if strings.HasPrefix(strings.TrimSpace(string(itemsRaw)), `"`) {
		var inner string
		if err := json.Unmarshal(itemsRaw, &inner); err == nil {
			itemsRaw = json.RawMessage(inner)
		}
		// The null check above saw the quoted form, so `"null"` arrives here as a
		// bare null -- which decodes into a nil slice with no error. Re-check what
		// the unwrap produced, not only what arrived.
		if strings.TrimSpace(string(itemsRaw)) == "null" {
			return nil, fmt.Errorf("%q is null", key)
		}
	}
	if err := json.Unmarshal(itemsRaw, &raws); err != nil {
		return nil, fmt.Errorf("%q is not an array: %w", key, err)
	}
	return raws, nil
}
