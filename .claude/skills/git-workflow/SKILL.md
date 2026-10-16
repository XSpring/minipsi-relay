---
name: git-workflow
description: Create branches, commits, pull requests and issues for this repository following the project rules in AGENTS.md. Use when asked to commit, push, open a pull request or file an issue.
---

# Git workflow

Apply the rules in AGENTS.md. Write everything in English, in plain ASCII.

## 1. Branch

1. Update main: `git fetch origin`.
2. Pick the module (a package under internal/, or api, docs, ci, build, agents) and a short
   kebab-case feature name.
3. Create the branch: `git switch -c <module>/<feature> origin/main`.

## 2. Commit

1. Run the tests that cover the change (`make test`, `make race`, `make lint`, `make e2e` as
   relevant) and fix failures first.
2. Stage only the files of this change: `git add <files>`.
3. Commit with `<type>(<module>): <summary>`, where type is one of feat, fix, docs, refactor,
   test, perf, build, ci, chore:

   ```bash
   git commit -m "feat(api): add session api"
   ```

4. Check the subject line before pushing:

   ```bash
   git log -1 --format=%s | grep -Eq '^(feat|fix|docs|refactor|test|perf|build|ci|chore)\([a-z0-9-]+\): [a-z0-9].*[^.]$' \
     && [ "$(git log -1 --format=%s | wc -c)" -le 73 ] && echo "subject ok"
   git log -1 --format=%B | LC_ALL=C grep -nP '[^\x00-\x7F]' && echo "non-ASCII found" || echo "ascii ok"
   ```

## 3. Pull request

1. Push the branch: `git push -u origin <module>/<feature>`.
2. Write the body with the sections of .github/pull_request_template.md: Summary, Changes,
   Testing, Related issues.
3. Create the pull request against main with the commit format as title:

   ```bash
   gh pr create --base main --head <module>/<feature> \
     --title "<type>(<module>): <summary>" --body-file /path/to/body.md
   ```

4. Return the pull request URL that gh prints.

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
- Every commit subject matches `<type>(<module>): <summary>`.
- Tests for the change pass.
- Titles and bodies are English and plain ASCII.
