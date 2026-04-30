# Pull Requests

> **Version**: 0.1.0

Guidelines for contributing code to DecisionBox.

## Before You Start

1. **Check existing issues** — Someone may already be working on it
2. **Open an issue first** — For new features, discuss the approach before coding
3. **Small PRs are better** — One feature or fix per PR

## Development Workflow

```bash
# Fork the repository on GitHub, then:
git clone https://github.com/YOUR-USERNAME/decisionbox-platform.git
cd decisionbox-platform
git remote add upstream https://github.com/decisionbox-io/decisionbox-platform.git

# Create a branch
git checkout -b feature/my-feature

# Make changes, test, commit
make test-go
git add .
git commit -m "feat: add snowflake warehouse provider"

# Push and create PR
git push origin feature/my-feature
```

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>

<body>
```

**Types:**
- `feat` — New feature
- `fix` — Bug fix
- `docs` — Documentation only
- `test` — Adding or updating tests
- `refactor` — Code restructuring (no behavior change)
- `chore` — Build, CI, config changes

**Scopes** (optional):
- `agent` — Agent service
- `api` — API service
- `ui` — Dashboard
- `llm` — LLM providers
- `warehouse` — Warehouse providers
- `secrets` — Secret providers
- `domain-packs` — Domain packs

**Examples:**
```
feat(warehouse): add Snowflake provider
fix(agent): handle LLM timeout during analysis phase
docs: add Snowflake configuration guide
test(llm): add Claude integration tests with error paths
```

## PR Requirements

### Must Have

- [ ] **Tests** — Unit tests for new logic. Integration tests for external services.
- [ ] **Builds** — `make build` succeeds. `make test-go` passes.
- [ ] **Lint** — `make lint` passes (golangci-lint + ESLint).
- [ ] **No hardcoded values** — Use config, env vars, or domain pack files.
- [ ] **Documentation** — Update docs if the change affects user-facing behavior.

### For Provider PRs

- [ ] Provider registered via `init()` with `RegisterWithMeta()`
- [ ] ConfigFields defined for dashboard form rendering
- [ ] LLM providers: `ProviderMeta.Models` populated with `Wire`, `MaxOutputTokens`, `Pricing`, plus `Aliases` covering every cross-region / suffix / short-form variant; `DefaultMaxOutputTokens` set
- [ ] Warehouse providers: `DefaultPricing` set
- [ ] Imported in both `services/agent/main.go` and `services/api/main.go`
- [ ] `replace` directive in both service go.mod files
- [ ] Dockerfile COPY line for go.mod/go.sum
- [ ] Added to Makefile test targets
- [ ] Unit tests (registration, config validation)
- [ ] Integration tests (skip without credentials)

### For Domain Pack PRs

- [ ] areas.json with proper field structure
- [ ] All prompt files referenced in areas.json exist
- [ ] base_context.md includes `{{PROFILE}}` and `{{PREVIOUS_CONTEXT}}`
- [ ] Analysis prompts include `{{QUERY_RESULTS}}`
- [ ] Recommendations prompt includes `related_insight_ids` instruction
- [ ] Profile schema is valid JSON Schema (draft 2020-12)
- [ ] Go implementation with tests
- [ ] Registered in both services

## PR Template

```markdown
## Summary
Brief description of what this PR does.

## Changes
- Added X
- Fixed Y
- Updated Z

## Testing
How this was tested:
- [ ] Unit tests added/updated
- [ ] Integration tests pass
- [ ] Manual testing done

## Documentation
- [ ] Docs updated (if user-facing change)
- [ ] README updated (if applicable)
```

## Code Style

### Go

- Standard `gofmt` formatting
- No unused imports or variables
- Error messages: lowercase, no period (e.g., `"failed to create provider"`)
- Structured logging with `apilog` or `applog` (never `fmt.Println`)
- Context passed as first argument

### TypeScript

- ESLint rules from Next.js config
- Functional components with hooks
- Types in `lib/api.ts`

### Markdown (docs)

- One sentence per line (for better diffs)
- Code blocks with language tag (```go, ```bash, ```json)
- Headers in title case

## Review Process

1. CI must pass (build, tests, and lint)
2. At least one maintainer review
3. No merge conflicts with main
4. Squash merge (clean history)

## Next Steps

- [Development Setup](development.md) — Local environment
- [Testing](testing.md) — Running and writing tests
