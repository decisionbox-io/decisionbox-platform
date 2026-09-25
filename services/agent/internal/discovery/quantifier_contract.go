package discovery

// quantifierContract asks the model to declare what each of its quantifier
// statements rests on.
//
// It is appended here rather than added to the domain packs' output-format
// sections for the reason the discipline rules live in code: a pack file can be
// edited, and a custom analysis area skips pack content entirely, so a contract
// that only exists in templates is a contract some areas do not have.
//
// What it asks for is mechanical — name the step, the column, the predicate.
// It does not ask the model to verify anything, because verifying is what it
// demonstrably cannot do: the claim this exists for was written with all
// seventeen relevant rows inline and correct, from a table ordered by profit
// while the claim ranked on sales.
const quantifierContract = "## Declaring quantifier claims\n\n" +
	"A **quantifier claim** is any statement whose truth depends on rows besides the ones it names: " +
	"*only*, *every*, *all*, *largest*, *lowest*, *second largest*, *top N*, *improved each year*, " +
	"*has N values*, *more than*.\n\n" +
	"For every such statement you write in `name`, `description` or `indicators`, add an entry to a " +
	"top-level `quantifier_claims` array on that insight. You are declaring what the claim rests on, " +
	"not proving it — the platform evaluates the predicate over the step's full rows and will tell you " +
	"if it does not hold.\n\n" +
	"```json\n" +
	"\"quantifier_claims\": [\n" +
	"  {\"claim\": \"the only top-10 revenue line running a loss\", \"kind\": \"only\",\n" +
	"   \"step\": 4, \"filter\": \"profit < 0\", \"top_n\": 10, \"top_n_column\": \"sales\"},\n" +
	"  {\"claim\": \"Chairs is the largest sub-category by sales\", \"kind\": \"rank\",\n" +
	"   \"step\": 4, \"column\": \"sales\", \"subject\": \"sub_category = 'Chairs'\", \"rank\": 1},\n" +
	"  {\"claim\": \"margin improved each year\", \"kind\": \"monotonic\",\n" +
	"   \"step\": 7, \"column\": \"margin\", \"trend\": \"increasing\"},\n" +
	"  {\"claim\": \"3 of 17 sub-categories run a loss\", \"kind\": \"cardinality\",\n" +
	"   \"step\": 4, \"filter\": \"profit < 0\", \"count\": 3},\n" +
	"  {\"claim\": \"the West region grew in every quarter of 2025\", \"kind\": \"all\",\n" +
	"   \"step\": 19, \"scope\": \"region = 'West' AND yr = 2025\", \"filter\": \"growth > 0\"}\n" +
	"]\n" +
	"```\n\n" +
	"- `kind` is `only` (exactly one row), `all` (every row), `rank`, `monotonic` (a trend) or " +
	"`cardinality` (a count). *Every year X* is `all`, not `monotonic` — `monotonic` asserts a " +
	"direction of change, which is a different claim and is usually false when `all` is true.\n" +
	"- `filter` is a conjunction of `column <op> literal` terms joined by `AND`, with op one of " +
	"`= != < <= > >=`. Nothing richer is evaluated.\n" +
	"- `scope` narrows **which rows** the claim is about, in the same grammar as `filter`. Use it whenever " +
	"the step holds more series than the sentence names. A step grouped by two keys holds one series per " +
	"combination, so a claim about one of them without a `scope` is read as a claim about all of them, and " +
	"is usually false. Put every bound the sentence states into it — the entity it names, and any period " +
	"or category it limits itself to, even when the step reaches past that period.\n" +
	"- `top_n` / `top_n_column` narrow to the largest few, after `scope`. Use them whenever the " +
	"claim ranks on one column and filters on another — that combination is where these claims go wrong.\n" +
	"- `subject` names the **single row** a `rank` claim is about, in the same grammar as `filter`. Where the " +
	"step is grouped by two keys it takes both — `\"sub_category = 'Tables' AND yr = 2026\"`, not just the " +
	"sub-category, which selects one row per year and settles nothing.\n" +
	"- `order` is `desc` (default) or `asc`; rank 1 is the largest under `desc`, the smallest under `asc`.\n\n" +
	"If a claim rests on a step whose `quality_caveats` say the result was capped, scope it to the " +
	"rows that step returned and say so with `top_n` — \"the only loss-making line among the 10 " +
	"largest by sales\" is settleable against a step that returned exactly those 10. A claim over a " +
	"capped step that reaches beyond the returned rows cannot be settled at all; drop it or rewrite " +
	"it as one that can.\n\n"
