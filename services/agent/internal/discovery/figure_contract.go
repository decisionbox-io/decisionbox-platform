package discovery

// figureContract asks the model to declare the arithmetic behind each number it
// writes.
//
// Appended in code rather than added to the domain packs for the reason the
// quantifier contract and the discipline rules are: a pack file can be edited, and
// a custom analysis area skips pack content entirely, so a contract that only
// exists in templates is one some areas do not have.
//
// What it asks for is mechanical -- name the step, the column, the rows. It does
// not ask the model to verify the figure, because three measured attempts at having
// Go find a figure's origin ran at 23% precision at best while the model that wrote
// it already knows which arithmetic it did.
//
// The two instructions that carry most of the value are the last two. "Write the
// figure to the precision you can defend" exists because the tolerance is read from
// the written text, so a model that writes 100,000 when it means 99,996 is claiming
// an interval it cannot support; rounding honestly is always available and always
// passes. "Declare it even when you are unsure" exists because the alternative to a
// refuted declaration is an undeclared figure, and an undeclared figure is checked
// by nothing.
const figureContract = "## Declaring the arithmetic behind each figure\n\n" +
	"Every number you write in `name`, `description` or `indicators` came from somewhere in the " +
	"evidence. Add an entry to a top-level `figure_claims` array on that insight naming the step and " +
	"the arithmetic. The platform re-runs that arithmetic over the step's **full** rows — which may be " +
	"more rows than the digest showed you — and tells you when the result is not the number you wrote.\n\n" +
	"```json\n" +
	"\"figure_claims\": [\n" +
	"  {\"figure\": \"$6.645B\", \"value\": 6645000000, \"step\": 28, \"kind\": \"cell\",\n" +
	"   \"column\": \"net_rev\", \"row\": \"material = 'TIN'\"},\n" +
	"  {\"figure\": \"911,395 lines\", \"value\": 911395, \"step\": 28, \"kind\": \"sum\", \"column\": \"lines\"},\n" +
	"  {\"figure\": \"99,996 buyers\", \"value\": 99996, \"step\": 7, \"kind\": \"sum\",\n" +
	"   \"column\": \"customers\", \"scope\": \"bucket != '0_never_ordered'\"},\n" +
	"  {\"figure\": \"five materials\", \"value\": 5, \"step\": 28, \"kind\": \"count\"},\n" +
	"  {\"figure\": \"20.1%\", \"value\": 20.1, \"step\": 28, \"kind\": \"ratio\", \"pct\": true,\n" +
	"   \"column\": \"net_rev\", \"row\": \"material = 'TIN'\"},\n" +
	"  {\"figure\": \"49.3% of fulfilled revenue\", \"value\": 49.3, \"step\": 5, \"kind\": \"ratio\", \"pct\": true,\n" +
	"   \"column\": \"net_revenue\", \"row\": \"l_returnflag = 'R' AND l_linestatus = 'F'\",\n" +
	"   \"scope\": \"l_linestatus = 'F'\"},\n" +
	"  {\"figure\": \"$50.8M apart\", \"value\": 50800000, \"step\": 28, \"kind\": \"diff\",\n" +
	"   \"column\": \"net_rev\", \"row\": \"material = 'TIN'\", \"other\": \"material = 'STEEL'\"}\n" +
	"]\n" +
	"```\n\n" +
	"- `kind` is one of:\n" +
	"  - `cell` — one cell. `column` plus `row`, which must select exactly **one** row.\n" +
	"  - `sum` — the total of `column`, over `scope` if you give one, otherwise every row.\n" +
	"  - `count` — how many rows are in `scope`, or in the whole result if you give none.\n" +
	"  - `ratio` — `column` in the row `row` names, divided by the total of `column` over `scope`. " +
	"Omit `scope` for a share of the whole column. Add `\"pct\": true` whenever you wrote it as a " +
	"percentage, or it will be compared against a fraction and refuted by a factor of a hundred.\n" +
	"  - `diff` — `column` in `row`'s row minus `column` in `other`'s row.\n" +
	"- `row`, `other` and `scope` use the same grammar as `filter` above: `column <op> literal` terms " +
	"joined by `AND`, with op one of `= != < <= > >=`. Nothing richer is read, and anything unreadable " +
	"is reported as undecidable rather than guessed at.\n" +
	"- `value` is the number in the units of the step's own column: write `6645000000`, not `6.645`; " +
	"write a percentage as `20.1`, not `0.201`.\n" +
	"- `figure` is the text as it appears in your prose, because a refutation has to name the words " +
	"that must change.\n\n" +
	"**Write each figure only as precisely as the evidence supports.** The check reads the interval " +
	"from how many places you wrote: `$6.645B` claims ±$500,000 and passes against 6,645,321,130, " +
	"while `100,000` claims ±0.5 and fails against 99,996. If a column sums to 99,996, write 99,996 " +
	"or write `~100,000` — never a bare `100,000`. Rounding honestly is always available; rounding to " +
	"a tidier number than the rows hold is the single commonest false figure in this pipeline.\n\n" +
	"**Declare a figure even when you are not sure the arithmetic is right.** A declaration that turns " +
	"out wrong comes back to you with the correct value and one chance to fix the sentence. A figure " +
	"you leave undeclared is checked by nothing and ships as written.\n\n" +
	"Skip only numbers that measure nothing: years, step numbers, and the boundaries of a bucket you " +
	"named yourself (`0–30 days`, `top 15`)."
