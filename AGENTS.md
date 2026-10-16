# Project rules

Rules for everyone who changes this repository, people and AI agents alike. Claude Code loads
this file through CLAUDE.md; other agents read it directly.

## Project

minipsi is a Go CLI for semi-honest polynomial-based Diffie-Hellman private set intersection
between two hosts over gRPC with mutual TLS 1.3. Usage is in README.md, operations in
RUNBOOK.md, and the code analysis (entry points, call graphs) in docs/.

## Build and test

| Task | Command |
|---|---|
| Build | `make build` (static binary in bin/minipsi) |
| Try it | `go run ./cmd/minipsi bench` |
| Unit tests | `make test` (full sizes) and `make race` (what CI runs) |
| Lint | `make tools` once, then `make lint` |
| End-to-end | `make e2e` |

Run the tests that cover your change before you push. CI must pass before a merge.

## Language and characters

- Write code comments, docs, commit messages, pull requests and issues in English.
- Use plain ASCII in branch names, commit messages, and pull request and issue titles and
  bodies: no emoji, no arrows or other symbols.

## Agent attribution

Commits and pull requests made with an AI agent follow these rules:

- End the commit message and the pull request body with this line, after a blank line:
  `Supported by minipsi-relay agent`
- Never add tool attribution such as `Generated with Claude Code`, or a `Co-Authored-By:`
  trailer for an AI model.

## Branches

Format: `<module>/<feature>`

- `<module>` is the area you change: a package under internal/ (for example cli, config,
  transport, protocol, poly, rns), or one of api, docs, ci, build, agents.
- `<feature>` is a short description in lower-case kebab-case.
- Examples: `transport/relay-conn`, `docs/entry-point-call-graph-analysis`,
  `agents/project-rules`.
- Branch from an up-to-date main. Never commit directly to main.

## Commits

Format: `<type>(<module>): <summary>`

- `<type>` is one of feat, fix, docs, refactor, test, perf, build, ci, chore.
- `<module>` is the same module as in the branch name.
- `<summary>` is imperative and lower case, with no trailing period, and the whole first line
  is at most 72 characters.
- Example: `feat(api): add session api`.
- Add a body after a blank line when the reason for the change is not obvious.
- Keep one logical change per commit.
- Never amend or force-push a commit that is already pushed. Make the fix in a new commit.

## Pull requests

- Title: the commit format, `<type>(<module>): <summary>`.
- Base branch: main.
- Body: the sections of .github/pull_request_template.md (Summary, Changes, Testing, Related
  issues).
- Keep each pull request focused; split unrelated changes into separate pull requests.
- Write `Closes #<number>` when the pull request resolves an issue.
- Merge only after CI passes and a reviewer approves.

## Issues

- Title: the commit format, for example `fix(transport): retry when the daemon is busy`.
- Use the templates in .github/ISSUE_TEMPLATE (bug report, feature request).
- Labels by type: fix uses bug, feat uses enhancement, docs uses documentation; add others
  when they apply.
- A bug report includes steps to reproduce, expected and actual behaviour, the output of
  `minipsi version`, and logs.

## Agent workflow

The skill in .claude/skills/git-workflow/SKILL.md walks an agent through branches, commits,
pull requests and issues with these rules.
