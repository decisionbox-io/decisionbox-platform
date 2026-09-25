package discovery

// digestLegend explains the query_result digest to the model reading it.
//
// Every field it names was already in the prompt, unexplained. Nothing in the
// domain packs or the prompt assembly said what the digest was, so its fields
// read as facts about the data rather than facts about the result: a reader
// shown `distinct: 15` beside a `GROUP BY p_type ... LIMIT 15` query wrote
// "p_type has 15 values" for a column holding 150, and a reader shown a
// twelve-row lowest-profit list wrote "12 Products Each Loss-Making" where the
// true count was 302.
//
// Worded as instructions rather than definitions for the same reason
// CaveatInstruction is: a description the model reads and does not act on
// changes nothing, because the rows are well-formed either way.
const digestLegend = "## Reading `query_result`\n\n" +
	"`query_result` is a **digest** of what the query returned, not the rows themselves.\n\n" +
	"- `row_count` — rows the query returned. If the query carried a row cap (`LIMIT`, `TOP`, " +
	"`FETCH FIRST`) and `row_count` equals it, you are looking at a top-N view and the population " +
	"is larger by an unknown amount.\n" +
	"- `all_rows` — every row, present only when the result was small enough to carry whole. " +
	"Column statistics are omitted when it is present: the rows are all here, so count, rank and " +
	"total them directly.\n" +
	"- `head_rows` / `tail_rows` — the first and last few rows of a larger result. The middle is " +
	"not shown, so no row you cannot see may be ranked, named or counted.\n" +
	"- `distinct_in_result` — distinct values **among the rows this result returned**. It is not " +
	"the column's cardinality. Never restate it as one.\n" +
	"- `quality_caveats` — what is wrong with this result. Scope every claim drawn from a " +
	"caveated step to the rows it actually contains.\n\n" +
	"A count, share, rank or \"only / largest / every\" claim is about the population, so it needs " +
	"a step that saw the population. Say which step that was, or scope the claim to the rows you have.\n\n"
