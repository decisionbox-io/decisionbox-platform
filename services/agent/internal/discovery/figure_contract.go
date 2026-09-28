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
	"- `unit` is `count`, `currency`, `percent`, `multiple` or `plain`. A unit is part of the " +
	"number's notation — a currency symbol, a percent sign, thousands separators — never a word. " +
	"Write words like *days*, *orders* or *lines* in the sentence: `{{f1}} days`, not a days unit.\n" +
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

// recommendationFigureContract asks a recommendation to reference the insights' figures
// rather than retype their numbers.
//
// Much shorter than the insight contract, because it asks for much less. An insight
// declares arithmetic over warehouse rows and needs the grammar for it; a recommendation
// is handed the insights and nothing else, so every number it reports is already a checked
// figure somewhere in its input. Naming one is the whole instruction.
//
// The scope is measured rather than assumed. Across four adjudicated runs 602 of 638
// numerals in recommendation prose restated a number an insight had already stated, and of
// eleven false recommendation claims ten were inherited verbatim from a false insight --
// which is fixed at the insight, not here. The single remaining one was a total over
// insight figures that matched no combination of them, in a headline its own body
// contradicted. That is what `sum` is for, and it is why there is no third kind.
const recommendationFigureContract = "## Numbers come from the insights, by reference\n\n" +
	"Every number you report is already in your input: an insight declared it and the " +
	"platform has checked it. Do not retype one. Put a reference in the prose and name the " +
	"figure in a top-level `figures` array — the platform writes the checked value into " +
	"`title`, `description` and `actions` before anyone reads them.\n\n" +
	"```json\n" +
	"\"title\": \"Win back the {{f1}} buyers in the 6+ order bands\",\n" +
	"\"description\": \"{{f1}} buyers sit in the two highest frequency bands ({{f2}} and {{f3}}).\",\n" +
	"\"figures\": [\n" +
	"  {\"id\": \"f1\", \"unit\": \"count\", \"kind\": \"sum\",\n" +
	"   \"refs\": [{\"insight\": \"<insight id>\", \"figure\": \"f4\"},\n" +
	"            {\"insight\": \"<insight id>\", \"figure\": \"f5\"}]},\n" +
	"  {\"id\": \"f2\", \"unit\": \"count\", \"kind\": \"ref\",\n" +
	"   \"refs\": [{\"insight\": \"<insight id>\", \"figure\": \"f4\"}]},\n" +
	"  {\"id\": \"f3\", \"unit\": \"count\", \"kind\": \"ref\",\n" +
	"   \"refs\": [{\"insight\": \"<insight id>\", \"figure\": \"f5\"}]}\n" +
	"]\n" +
	"```\n\n" +
	"- `kind` is `ref` to restate one insight figure, or `sum` to total several.\n" +
	"- `insight` is the insight's `id`, copied verbatim, the same way you copy it into " +
	"`related_insight_ids`. `figure` is the `id` inside that insight's own `figures` array.\n" +
	"- **You do not write the number.** There is no `value` field to fill: the platform " +
	"takes it from the figure you named. `unit`, `scale` and `decimals` are yours, so you " +
	"choose how it is written — `{\"unit\": \"currency\", \"scale\": \"millions\", " +
	"\"decimals\": 1}` writes `$8.5M` from the same figure the insight wrote in full.\n" +
	"- Reference the same figure everywhere you mention it. A title and a body that share " +
	"a reference cannot disagree; one measured run headlined 96,447 buyers while its own " +
	"body said 96,031.\n\n" +
	"### What stays as ordinary text\n\n" +
	"Two kinds of number are yours rather than the warehouse's, and they belong in the " +
	"prose exactly as you would write them:\n\n" +
	"- **What you project.** *\"a 3% conversion on those accounts is ~1,500 first orders\"*. " +
	"An estimate is not a measurement, and `expected_impact` is where it goes.\n" +
	"- **What you choose.** A threshold, a window, a band — *\"a 45-60 day reminder track\"*, " +
	"*\"deciles 1 to 3\"*. You are setting it, not reading it.\n\n" +
	"Everything else — every count, share, amount and average you report — is an insight's " +
	"figure, so reference it."
