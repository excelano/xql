# Releasing xql

The release loop lives in `~/notes/releasing.md` — the ordered steps, the apt
step, the winget submission, the spent-tag rule, and the standing facts about
tokens and secrets. Failure recipes are in `~/notes/build_release_gotchas.md`.
This file carries what is true of xql and not of its siblings.

| | |
|---|---|
| Loop | goreleaser |
| `apt-ship` argument | `xql` |
| winget package | `Excelano.xql` |
| Windows asset | `xql_<version>_windows_amd64.zip` |

**The release builds** platform archives for Linux and macOS on both
architectures plus Windows x64, the two `.deb` packages, `checksums.txt`, the
Homebrew formula, and the GitHub Release, all in one job.

**Windows is x64 only.** goreleaser ignores the `windows/arm64` combination, so
there is one Windows archive and one winget installer entry. Adding the target
later means the winget manifest grows a second `Installers` block.

`install.sh` and `uninstall.sh` are attached to the release as extra files, which
is what lets a user pin an install to a release URL instead of the rolling `main`
branch. They ship as-is from the tagged commit, so a fix to either only reaches
pinned installs on the next release.

**The release workflow takes a `workflow_dispatch` input,** so a tag that fails
to trigger it needs no re-tagging: `gh workflow run release.yml -f tag=v1.2.3`.

**The unit tests cannot reach SharePoint,** so a clean `go test ./...` says the
code compiles and the pure logic holds, not that the `sp` backend's Graph calls
still work. The live suite is the pass that does, and it is part of step 1 here:

```sh
XQL_LIVE_LIST=https://<tenant>.sharepoint.com/sites/<test-site>/Lists/<test-list> go test -tags live ./...
```

It runs on a machine that has signed in once (`xql sp`, or any xfiles tool),
binds the named list, and round-trips one row through INSERT, SELECT, UPDATE
and DELETE, so the list's rows are what they were when it finishes. It cannot
run in CI: device-code needs a human, and a refresh token in this public repo's
secrets would be a live credential to the tenant.

**The `xtabular` metapackage depends on xql** and pins no version, so a release
needs no metapackage rebuild. Only a change in membership does.
