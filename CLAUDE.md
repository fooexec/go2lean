# go2lean: working rules

go2lean turns Go packages into a Lean model, searches it for bugs, and
replays each candidate as a real `go test`. Keep the name go2lean.

## Workflow

- Work on a feature branch and open a pull request into `main`; never push
  to `main` directly. Once CI is green and the PR mergeable, merge it and
  carry on with the next item; no need to wait for a go-ahead between PRs.
- Plans live in GitHub issues. The roadmap, its milestones and the periodic
  reviews are in fooexec/go2lean#53. Every problem a run or an
  evaluation turns up becomes an issue under a milestone there.
- Evaluations find problems; development fixes them. They run at the same
  time: an evaluation uses a frozen build of the tool, so work on `main`
  does not change a run in progress, and after a fix the same cases are
  rerun to check it.
- Every PR updates the docs it affects, the README above all. Keep the
  README short (what it is, quick start, how it works, links); details go
  to the docs it links: `docs/usage.md`, `docs/pipeline.md`,
  `docs/validation.md` (ground-truth table, evaluations),
  `docs/reference.md` (supported Go, roadmap, counts), `docs/concurrency.md`.
- CI minutes are limited. Before pushing, run what CI runs and push once:
  `go vet ./...`, `go test ./internal/... ./cmd/go2lean`,
  `PROVE=0 scripts/regress.sh <ws>` (update
  `testdata/regress/examples.golden` only for new or intended outcome
  changes), `scripts/sem-diff.sh <ws>` (the model against Go on Goose's
  semantics tests; `testdata/goosesem/expected.txt` lists the known
  disagreements), and the `json-diff.sh` checks of the `models` job. The
  Lean proofs (`scripts/proofs.sh`, the `proofs` workflow) run on demand.
- Do not post to other projects' trackers (issues, PRs, comments) without
  the user's consent.

## Words

- The pipeline has three stages: the **compile stage** (Go to the Lean
  model), the **spec & search stage** (properties and the engines), the
  **replay stage** (counterexamples replayed as `go test`, with a PoC).
- Code, docs, commits and PRs are in English.

## Layout

- `internal/frontend/gossa` (compile stage), `internal/constraint`
  (spec & search stage), `internal/poc` and `internal/replay` (replay
  stage), `lean/Go2Lean` (the model's semantics and the engines).
- Design notes: `docs/design.md`, `docs/semantics.md`,
  `docs/concurrency.md`, `docs/limitations.md`, `docs/related-work.md`.
- Ground truth: `examples/`; GoKer evaluation: `scripts/goker.sh`.
