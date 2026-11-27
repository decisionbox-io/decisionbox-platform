import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// The same findings are listed in two places: the project's insights page and
// the page for the run that produced them. A reader moving between them is
// looking at one set of documents, so a column that exists on one and not the
// other is not a difference in emphasis — it is one of the two lists quietly
// withholding something.
//
// This is a source assertion rather than a render, deliberately: the defect it
// guards was not a component behaving wrongly but a component never being
// called. The business-risk chips were added to the project list when the
// weighting work landed and not to the run page, so for one release the kind
// of risk a finding carried was visible on one screen and absent on the other
// — 20 chips against 0, on the same twenty findings. Nothing failed; the
// chips' own tests passed, because they were about the chips.
describe('both insight lists carry the same per-finding chips', () => {
  const pages = {
    'project insights': resolve(process.cwd(), 'src/app/projects/[id]/insights/page.tsx'),
    'discovery run': resolve(process.cwd(), 'src/app/projects/[id]/discoveries/[runId]/page.tsx'),
  };

  it.each(Object.entries(pages))('%s reads the file it is asserting against', (_name, path) => {
    expect(readFileSync(path, 'utf8')).toContain('AreaBadge');
  });

  // Wired to the row, not merely present: `RiskFlagChips` renders null when it
  // has no scoring, so a list could carry the tag and still show nothing. The
  // chips' own rendering -- including that `labels="ids"` quotes the category
  // verbatim -- is covered in RiskConfig.test.tsx; what cannot be covered
  // there is whether a page calls it at all, which is the defect this guards.
  it.each(Object.entries(pages))('%s renders the business-risk flags from the row', (_name, path) => {
    const src = readFileSync(path, 'utf8');
    const chip = src.slice(src.indexOf('<RiskFlagChips'));
    const props = chip.slice(0, chip.indexOf('/>'));
    expect(props).toContain('<RiskFlagChips');
    expect(props).toMatch(/scoring=\{[A-Za-z]+\.evidence_scoring\}/);
  });

  // And reads them the same way. A list is where the chip sits beside the
  // analysis-area id, so both quote the category as authored rather than one
  // shouting a humanised form the other does not.
  it.each(Object.entries(pages))('%s quotes the category id rather than humanising it', (_name, path) => {
    const src = readFileSync(path, 'utf8');
    const chip = src.slice(src.indexOf('<RiskFlagChips'));
    expect(chip.slice(0, chip.indexOf('/>'))).toContain('labels="ids"');
  });
});
