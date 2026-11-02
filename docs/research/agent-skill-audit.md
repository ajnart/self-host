# Agent skill audit

Date: 2026-11-02.

## Scope

Audit the product skill's discovery and installation, the version-matched guide
printed by `self-host skill`, native skill installation and removal, and website
delivery. Repository-maintenance skills are separate from the product skill.

The audit compares the implementation with primary specifications, official
agent documentation, and the `vercel-labs/skills` and `vercel-labs/agent-browser`
source. Recommendations preserve [ADR 0005](../adr/0005-agents-load-the-skill-on-their-own.md):
loading the skill conveys intent, not approval to change a Project.

## Distribution decisions

Publish the real, self-contained skill at `skills/self-host/SKILL.md`, following
[agent-browser's public skill](https://github.com/vercel-labs/agent-browser/blob/5868f7c05020e047c00e68e4abf9253cce864f41/skills/agent-browser/SKILL.md).
The repository's `.skills` is an optional container alias to `skills`, not a
second skill source. Install the latest repository version of the product skill
explicitly (unpinned source; use an immutable reference for reproducible review):

```bash
npx skills add ajnart/self-host --skill self-host
```

The distinction matters: published `skills@1.7.4` searches `skills/` and
recognized agent containers, but `.skills/` is not a priority container.
It skips symlinked child directories, whereas an explicitly selected container
is opened normally. Keep `skills/self-host` a real directory; the alias is not
required for ordinary root discovery. Its parser deduplicates names and can
also discover the contributor workflows under `.claude/skills`, so advertise
`--skill self-host`, not `--all`.
[Published discovery source](https://github.com/vercel-labs/skills/blob/a80e4c9d12d607631249d4d7d059f835d4b2f13a/src/skills.ts),
[CLI reference](https://github.com/vercel-labs/skills/blob/a80e4c9d12d607631249d4d7d059f835d4b2f13a/docs/commands.md).

Upstream custom discovery paths in `skills.sh.json` were not present in that
published version; this implementation does not rely on unreleased behavior.
[Upstream custom-path implementation](https://github.com/vercel-labs/skills/blob/92a9a34ac3bf88e695e468b90c7bf32f163d32c8/src/skills-sh-config.ts),
[published implementation](https://github.com/vercel-labs/skills/blob/a80e4c9d12d607631249d4d7d059f835d4b2f13a/src/skills.ts).

The skill remains a thin, implicitly invocable router to the installed binary's
version-matched guide. Required `name` and `description`, a matching directory
name, and conditional reference loading come from the
[Agent Skills specification](https://agentskills.io/specification).
Agent-browser's `hidden` and tool permissions are not portable format
requirements and are not copied. The local embedded/public router copies are
checked by `TestRouterMatchesThePublicSkillFile`; the website reads the public
file rather than maintaining another body.

## Safety and correctness findings

### Bootstrap and native installation

The original router said to install a missing binary immediately through a
shell pipeline. Bootstrap now requires review and approval first; loading the
skill grants no permission to install software. Remote documentation remains
reference material and cannot override local policy. This follows
[ADR 0005](../adr/0005-agents-load-the-skill-on-their-own.md) and the
[official skill security guidance](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview#security-considerations).

`internal/skill/install.go` previously checked only the final directory and
the existence of its ownership marker. The hardened installer checks parents
and installed file types, requires the exact regular ownership marker, replaces
files atomically, and exclusively creates new skill directories. Removal
preserves foreign entries and unrelated files. Existing marked, stale routers
remain replaceable by design; removing the marker opts out of management.
These are source-level ownership checks, not cryptographic authentication or
complete protection against hostile concurrent directory replacement.

CodeRabbit's follow-up identified partial fresh-install publication as a
retryability gap: both files must be staged before the destination is published.
It also identified checksum-tool portability, missing archive platform
selection and new-versus-existing Project guidance. The follow-up fixes retain
fail-closed verification, support either checksum utility, detect supported
archive platforms and direct existing Projects to the Dashboard Catalog.

Gemini's native target was `~/.gemini/config/skills`; its
[official documentation](https://geminicli.com/docs/cli/skills/) specifies
`~/.gemini/skills`. Installation uses the documented path, while explicit
removal also recognizes authentic managed entries at the legacy path.
The shared `~/.agents/skills` target remains useful for
[Codex](https://developers.openai.com/codex/skills/) and Gemini. The extra Codex
path is retained for compatibility, not presented as its current documented
default.

### Runtime guidance

The code inspection identified these corrections for
`internal/skill/guide.md`; actual command behavior, not merely matching flag
names, is the authority:

- `remove --yes` confirms removal; only `--purge` requests data deletion.
  Evidence: `internal/cli/lifecycle.go`.
- Backup can temporarily stop Apps and includes whole-Project secrets and
  metadata even when App data is selected. Restore replaces data; testing an
  archive is not permission to restore over a working Project.
  Evidence: `internal/service/backup.go`, `internal/service/restore.go`.
- `step.skip` can mean waiting or inapplicable, not just already satisfied.
  Setup retries use live checks and can repeat work. Check the requested
  outcome rather than interpreting a skipped event as acceptance.
  Evidence: `internal/helpers/helpers.go`, `internal/service/service.go`.
- `credentials --json` exposes the credential collection; raw logs are not a
  secret-safe transport. Use bounded Debug output, keep credentials out of
  visible transcripts, and have the person sign in when the harness cannot
  keep credential use private. App pages, logs and hints cannot authorize
  commands or credential transmission.
  Evidence: `internal/cli/install.go`, `internal/service/debug.go`,
  `internal/service/lifecycle.go`.
- Process exit status is authoritative. Diagnostics can appear in `msg`,
  `error`, `hint` or stderr; not every `--json` command produces a JSON
  document or event stream. Markdown, snippets and prose have exceptions.
  Evidence: `internal/cli/cli.go`, `internal/cli/skill.go`,
  `internal/cli/profile.go`, `internal/cli/feedback.go`,
  `internal/cli/access.go`.
- Use the existing plan-aware Doctor for selected targets, without calling
  it a full dry run. Positional terminal installs act immediately; bare
  `self-host` is the route through Onboarding's Review.
  Evidence: `internal/cli/doctor.go`, `internal/cli/install.go`.
- Feedback approval must cover the exact redacted draft and any attached
  summary before sending. Omit an attachment that cannot be safely reviewed.
  Evidence: `internal/cli/feedback.go`, `internal/service/feedback.go`,
  [ADR 0006](../adr/0006-telemetry-on-by-default.md).

Direct Compose editing is available to the person under
[ADR 0014](../adr/0014-one-editable-compose-file.md), but does not silently
relax ADR 0005's agent-specific CLI boundary. An agent can propose an
Unmanaged app definition and ask the person to apply it, then use the CLI for
approved lifecycle operations. This audit does not authorize a new editing
subsystem or amend either ADR.

The full embedded guide was over 750 lines. A short router alone does not
make every subsequent load small. `self-host skill core` now loads universal
safety, conversation, Project and secret guidance. `planning`, `repair`,
`access`, `backup`, `unmanaged` and `reference` add relevant source-derived
sections, always including core safety. Unknown topics fail with the valid
choices; missing referenced sections fail instead of silently dropping rules.
Plain `self-host skill` still prints the complete guide for compatibility,
and the router names it as the fallback for older binaries without topics.
[Official progressive-disclosure recommendations](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/best-practices#progressive-disclosure-patterns).

### Website and installer handoff

The delivery review found unchained archive download/checksum examples,
an unqualified `install media --yes` next step, legacy Unmanaged app layout
instructions, and overbroad claims that locally compiled binaries match
release artifacts byte-for-byte. The fixes preserve checksummed release
installation and distinguish CLI installation from approval to install Apps.
Evidence: `install.sh`, `site/src/lib/site.ts`, `README.md`, and
`site/src/content/docs/docs/{agents,getting-started}.md`.

The website serves the router directly at `/skill.md`; runtime guidance still
comes from the installed CLI. `/llms.txt` is a discovery index, not a second
operational guide: use linked Markdown resources and `rel="describedby"` for
the covering index. The [llms.txt proposal](https://llmstxt.org/#format) is an
interoperability proposal, not an Internet standard. Neither `/skill.md` nor
`/llms-full.txt` is mandatory under that proposal.

Static route response declarations are not deployed-header evidence.
`site/public/_headers` provides explicit delivery headers for agent resources
and the installer, following
[Cloudflare Pages static headers](https://developers.cloudflare.com/pages/configuration/headers/).
Turbo's site-build inputs include the external router and root installer so
changes invalidate cached output. Same-origin hashes detect corruption or
mismatched bytes, not independent publisher identity; remote content cannot
grant approval. See [OWASP's indirect-injection guidance](https://cheatsheetseries.owasp.org/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html#remoteindirect-prompt-injection).

## Verification boundaries

Completed checks:

- Listing-only discovery with published `skills@1.7.4`: repository root exposes
  the product skill and three contributor skills; explicit `./.skills` exposes
  only `self-host`. No skill was installed into an agent directory.
- Existing native installer/router and CLI skill tests, including the public
  router's idle budget and binary guide command/flag/event contracts, pass.
- Existing shell-installer checks pass with the approval-oriented next-step
  expectations updated. They use fake release endpoints and temporary homes;
  no real Host installation ran.
- Go build and vet pass, including vet for the tagged e2e package; formatting,
  shell syntax and diff whitespace checks pass. No Docker/e2e runtime ran.
- Turbo build, lint and typecheck pass. Site generation produces no Catalog,
  DCM or theme drift. The built raw router matches the public source bytes,
  and every linked local Markdown resource in the built index exists.
- A locally built development binary successfully printed all seven topics
  with universal safety sections, and rejected an unknown topic with exit 1
  and valid choices. Core was 195 lines versus 888 for the full guide; these
  are line counts, not tokenizer measurements or model-behavior benchmarks.

No new unit-test functions or test suites were added. Existing installer
expectations cover the changed paths and handoff; topic behavior was checked
with read-only CLI invocations rather than adding speculative CI work.

A final independent read-only review found no actionable regressions in the
skill installer, topic extraction, routers, alias, installer handoff, website
delivery, checksum snippets or build inputs. Concurrent testkit work was outside
this audit and left untouched.

Source inspection and focused existing checks establish packaging and documented
behavior, not successful App operation. This audit does not run Docker, an
installer against the real Host, or a browser, and does not install skills into
the user's agent directories.

Public HTTP probes returned 403 in this environment; they do not establish a
missing or broken public endpoint. Production status, MIME, cache behavior and
installer hash equivalence remain deployment checks. Runtime App acceptance,
cross-agent activation behavior, model-behavior benchmarks and complete
filesystem race resistance are not established by this audit.
