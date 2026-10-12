#!/usr/bin/env python3
"""Regenerate the embedded domain-pack seeds from domain-packs/.

The prompts that reach an install come from services/api/internal/handler/seed/*.json,
embedded into the API binary and written to MongoDB by SeedBuiltInPacks on every
startup. The authored sources live in domain-packs/<slug>/. Nothing kept the two
in step, so a prompt fix could land in the source tree and never ship: PR #211
("wire SQL dialect + dialect-correct table refs into exploration prompts") updated
the source prompts and the seeds kept the old hardcoded-BigQuery text for months.

Run after editing anything under domain-packs/:

    make regen-pack-seeds        # or: python3 scripts/regen-domain-pack-seeds.py

Derived from the source tree (overwritten):
    pack.prompts.base.<name>              <- prompts/base/<name>.md
    pack.prompts.categories.<cat>.<name>  <- prompts/categories/<cat>/<name>.md
    pack.analysis_areas.base              <- prompts/base/areas.json + its prompt_file bodies
    pack.analysis_areas.categories.<cat>  <- prompts/categories/<cat>/areas.json + bodies
    pack.profile_schema.base              <- profiles/schema.json
    pack.profile_schema.categories.<cat>  <- profiles/categories/<cat>.json

Preserved from the existing seed (not derivable from the source tree, which carries
no pack manifest): format, format_version, slug, name, description, version,
is_published, shape, categories.

A seed with no matching domain-packs/<slug>/ directory is left alone and reported —
digital-analytics is generated elsewhere and has no source here.
"""
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PACKS = ROOT / "domain-packs"
SEEDS = ROOT / "services/api/internal/handler/seed"

# Area metadata keys carried into the seed. `prompt_file` is deliberately not
# among them: the seed inlines the body as `prompt`, because the API serves
# packs from MongoDB and has no source tree to resolve a filename against.
AREA_KEYS = ("id", "name", "description", "keywords", "priority")


def read_areas(areas_path: pathlib.Path) -> list:
    """Load an areas.json and inline each area's prompt_file body as `prompt`."""
    if not areas_path.exists():
        return []
    out = []
    for area in json.loads(areas_path.read_text()):
        entry = {k: area[k] for k in AREA_KEYS if k in area}
        body = areas_path.parent / area.get("prompt_file", "")
        if area.get("prompt_file"):
            if not body.exists():
                sys.exit(f"{areas_path}: area {area.get('id')!r} names a missing prompt_file {body.name!r}")
            entry["prompt"] = body.read_text()
        out.append(entry)
    return out


def read_prompts(dir_path: pathlib.Path, areas_path: pathlib.Path) -> dict:
    """Every .md in dir_path that is NOT an area body, keyed by basename.

    Area bodies belong to analysis_areas; what is left is the pack's own prompt
    set (base_context, exploration, recommendations, exploration_context, ...).
    Splitting on "is it referenced by areas.json" means a newly added prompt
    file is picked up without touching this script.
    """
    if not dir_path.exists():
        return {}
    area_files = set()
    if areas_path.exists():
        for area in json.loads(areas_path.read_text()):
            if area.get("prompt_file"):
                area_files.add(area["prompt_file"])
    return {
        md.stem: md.read_text()
        for md in sorted(dir_path.glob("*.md"))
        if md.name not in area_files
    }


def regen(seed_path: pathlib.Path) -> bool:
    doc = json.loads(seed_path.read_text())
    pack = doc["pack"]
    src = PACKS / pack["slug"]
    if not src.is_dir():
        print(f"  {pack['slug']:<18} SKIP — no domain-packs/{pack['slug']}/ (generated elsewhere)")
        return False

    base_areas = src / "prompts/base/areas.json"
    prompts = {"base": read_prompts(src / "prompts/base", base_areas)}
    areas = {"base": read_areas(base_areas)}

    cat_prompts, cat_areas, cat_profiles = {}, {}, {}
    cat_root = src / "prompts/categories"
    if cat_root.is_dir():
        for cat_dir in sorted(d for d in cat_root.iterdir() if d.is_dir()):
            cat_areas_path = cat_dir / "areas.json"
            got = read_prompts(cat_dir, cat_areas_path)
            if got:
                cat_prompts[cat_dir.name] = got
            got_areas = read_areas(cat_areas_path)
            if got_areas:
                cat_areas[cat_dir.name] = got_areas

    profile_dir = src / "profiles/categories"
    if profile_dir.is_dir():
        for f in sorted(profile_dir.glob("*.json")):
            cat_profiles[f.stem] = json.loads(f.read_text())

    # Overwrite only what the source tree actually supplies, and otherwise
    # keep what the seed already had. A source tree with no
    # profiles/categories/ means "this script cannot speak to that field",
    # not "that field is empty" — deleting it would make regeneration lossy,
    # which is exactly the property a generator must not have.
    prompts["categories"] = cat_prompts or pack.get("prompts", {}).get("categories", {})
    areas["categories"] = cat_areas or pack.get("analysis_areas", {}).get("categories", {})

    profile_schema = dict(pack.get("profile_schema", {}))
    base_profile = src / "profiles/schema.json"
    if base_profile.exists():
        profile_schema["base"] = json.loads(base_profile.read_text())
    if cat_profiles:
        profile_schema["categories"] = cat_profiles

    pack["prompts"] = prompts
    pack["analysis_areas"] = areas
    if profile_schema:
        pack["profile_schema"] = profile_schema

    # Match the existing files byte-for-byte in style: 2-space indent, real
    # UTF-8 (these prompts are full of em dashes and arrows), no trailing
    # newline. Anything else turns the diff into the whole file.
    rendered = json.dumps(doc, indent=2, ensure_ascii=False)
    changed = rendered != seed_path.read_text()
    seed_path.write_text(rendered)
    print(f"  {pack['slug']:<18} {'updated' if changed else 'unchanged'}")
    return changed


def main() -> int:
    if not SEEDS.is_dir():
        sys.exit(f"missing {SEEDS}")
    print("regenerating embedded domain-pack seeds from domain-packs/")
    any_changed = False
    for seed in sorted(SEEDS.glob("*.json")):
        any_changed |= regen(seed)
    if any_changed:
        print("\nSeeds changed — commit them, or the fix does not ship.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
