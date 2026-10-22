package discovery

// Every string the recommendation schema asks the model for carries a maximum
// length.
//
// The schemas these bounds apply to are sent as decode-time grammars where the
// provider supports it, and a grammar constrains *structure*. It does not
// constrain how long a string may be: `{"type":"string"}` admits a string of
// any length, so an unbounded field is an unbounded legal walk, and a model
// that starts repeating inside one never has to come out.
//
// That is not hypothetical. On a self-hosted 27B model the recommendation
// phase produced, three runs in a row, a response shaped like
//
//	{"recommendations":[{"title":"<25,000 characters>
//
// -- one title string, never closed, the whole output budget spent inside it,
// 17 to 23 quote characters in 26,000. The grammar was being enforced the
// whole time and permitted every token of it. Measured against the same model
// with a length bound on the string fields, the identical request completed in
// 306 output tokens with a clean stop.
//
// The bounds below are deliberately far above anything a working model has
// produced on this pipeline -- measured over the 91 recommendations it has
// written, every bound sits at three to eight times the largest observed
// value. They exist to stop a runaway, never to shape a result, and a bound
// that ever binds on a real answer is a bug in the bound.
//
// Anything added here should be measured the same way rather than guessed.
//
// SCOPE, deliberately narrow. The insight, clarifying-question and reflection
// schemas describe unbounded strings through the same mechanism and are not
// bounded here: the recommendation phase is the one with a reproduction, and
// those three are producing results today, so bounding them is a regression
// risk taken for a failure that has not been observed in them. They each still
// carry their own `str := func(desc string)` helper, which is the pattern to
// avoid copying -- see the follow-up issue referenced in
// schema_strings_test.go.
const (
	// A name or title: a line of prose. Observed maximum 109.
	maxSchemaTitleRunes = 400

	// A description: the long prose field, the one that ran away. Observed
	// maximum 1,191, and held under grammarRepetitionCeiling, which is why
	// this is the one bound with less than twice the headover the evidence.
	maxSchemaDescriptionRunes = 2000

	// A sentence or two of supporting prose -- a rationale, a reason, a
	// learning, an impact's reasoning. Observed maximum 188.
	maxSchemaSentenceRunes = 1500

	// One item of a list the model writes: an implementation step, an
	// indicator, an option label. Observed maximum 178 (an action).
	maxSchemaListItemRunes = 600

	// A segment definition -- measurable criteria, longer than a label.
	// Observed maximum 194.
	maxSchemaSegmentRunes = 800

	// A short free-text label the model picks: a category, a severity, a
	// unit, a currency symbol, a scale word, a tag. Observed maximum 63.
	maxSchemaLabelRunes = 200

	// An identifier the model copies rather than composes: a UUID, a figure
	// id, a table name, a slug. A UUID is 36; a fully-qualified table name is
	// the long case.
	maxSchemaIdentifierRunes = 256
)

// grammarRepetitionCeiling is the largest bound that survives translation into
// a llama.cpp grammar, and therefore the largest bound that does anything on an
// Ollama-backed deployment.
//
// A maxLength becomes a repetition there: json-schema-to-grammar.cpp emits
// `build_repetition(char, minLength, maxLength)`, i.e. `char{0,N}`. llama.cpp's
// grammar parser then applies MAX_REPETITION_THRESHOLD = 2000 to it, and the
// behaviour for an upper bound is not an error:
//
//	if (max_times != UINT64_MAX && max_times > MAX_REPETITION_THRESHOLD) {
//	    max_times = UINT64_MAX;
//	}
//
// -- the bound is clamped back to unbounded, before handle_repetitions is
// called, so nothing throws and nothing warns. A bound above this ceiling is
// therefore not a stricter protection on that backend; it is no protection at
// all, silently. (The throw in that file applies to a *minimum* repetition
// above the threshold, which no bound here produces.)
//
// Checked by TestSchemaBoundsSurviveGrammarTranslation, because a bound raised
// later would otherwise stop applying on Ollama without a single symptom.
const grammarRepetitionCeiling = 2000

// boundedString returns a JSON-Schema string field with a length ceiling.
//
// There is deliberately no unbounded variant in this package. Every schema
// used to carry its own `str := func(desc string)` helper, and that is how four
// schemas came to describe strings a grammar would let run forever: the
// unbounded form was the convenient one. Requiring the bound as an argument
// makes the omission a compile error rather than an oversight.
func boundedString(desc string, maxRunes int) map[string]interface{} {
	return map[string]interface{}{
		"type":        "string",
		"description": desc,
		"maxLength":   maxRunes,
	}
}

// nonEmptyString is boundedString for a field the schema also requires.
//
// `required` makes a key present and says nothing about its value, so a
// grammar satisfies a required string with "". That is not a hypothetical
// either: the run that first completed under these bounds wrote a title and
// then closed the object, and every field it skipped persisted as the zero
// value -- which reads downstream exactly like the absent field `required`
// exists to prevent.
//
// A minimum of one, not a measured floor. The point is only that the model
// cannot satisfy the grammar by opening the field and closing it; how much it
// writes is the prompt's business. One repetition is also far below the
// threshold that makes a *minimum* throw in a llama.cpp grammar, which an
// ambitious floor here would risk -- see grammarRepetitionCeiling.
func nonEmptyString(desc string, maxRunes int) map[string]interface{} {
	f := boundedString(desc, maxRunes)
	f["minLength"] = 1
	return f
}
