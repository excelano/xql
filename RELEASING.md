# Releasing xql

The release loop for a new version. Run it from a clean `main` with the working tree committed. Examples below cut `v1.2.3`; substitute the version you are actually releasing.

**There is no version to bump.** goreleaser stamps the binary from the tag (`-X main.version={{ .Version }}`), so the tag is the single source of truth and no file in the repo carries the number. The loop starts at step 2 if the tests already pass.

1. **Verify.** `go build ./... && go test ./...`, and confirm `git status` is clean. A dirty tree makes goreleaser refuse the release outright, which is the good failure — the bad one is tagging a commit you have not tested.

2. **Tag and push.** `git tag v1.2.3 && git push origin main --tags`. The `v*` tag triggers `.github/workflows/release.yml`, which runs goreleaser and does the whole build in one job: platform archives for Linux and macOS on both architectures plus Windows x64, the two `.deb` packages, `checksums.txt`, and the GitHub Release itself. It also pushes the updated formula to `excelano/homebrew-tap`, so Homebrew needs no local step.

   `install.sh` and `uninstall.sh` are attached to the release as extra files, which is what lets a user pin an install to a release URL instead of the rolling `main` branch. They are shipped as-is from the tagged commit, so a fix to either only reaches pinned installs on the next release.

   If the tag was created by another workflow's `GITHUB_TOKEN` the push trigger will not fire, since GitHub suppresses downstream events for token-created refs. Dispatch by hand in that case: `gh workflow run release.yml -f tag=v1.2.3`.

3. **Add the .debs to the Excelano apt repo.** Download `xql_1.2.3_amd64.deb` and `xql_1.2.3_arm64.deb` from the release, then in `~/excelano-apt/`: `add-deb.sh` each one → `rebuild.sh` (GPG-signs) → `updatesite excelano.com.apt -y`. **Dry-run the rsync first** (`rsync … --delete -n`) and confirm zero deletions before the real push — the apt pool is a superset of live, and a stray `--delete` wipe is the standing hazard. See `feedback_rsync_parent_wipes_subpath`.

   `updatesite` does not touch git, so commit the apt repo right afterwards or it drifts behind what is actually being served.

4. **Submit the winget manifest.** winget stores one manifest per version, so every release needs its own PR; there is no update in place. Run komac:
   ```sh
   komac update Excelano.xql --version 1.2.3 \
     --urls https://github.com/excelano/xql/releases/download/v1.2.3/xql_1.2.3_windows_amd64.zip \
     --submit
   ```
   It downloads the asset, computes the `InstallerSha256`, generates the manifest from the previous version's, and opens the PR against `microsoft/winget-pkgs`. Drop `--submit` (or add `--dry-run --output ./dir`) to eyeball the manifest first, and check the generated hash against the release's `checksums.txt`.

   **Sync the fork before submitting**, every time. komac pushes a branch to `anderix/winget-pkgs`, and a fork that has drifted behind upstream fails in a way that reads like a permissions problem rather than a stale fork. Recipe in `~/notes/build_release_gotchas.md`.

   A **version update** to an already-merged package clears automated validation and merges with no human involved, usually inside a day. A **new package** picks up the `New-Package` label and waits on a volunteer moderator, which runs to days or weeks. The recurring validation failures and their recipes are all in `~/notes/build_release_gotchas.md`.

   **A pushed `v*` tag is spent.** The merged manifest pins `InstallerSha256`, so deleting and re-cutting a tag swaps the release asset out from under it and breaks every install of that version. Nothing in the pipeline refuses the second attempt — winget, apt, and the Homebrew formula all overwrite silently. If a release goes wrong after the tag is pushed, bump to the next number.

## Notes

- **Windows is x64 only.** goreleaser ignores the `windows/arm64` combination, so there is one Windows archive and one winget installer entry. Adding the target later means the winget manifest grows a second `Installers` block.
- **`go install` bypasses the ldflags,** so a copy installed that way reports a dev version rather than the tag. That is expected and not worth working around; the installers, apt, Homebrew, and winget all carry the stamped binary.
- **Homebrew tap access is an org-secret question.** The release job pushes the formula with `HOMEBREW_TAP_TOKEN`. If that secret is scoped to selected repositories, a repo that is not on the list fails the formula step with `Input required and not supplied: token` while the rest of the release succeeds.
- **The `xtabular` metapackage depends on xql** and pins no version, so a release needs no metapackage rebuild. Only a change in membership does.
- The README, the landing page (`excelano.com/xql`), and `SECURITY.md` reference the version implicitly via "latest"; none need a per-release edit.
