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
// One thing still has to be caught: a further answer that was TRYING to parse and
// failed, whether truncated by a token limit or malformed. That is the one case where
// shipping the empty first value loses content silently, and it is recognisable without
// classifying prose -- the unparsed remainder names the envelope key.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxResponseValues bounds how many top-level values are read out of one response.
// Real ones carry one or two; the cap is here so a pathological response cannot make
// this loop the expensive part of a run.
const maxResponseValues = 16

// jsonValues decodes the top-level JSON values in s, in order.
//
// tail is what was left when decoding stopped, empty when the input was consumed
// cleanly. firstErr is set only when nothing decoded at all, so a caller can report why
// a response was unreadable rather than inventing a reason.
func jsonValues(s string) (vals []json.RawMessage, tail string, firstErr error) {
	dec := json.NewDecoder(strings.NewReader(s))
	off := int64(0)
	for len(vals) < maxResponseValues {
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				return vals, "", nil
			}
			if len(vals) == 0 {
				firstErr = err
			}
			return vals, strings.TrimSpace(s[off:]), firstErr
		}
		vals = append(vals, v)
		off = dec.InputOffset()
	}
	return vals, strings.TrimSpace(s[off:]), nil
}

// tailAttemptsAnswer reports whether unparsed trailing text was trying to be another
// answer under key, rather than prose.
//
// Deliberately a single narrow test -- does the unparsed remainder name the envelope
// key -- and not an attempt to tell prose from JSON, which is what failed four times.
// An explanation of why an area is empty does not contain `"insights"`; a truncated or
// malformed second envelope does.
func tailAttemptsAnswer(tail, key string) bool {
	if tail == "" {
		return false
	}
	return strings.Contains(strings.ToLower(tail), `"`+strings.ToLower(key)+`"`)
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
	// tag -- some models capitalise it (`{"Insights":[…]}`).
	var itemsRaw json.RawMessage
	found := false
	for k, v := range envelope {
		if strings.EqualFold(k, key) {
			itemsRaw, found = v, true
			break
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
