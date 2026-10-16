# Claude Code instructions

Follow the project rules in AGENTS.md. For branches, commits, pull requests and issues, use the
git-workflow skill in .claude/skills/git-workflow.

@AGENTS.md

## Claude-specific notes

- Orient with docs/SUMMARY.md and docs/ARCHITECTURE.md; use docs/CALL_GRAPHS.md and
  docs/BUSINESS_FLOWS.md to trace a path before editing it.
- Refer to code as `path:line`. Line numbers in docs/ are as of commit 4aa7c9f; search by symbol
  if they have drifted.
- Prefer targeted tests while iterating: `go test ./internal/<pkg> -run <Name>`, then the full
  checks from AGENTS.md before finishing.
- `make lint`, `make vuln` and `make proto` need tools from `make tools`, which installs into
  `$(go env GOPATH)/bin`; ask before installing them.
- When running the CLI by hand, use `--dev` with `MINIPSI_DEV_PKI` set to a temporary directory,
  loopback addresses, and stop any background `serve` or `sender` process afterwards.
- `old_plan/` is a local, gitignored folder and not part of the product.
