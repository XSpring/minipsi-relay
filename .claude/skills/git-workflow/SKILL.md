---
name: git-workflow
description: Write the task plan, then create branches, commits, pull requests and issues for this repository following the project rules in AGENTS.md. Use before starting a task and when asked to commit, push, open a pull request or file an issue.
---

# Git workflow

Apply the rules in AGENTS.md. Write everything in English, in plain ASCII.

## 0. Plan

1. Read AGENTS.md and the docs/ files for the area you will change.
2. Write the plan (Objective, Scope, Approach, Affected components, Acceptance criteria,
   Verification plan; see Planning in AGENTS.md), show it to the user, and put it in the
   task's issue: the plan sections of the feature request template, or a comment on a bug
   report. If the task has no issue yet, create it first as in section 4 (`gh issue create`). To add the plan, edit the full issue body with the plan sections filled in (`gh issue edit <number> --body-file body.md`; the file replaces the whole body), or for a bug report add it as a comment (`gh issue comment <number> --body-file plan.md`).
3. Do not write code before the plan exists. When the scope or approach changes, update the
   issue first.

## 1. Branch

1. Check the scope (short-lived branches, see AGENTS.md): the task must be one small change
   that can be implemented, tested, reviewed and merged within 24-48 hours. If it is larger,
   propose a breakdown into smaller tasks, each with its own branch, and wait for agreement
   before writing code.
2. Update main: `git fetch origin`.
3. Pick the module (a package under internal/, or api, docs, ci, build, agents) and a short
   kebab-case feature name.
4. Create the branch: `git switch -c <module>/<feature> origin/main`.

## 2. Commit

1. Run the tests that cover the change (`make test`, `make race`, `make lint`, `make e2e` as
   relevant) and fix failures first.
2. Stage only the files of this change: `git add <files>`.
3. Commit with `<type>(<module>): <summary>`, where type is one of feat, fix, docs, refactor,
   test, perf, build, ci, chore. End the message with the line
   `Supported by minipsi-relay agent`. Never add `Generated with Claude Code` or a
   `Co-Authored-By:` trailer for an AI model:

   ```bash
   git commit -m "feat(api): add session api" -m "Supported by minipsi-relay agent"
   ```

4. Check the message before pushing:

   ```bash
   git log -1 --format=%s | grep -Eq '^(feat|fix|docs|refactor|test|perf|build|ci|chore)\([a-z0-9-]+\): [a-z0-9].*[^.]$' \
     && [ "$(git log -1 --format=%s | wc -c)" -le 73 ] && echo "subject ok" || echo "subject bad"
   git log -1 --format=%B | LC_ALL=C grep -nP '[^\x00-\x7F]' && echo "non-ASCII found" || echo "ascii ok"
   git log -1 --format=%B | sed '/^$/d' | tail -1 | grep -qx 'Supported by minipsi-relay agent' && echo "footer ok" || echo "footer missing"
   git log -1 --format=%B | grep -Eiq 'generated with \[?(claude|chatgpt|copilot|cursor)|co-authored-by:.*(claude|anthropic|openai|gpt|copilot)' \
     && echo "remove the tool attribution" || echo "no tool attribution"
   ```

## 3. Pull request

1. Follow "Before opening or updating a pull request" in AGENTS.md: re-read the task and the
   plan, review the whole diff (`git diff origin/main...HEAD`) against the scope, and run the
   verification plan.
2. Push the branch: `git push -u origin <module>/<feature>`.
3. Write the body with the sections of .github/pull_request_template.md (Summary, Changes,
   Implementation details, Verification, Dependencies and related work, Limitations and
   risks) and the quality rules in AGENTS.md. Under Verification, list only checks you
   actually ran, with commands and results, and separately the checks that failed, were
   skipped or could not be run. End the body with the line
   `Supported by minipsi-relay agent`, with no tool attribution.
4. Create the pull request against main with the commit format as title:

   ```bash
   gh pr create --base main --head <module>/<feature> \
     --title "<type>(<module>): <summary>" --body-file /path/to/body.md
   ```

5. Return the pull request URL that gh prints.
6. After new commits or new verification results, update the body:
   `gh pr edit <number> --body-file /path/to/body.md`.

## 4. Issue

1. Pick the template: bug report (label bug) or feature request (label enhancement); use the
   label documentation for docs work.
2. Create the issue with the commit format as title:

   ```bash
   gh issue create --title "fix(transport): retry when the daemon is busy" \
     --label bug --body-file /path/to/body.md
   ```

## Checklist

- The branch is `<module>/<feature>` and starts from an up-to-date main.
- The branch holds one small task and is ready for a pull request within 24-48 hours; unrelated
  changes are in other branches.
- Every commit subject matches `<type>(<module>): <summary>`.
- No pushed commit was amended or force-pushed; fixes are new commits.
- The plan is in the task's issue and matches the change.
- Tests for the change pass.
- The pull request body follows the template and the quality rules in AGENTS.md, matches the
  final diff, and its Verification lists only checks that were run and names the ones that
  were not.
- Titles and bodies are English and plain ASCII.
- Commit messages and pull request bodies end with `Supported by minipsi-relay agent` and
  contain no `Generated with Claude Code` or AI `Co-Authored-By:` lines.
