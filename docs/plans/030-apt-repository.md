# Publish an apt repository on GitHub Pages (GH-28)

Source: conversation; follows GH-37 (release builds, plan 028), which already builds `burrow_<version>_linux_<arch>.deb` with `nfpms`. GitHub issue: #28.
Done when: **`sudo apt install burrow` works on a clean Ubuntu machine from the Pages URL, and `apt upgrade` picks up the next release.**
Final location: `docs/plans/030-apt-repository.md` (same format as `docs/plans/archive/027-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: a signed repo you can install from locally** | Make the GPG key (`gpg --quick-generate-key "Burrow apt <reachme@amitavroy.com>" ed25519 sign never`), back up the private key outside the repo, export the public key to `packaging/apt/burrow.gpg` (dearmored). `scripts/apt-repo.sh <deb dir> <out dir>`: copies the `.deb` files into `<out>/pool/main/`, writes `dists/stable/main/binary-{amd64,arm64}/Packages(.gz)` with `apt-ftparchive packages`, `dists/stable/Release` with `apt-ftparchive release` (Origin, Label, Suite `stable`, Architectures, Components), then `InRelease` (`gpg --clearsign`) and `Release.gpg` (`gpg -abs`). Copies `burrow.gpg` to `<out>/burrow.gpg`. **Review point (demo: build into `/tmp/apt-test`, add `deb [signed-by=/tmp/apt-test/burrow.gpg] file:///tmp/apt-test stable main`, `sudo apt update`, `apt-cache policy burrow` shows the new version).** | [ ] |
| 2 | **Slice 2: published on GitHub Pages** | Create the orphan `gh-pages` branch, enable Pages for it (`gh api -X POST repos/amitavroy/burrow/pages -f 'source[branch]=gh-pages' -f 'source[path]=/'`), add `.nojekyll`. `make apt-repo`: runs the script against `dist/*.deb` into a `git worktree` of `gh-pages`, commits and pushes. **Review point (demo: `sudo apt remove burrow`, add the real source line with `https://amitavroy.github.io/burrow`, `sudo apt update && sudo apt install burrow`, `syncd version`).** | [ ] |
| 3 | **Slice 3: part of every release** | `make publish` calls `make apt-repo` after `make release`, so one command does tag, GitHub release and apt repo. `make apt-repo` keeps the pool to the last 5 versions so the branch does not grow forever. **Review point (demo: publish the next patch version, then `sudo apt update && apt list --upgradable` shows `burrow`, and `sudo apt upgrade` installs it).** | [ ] |
| 4 | **Slice 4: docs** | README Install: the three commands (fetch key to `/etc/apt/keyrings/burrow.gpg`, add `/etc/apt/sources.list.d/burrow.list` with `signed-by`, `apt update && apt install burrow`); wiki "Release builds" section (what `make apt-repo` does, where the key lives, how to rotate it later); `make help` descriptions. **Review point.** | [ ] |

## Context

A release already produces `.deb` files (GH-37), but users have to find the GitHub release, download the right architecture and install it by hand, and they get no updates. An apt repository makes it `apt install burrow` and `apt upgrade`. Repo hosting is static files, which GitHub Pages serves for free.

## Decisions

1. **Our own repository, not the Debian or Ubuntu archives.** The official archives need source packaging, every Go dependency packaged or vendored, a sponsor, and no build-time secrets (the OAuth client is baked in with `-ldflags`). That is weeks of work for no gain here.
2. **GitHub Pages from a `gh-pages` branch of this repo (user choice).** Free, no extra service, same repo and permissions as releases. The `.deb` files are copied there; they are about 2 MB each. Pages has a soft 1 GB site limit, hence the "keep the last 5 versions" rule in slice 3.
3. **`apt-ftparchive` (from `apt-utils`) plus `gpg`, no `reprepro` or `aptly`.** The repo is regenerated from the `pool/` directory every time, so there is no database to keep or lose. This matches the project's "disposable cache" habit: the files in `pool/` are the truth.
4. **Flat layout with one suite.** Suite `stable`, component `main`, architectures `amd64` and `arm64` (what `nfpms` builds). No per-Ubuntu-release suites; the binary is static (`CGO_ENABLED=0`), so one build serves all.
5. **A dedicated GPG key, kept on the maintainer machine.** ed25519, no expiry, generated for this repo only. The private key is backed up outside the repo and never committed. The public key is committed at `packaging/apt/burrow.gpg` and published at `<site>/burrow.gpg`. No CI means no key in GitHub secrets.
6. **Users trust the key through `signed-by`, never `apt-key`.** `apt-key` is deprecated and trusts a key for every repository. The documented line is `deb [signed-by=/etc/apt/keyrings/burrow.gpg] https://amitavroy.github.io/burrow stable main`.
7. **`make apt-repo` is its own target and `make publish` calls it.** It can be re-run alone if a push fails, and `publish` stays the one-command release. It reads `dist/*.deb`, so it runs after `make release`.
8. **Out of scope:** snap, rpm and apk repos, official archives, key rotation (the wiki records how), a custom domain, a CI workflow (ADR-002).
9. **Plan number vs issue number.** This doc is 030 and the GitHub issue is #28; they are unrelated counters.

## Shape

```sh
# scripts/apt-repo.sh <deb dir> <out dir>   (sketch)
mkdir -p "$out/pool/main"; cp "$debs"/*.deb "$out/pool/main/"
cd "$out"
for arch in amd64 arm64; do
  d=dists/stable/main/binary-$arch; mkdir -p $d
  apt-ftparchive --arch $arch packages pool > $d/Packages
  gzip -k -f $d/Packages
done
apt-ftparchive -o APT::FTPArchive::Release::Origin=Burrow \
  -o APT::FTPArchive::Release::Suite=stable \
  -o APT::FTPArchive::Release::Architectures="amd64 arm64" \
  -o APT::FTPArchive::Release::Components=main \
  release dists/stable > dists/stable/Release
gpg --default-key "$KEY" --clearsign -o dists/stable/InRelease dists/stable/Release
gpg --default-key "$KEY" -abs -o dists/stable/Release.gpg dists/stable/Release
```

```sh
# user side
sudo install -d /etc/apt/keyrings
sudo curl -fsSL https://amitavroy.github.io/burrow/burrow.gpg -o /etc/apt/keyrings/burrow.gpg
echo "deb [signed-by=/etc/apt/keyrings/burrow.gpg] https://amitavroy.github.io/burrow stable main" | sudo tee /etc/apt/sources.list.d/burrow.list
sudo apt update && sudo apt install burrow
```

## Files to create/modify

| File | Change |
| --- | --- |
| `scripts/apt-repo.sh` | new: builds the signed repo tree from a directory of `.deb` files |
| `packaging/apt/burrow.gpg` | new: the public signing key (dearmored) |
| `Makefile` | `apt-repo` target (worktree of `gh-pages`, run the script, commit, push); `publish` calls it |
| `gh-pages` branch | new, orphan: `pool/`, `dists/`, `burrow.gpg`, `.nojekyll` |
| `README.md`, `docs/wiki/index.md` | install commands, release steps, key notes |

## Testing plan

- Slice 1: the local `file://` source installs and `apt-cache policy burrow` shows the version; `apt update` reports no signature warnings.
- Tamper check: edit a byte in `Packages`, `apt update` must refuse the repo (proves signing is checked).
- Slice 2: install on a clean machine or container (`docker run -it ubuntu:24.04`) from the Pages URL, including the arm64 index (`apt-get -o APT::Architecture=arm64 update` is enough to check the index).
- Slice 3: publish a patch release and confirm `apt upgrade` installs it.
- No unit tests: the script is shell glue over `apt-ftparchive` and `gpg`; the demos are the check.

## Verification

1. `make vet test` passes.
2. A clean Ubuntu machine installs `burrow` from the Pages URL with only the three documented commands; `syncd version` prints the release version.
3. After the next release, `apt update && apt list --upgradable` shows `burrow`.
4. `gpg --list-secret-keys` shows the key on the maintainer machine, and the private key backup exists outside the repo.
5. Open: the private key is a single point of failure; if it is lost the repo must be re-keyed and users must re-add the key. Write the backup location down in the wiki (not the key).
