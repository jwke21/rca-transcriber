# ghprobe

A throwaway manual test harness for `internal/adapter/github`. It is not
part of any work package — delete this directory once you're done testing,
don't commit it.

## Read this first

Unlike the Deepgram/Gemini probes, `Publish` has **real, visible side
effects**: it creates a branch, commits a file, and opens (or edits) a pull
request in whatever repo you point it at.

**Never point this at a real project repo.** Use a disposable scratch repo
you control, with a fine-grained PAT scoped only to that repo (Contents +
Pull requests, read/write — same scopes `.env.example` describes).

## Setup

Either export flags directly, or point the `GITHUB_*` vars in `.env` at your
scratch repo and omit the flags:

```
GITHUB_TOKEN=ghp_your-scratch-repo-token
GITHUB_OWNER=you
GITHUB_REPO=your-scratch-repo
GITHUB_BASE_BRANCH=main
```

## Run

```
go run ./cmd/ghprobe -owner you -repo scratch-repo -token ghp_xxx
```

Flags: `-token`, `-owner`, `-repo`, `-base` (default `main`), `-incident`
(default `4821`), `-api-base-url` (leave empty for the real API). The tool
prints the target repo before publishing so you can double-check it's the
scratch one, not the real project.

## What to check

Run it once, then run it again with the same `-incident` value, to exercise
both branches of every idempotent step in IMPLEMENTATION.md §6.3:

- **First run:** creates the branch, creates the file, opens a new PR.
  Confirms the base-ref lookup, branch creation, file creation and PR
  creation requests all succeed against the real API.
- **Second run (same `-incident`):** branch already exists (422, tolerated),
  file already exists so it's updated with its `sha` and an "Update RCA…"
  message, and the open PR is found and its body patched instead of a new
  PR being opened. Confirms the reuse/update paths the fakes in
  `publisher_test.go` assume actually match GitHub's real behavior.
- Use a fresh `-incident` value to confirm a clean create path.
- A bad `-token` should fail with an error that doesn't contain the token
  itself.

The automated tests in `internal/adapter/github/publisher_test.go` already
cover all of this against a fake `httptest.Server`; this tool is only for
confirming behavior against the real GitHub API and your PAT's actual scopes.

## Cleanup

Close the PR(s) and delete the `incident-*` branch(es) this creates in your
scratch repo when you're done.
