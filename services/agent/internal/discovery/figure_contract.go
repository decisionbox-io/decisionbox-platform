package discovery

// figureContract asks the model to emit every number as data and reference it from the
// prose, instead of typing it into the sentence.
//
// Appended in code rather than added to the domain packs for the reason the quantifier
// contract and the discipline rules are: a pack file can be edited, and a custom analysis
// area skips pack content entirely, so a contract that only exists in templates is one some
// areas do not have.
//
// The instruction that carries the most weight is the first: never type a number.
// Everything this design buys -- a precision that is known rather than parsed, a correction
// that is a field assignment, a link between prose and evidence that is exact rather than
// inferred at 1% tolerance -- holds only for figures that went through `figures`. A number
// typed into a sentence is a number nothing checks, and it looks identical to a sound one.
//
// Every rule that remains traces to a measured failure, which is the test each one had to
// pass to survive the shortening that produced this version:
//
//   - the ratio bullet says "the quotient, not the excess" because the earlier wording
//     offered *3.5% above the lowest* as an example of what `ratio` declares. evalFigure
//     computes 100*numerator/denominator, so a model following that example on 103.5 against
//     100 declares the correct 3.5% and is refuted by arithmetic that answers 103.5%. A
//     contract that advertises an operation the evaluator does not implement is a false
//     refutation generator, which is the one failure mode this whole layer exists to avoid.
//   - "never a word" is why a days unit no longer ships "180.1 days days".
//   - decimals-as-precision is the whole check: the interval is half the last place Go
//     printed, which separated "$6.645B" against its cell (true) from "100,000" against
//     99,996 (false) where no relative band could.
//   - approx not loosening the check is what stops a tilde being a way to make a figure
//     unrefutable.
//   - declaring an uncertain figure is what makes the correction pass reachable at all.
//
// The one clause that is new rather than kept: an indicator with no number in it needs no
// declaration. Three of thirteen insights in the first clean run emitted no indicators at
// all, against four per insight before the contract existed, and "never type a number"
// read as "say nothing" is the likeliest reason.
const figureContract = "## Every number is data, not text\n\n" +
	"**Never type a number into `name`, `description` or `indicators`.** A number in a " +
	"sentence rather than in `figures` is checked by nothing and reads exactly like one that " +
	"was checked. Declare each one in a top-level `figures` array and put a reference in the " +
	"prose; the platform renders it into the text a reader sees, after re-running your " +
	"arithmetic over the step's **full** rows — which may be more rows than the digest " +
	"showed you.\n\n" +
	"Years are the only exception: a period is prose (\"in 1997\", \"across 1992-1997\"), not a " +
	"measurement. And an indicator with no number in it needs no declaration — write it.\n\n" +
	"```json\n" +
	"\"name\": \"Top decile of customers generates {{f1}} of 1997 revenue\",\n" +
	"\"description\": \"The top decile contributed {{f2}} of {{f3}} in 1997.\",\n" +
	"\"figures\": [\n" +
	"  {\"id\": \"f1\", \"value\": 24.66, \"unit\": \"percent\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"ratio\", \"column\": \"revenue\", \"row\": \"decile = 1\"},\n" +
	"  {\"id\": \"f2\", \"value\": 8476238553, \"unit\": \"currency\", \"scale\": \"billions\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"cell\", \"column\": \"revenue\", \"row\": \"decile = 1\"},\n" +
	"  {\"id\": \"f3\", \"value\": 34373633413, \"unit\": \"currency\", \"scale\": \"billions\", \"decimals\": 2,\n" +
	"   \"step\": 48, \"kind\": \"sum\", \"column\": \"revenue\"}\n" +
	"]\n" +
	"```\n\n" +
	"Each figure's `id` is a letter followed by letters, digits or underscores — `f1`, `f12`, " +
	"`rev_share`. The prose reference must match it exactly.\n\n" +
	"### The arithmetic\n\n" +
	"- `cell` — one cell: `column` plus `row`, which must select exactly **one** row.\n" +
	"- `sum` — the total of `column`, over `scope` if given, otherwise every row.\n" +
	"- `count` — how many rows are in `scope`, or in the whole result if you give none.\n" +
	"- `ratio` — `column` in `row` over the same column in `other`; or, with no `other`, over " +
	"the total of `column` across `scope`. The first form is one row against another — *4.8x " +
	"the lowest band*; the second is a share of the whole column. Either way it is the " +
	"quotient, **not the excess**: for 103.5 against 100 it is `103.5%`, and if you want to " +
	"say *3.5% higher* then write that in the sentence and declare the two amounts.\n" +
	"- `diff` — `column` in `row` minus `column` in `other`. Both required.\n\n" +
	"`row`, `other` and `scope` use the same grammar as `filter` above: `column <op> literal` " +
	"terms joined by `AND`, op one of `= != < <= > >=`. Nothing richer is read, and anything " +
	"unreadable is reported as undecidable rather than guessed at.\n\n" +
	"`value` is in the units of the step's own column: `8476238553`, not `8.48`. Write a " +
	"percentage as `24.66`, not `0.2466` — whichever way the column stores it.\n\n" +
	"### How it is written\n\n" +
	"- `unit` is `count`, `currency`, `percent`, `multiple` or `plain`. A unit is part of the " +
	"number's notation — a currency symbol, a percent sign, thousands separators — **never a " +
	"word**. Write *days*, *orders*, *lines* in the sentence: `{{f1}} days`.\n" +
	"- `currency` is the symbol a currency figure prints. Omit it for dollars; set it when " +
	"the data is not in dollars — `{\"unit\": \"currency\", \"currency\": \"€\"}` writes `€8.48B`.\n" +
	"- `scale` is `thousands`, `millions` or `billions` to abbreviate; omit it to write the " +
	"number in full with thousands separators.\n" +
	"- `decimals` is how many places to print, and **it is the precision you are claiming.** " +
	"8476238553 at `billions` with 2 decimals renders `$8.48B` and asserts the arithmetic " +
	"lands within $5,000,000 of it; with 0 decimals it renders `$8B` and asserts far less. " +
	"Choose the places the evidence supports — never more.\n" +
	"- `approx: true` prints a tilde (`~911K`). It marks the number as rounded for the reader " +
	"and does not loosen the check, so it is not a way to make a figure unrefutable.\n\n" +
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
