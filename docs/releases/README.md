# Releases

go2lean releases **every Sunday**: one patch version a week (`v0.1.3`,
`v0.1.4`, ...). A minor version (`v0.2.0`) is for a change that breaks the
command line or the JSON records the tools write.

go2lean is installed from source (`go install ...@vX.Y.Z`), so a release is
a tag and its notes; there are no binaries to build.

## The week

| when | what |
|---|---|
| Monday to Saturday | Pull requests as usual: CI green, merge, docs updated with each. There is no freeze. |
| Saturday or Sunday morning | Draft the notes from what has merged. |
| Sunday | Refresh the notes, run the gates, then the maintainer tags and publishes. |

## Steps

1. **Draft the notes** (Saturday or Sunday morning; refresh them just before
   the gates if more has merged):

   ```bash
   scripts/release.sh notes > docs/releases/vX.Y.Z.md
   ```

   Edit it: one paragraph on the week; the findings (the counts as of the
   date, what is new); the known limits (what changed since the last
   release). Add the entry to [CHANGELOG.md](../../CHANGELOG.md). Merge both
   in a pull request.

2. **Run the gates** (Sunday), on an up-to-date `main`:

   ```bash
   scripts/release.sh check
   ```

   It checks that the tree is clean, CI is green on the head of `main`, the
   defect counts in the README and in [findings](../findings.md) agree, and
   the notes and the changelog entry exist. It does not tag.

3. **Run what CI runs and the two cross-checks CI does not**:
   `scripts/regress.sh`, `scripts/sem-diff.sh`, `scripts/llvm-regress.sh`
   (CLAUDE.md lists the rest).

4. **Tag and publish**, from the head of `main` that passed the gates. The
   maintainer does this step: a pushed tag is public and cannot be taken
   back, so the tools prepare it and never run it:

   ```bash
   git tag -a vX.Y.Z -m "go2lean vX.Y.Z" origin/main
   git push origin vX.Y.Z
   gh release create vX.Y.Z --title "go2lean vX.Y.Z" --notes-file docs/releases/vX.Y.Z.md
   ```

5. **Check the install** from a clean directory:

   ```bash
   GOFLAGS=-mod=mod go install github.com/fooexec/go2lean/cmd/go2lean@vX.Y.Z
   ```

## What a release says

Four parts, as in [v0.1.2](https://github.com/fooexec/go2lean/releases/tag/v0.1.2):
what it is and how to install it; the evidence (counts as of the date);
what is new this week; the known limits. Upstream report drafts are not part
of a release: they wait for the user to file them.

## Not in a release

A week with nothing to release says so in the changelog and skips the tag.
A broken `main` is fixed first; a release is never cut over red CI.
