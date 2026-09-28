package discovery

// figureContract asks the model to emit every number as data and reference it from the
// prose, instead of typing it into the sentence.
//
// Appended in code rather than added to the domain packs for the reason the quantifier
// contract and the discipline rules are: a pack file can be edited, and a custom analysis
// area skips pack content entirely, so a contract that only exists in templates is one some
// areas do not have.
//
// The instruction that carries the most weight is the last: never type a number. Everything
// this design buys -- a precision that is known rather than parsed, a correction that is a
// field assignment, a link between prose and evidence that is exact rather than inferred at
// 1% tolerance -- holds only for figures that went through `figures`. A number typed into a
// sentence is a number nothing checks, and it looks identical to a sound one.
const figureContract = "## Every number is data, not text\n\n" +
	"Do not type numbers into `name`, `description` or `indicators`. Declare each one in a " +
	"top-level `figures` array and put a reference in the prose. The platform renders the " +
	"reference into the text a reader sees, after re-running your arithmetic over the step's " +
	"**full** rows — which may be more rows than the digest showed you.\n\n" +
	"```json\n" +
	"\"name\": \"Top decile of customers generates {{f1}} of 1997 revenue\",\n" +
	"\"description\": \"The top decile contributed {{f1}} ({{f2}} of {{f3}}) across {{f4}} buyers.\",\n" +
	"\"figures\": [\n" +
	"  {\"id\": \"f1\", \"value\": 24.66, \"unit\": \"percent\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"ratio\", \"column\": \"revenue\", \"row\": \"decile = 1\"},\n" +
	"  {\"id\": \"f2\", \"value\": 8476238553, \"unit\": \"currency\", \"scale\": \"billions\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"cell\", \"column\": \"revenue\", \"row\": \"decile = 1\"},\n" +
	"  {\"id\": \"f3\", \"value\": 34373633413, \"unit\": \"currency\", \"scale\": \"billions\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"sum\", \"column\": \"revenue\"},\n" +
	"  {\"id\": \"f4\", \"value\": 8668, \"unit\": \"count\",\n" +
	"   \"step\": 48, \"kind\": \"cell\", \"column\": \"customers\", \"row\": \"decile = 1\"}\n" +
	"]\n" +
	"```\n\n" +
	"### The arithmetic\n\n" +
	"- `kind` is one of:\n" +
	"  - `cell` — one cell. `column` plus `row`, which must select exactly **one** row.\n" +
	"  - `sum` — the total of `column`, over `scope` if you give one, otherwise every row.\n" +
	"  - `count` — how many rows are in `scope`, or in the whole result if you give none.\n" +
	"  - `ratio` — `column` in `row`, divided by **either** the same column in `other`, **or**, " +
	"when you give no `other`, the total of `column` over `scope`. The first form is how you " +
	"declare a spread, a multiple or one row against another — *4.8x more often*, *3.5% above " +
	"the lowest*. The second is a share of the whole column.\n" +
	"  - `diff` — `column` in `row` minus `column` in `other`. Both selectors are required.\n" +
	"- `row`, `other` and `scope` use the same grammar as `filter` above: `column <op> literal` " +
	"terms joined by `AND`, with op one of `= != < <= > >=`. Nothing richer is read, and " +
	"anything unreadable is reported as undecidable rather than guessed at.\n" +
	"- `value` is the number in the units of the step's own column: `8476238553`, not `8.48`. " +
	"Write a percentage as `24.66`, not `0.2466` — whichever way the column stores it.\n\n" +
	"### How it is written\n\n" +
	"- `unit` is `count`, `currency`, `percent`, `multiple`, `days` or `plain`.\n" +
	"- `scale` is `thousands`, `millions` or `billions` to abbreviate a large number; omit it " +
	"to write the number in full with thousands separators.\n" +
	"- `decimals` is how many decimal places to print. **This is the precision you are " +
	"claiming.** `value` 8476238553 at `billions` with 2 decimals renders `$8.48B` and asserts " +
	"the arithmetic lands within $5,000,000 of it; the same value with 0 decimals renders `$8B` " +
	"and asserts far less. Choose the places the evidence supports — never more.\n" +
	"- `approx: true` prints a tilde (`~911K`). It marks the number as rounded for the reader " +
	"and does not loosen the check, so it is not a way to make a figure unrefutable.\n\n" +
	"### Two rules\n\n" +
	"**Never type a number into the prose.** A number in a sentence rather than in `figures` is " +
	"checked by nothing and reads exactly like one that was checked. Years are the only " +
	"exception — a period is prose (\"in 1997\", \"across 1992-1997\"), not a measurement.\n\n" +
	"**Declare a figure even when you are unsure the arithmetic is right.** A declaration that " +
	"turns out wrong is corrected for you, from your own evidence, before anyone reads it. A " +
	"number you type instead ships as written."
