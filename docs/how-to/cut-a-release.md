# Cut a release

Releases follow Gitflow ([CONTRIBUTING.md](../../CONTRIBUTING.md), "Branches and pull requests"):
changes land on `develop` through feature pull requests, and a release branch carries them to
`main`, where the tag is cut.

1. **Cut `release/vX.Y.Z` from `develop`.**

   ```bash
   git switch develop && git pull
   git switch -c release/vX.Y.Z
   ```

2. **Pin the tools the factory image pairs with.** `images/factory/versions.env` names the ynh and
   ynm releases the image is built from (ADR-009). Bump them to the releases this version is
   tested with, and commit.

3. **Prove it.** All three must pass on the release branch:

   ```bash
   make check
   make e2e                              # the factory against your sandbox, ynf on the host
   make factory-image                    # from the pinned releases, no YNH_SRC or YNM_SRC
   make -C sandbox e2e-factory           # the same, ynf inside the factory image
   ```

4. **Open a pull request from `release/vX.Y.Z` into `main`,** with the release notes as its
   description, and merge it with **Create a merge commit** (not squash) once CI is green. GitHub
   deletes the release branch when it merges.

5. **Tag `vX.Y.Z` on `main` and push the tag.**

   ```bash
   git switch main && git pull
   git tag -a vX.Y.Z -m vX.Y.Z
   git push origin vX.Y.Z
   ```

   The `release` workflow then:

   - **check:** runs `make check` again on the tag;
   - **release:** GoReleaser builds ynf for macOS and Linux, signs the checksums with cosign
     (keyless), attaches SBOMs, publishes the GitHub release, and pushes `Formula/ynf.rb` to
     `eyelock/homebrew-tap` with `RELEASE_TOKEN`;
   - **factory image:** builds `ghcr.io/eyelock/ynf-factory:X.Y.Z` and `:latest` for amd64 and
     arm64 from the pinned ynh and ynm releases, reading ynm's release with `RELEASE_TOKEN`.

6. **Check what it published:** the GitHub release's assets; the signature, with
   `cosign verify-blob --bundle checksums.txt.sigstore.json --certificate-identity-regexp
   'https://github.com/eyelock/ynf/' --certificate-oidc-issuer
   https://token.actions.githubusercontent.com checksums.txt`; the formula in
   `eyelock/homebrew-tap`; and the factory image for both architectures. GoReleaser writes a
   commit list as the release's description: replace it with the release notes,
   `gh release edit vX.Y.Z --notes-file <notes>`.

7. **Back-merge into `develop` once the release works,** with a pull request from `main` into
   `develop`, merged with **Create a merge commit**. Waiting means a hotfix the release needed
   reaches `develop` in the same back-merge.

If the release workflow fails before publishing anything, fix it with a hotfix, then move the
tag to the hotfix's merge commit and push it again: no release, formula or image exists for it
yet, so nothing has used it.

The version is the tag: `ynf version` prints it, and a build from anything but a clean tag says
`dev-<branch>-<commit>`.

## A hotfix

Branch `hotfix/<what>` from the release tag, fix, and open a pull request into `main`. Merge it
with **Create a merge commit**, tag the next patch version on `main`, and back-merge `main` into
`develop` once that release works.

## The docs site

The site at https://eyelock.github.io/ynf/ is built from `/docs` on `main`, so the published docs
are the released ones; changes on `develop` appear with the next release.
