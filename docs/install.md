# Installing deps

In CI you don't need to install anything: the composite action downloads, verifies and caches the release binary ([Running deps in CI](ci.md)). Install deps to run it on your machine, in a container image, or in a plain `run:` step.

- [Packages](#packages)
- [Static binary](#static-binary)
- [From source](#from-source)
- [Verify a download](#verify-a-download)
- [Local usage](#local-usage)

## Packages

Every release is published to the Debian, RPM, Arch Linux and Alpine package registries on git.jakob-moeller.cloud, which need no login. Each package installs `/usr/bin/deps` with bash, zsh and fish completions, and the package files are also attached to each release.

**Debian and Ubuntu (amd64, arm64).**

```sh
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian/repository.key -o /etc/apt/keyrings/deps.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/deps.asc] https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian stable main" | sudo tee /etc/apt/sources.list.d/deps.list
sudo apt-get update && sudo apt-get install deps
```

The package recommends `git`, which `deps run` needs.

**Fedora and RHEL (x86_64, aarch64).** Gitea signs the repository metadata, not the packages, so the repository checks the metadata signature (`repo_gpgcheck`) instead of the package signature:

```sh
printf '[deps]\nname=deps\nbaseurl=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/rpm\nenabled=1\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/rpm/repository.key\n' | sudo tee /etc/yum.repos.d/deps.repo
sudo dnf install deps
```

The package recommends `git`, which dnf installs by default.

**Arch Linux (x86_64, aarch64).** The repository is signed with the registry's key, which pacman has to trust:

```sh
curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/repository.key -o /tmp/deps.key
sudo pacman-key --add /tmp/deps.key
sudo pacman-key --lsign-key "$(gpg --show-keys --with-colons /tmp/deps.key | awk -F: '$1 == "fpr" { print $10; exit }')"
printf '\n[jakobmoellerdev]\nSigLevel = Required\nServer = https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/$repo/$arch\n' | sudo tee -a /etc/pacman.conf
sudo pacman -Syu deps
```

`git` is an optional dependency, needed by `deps run`.

**Alpine Linux (x86_64, aarch64).** One repository serves every Alpine release, since the binary is static. As root:

```sh
wget -qO /etc/apk/keys/jakobmoellerdev@406384923acbad2a78f7a8f09acbfa3ae0bd4acf33360d688568adaa4cd205a3.rsa.pub https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/key
echo https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/stable/main >> /etc/apk/repositories
apk add deps
```

apk checks the index signature against the key file name, so keep the name. The package depends on `git`, which `deps run` needs.

## Static binary

For linux, macOS (`darwin`) and Windows on amd64 and arm64: `deps_<os>_<arch>`, with `.exe` on Windows. Download it from the [releases](https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases) and verify it:

```sh
base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.2.5
curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt"
grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 ~/.local/bin/deps
```

On macOS use `shasum -a 256 -c` instead of `sha256sum -c`. The composite action and the packages stay Linux-only.

## From source

`go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@latest`, or `task build` in a clone, which builds a static `bin/deps`.

## Verify a download

Every release file is attested with [Sigstore](https://www.sigstore.dev) by the release workflow on the GitHub mirror (`.github/workflows/release.yml`): SLSA build provenance for each binary, package and `checksums.txt`, and an SPDX 2.3 SBOM (syft) for each binary. The packages contain the same binary, so the binary's SBOM describes them too. Both forges serve byte-identical files, so the same attestation verifies a download from either one. The bundles are release assets on both forges: `<file>.sigstore.json` (provenance), `deps_linux_<arch>.sbom.sigstore.json` and the SBOM itself, `deps_linux_<arch>.spdx.json`.

With [gh](https://cli.github.com), against GitHub's attestations API:

```sh
gh attestation verify deps_linux_amd64 -R jakobmoellerdev/deps
gh attestation verify deps_linux_amd64 -R jakobmoellerdev/deps --predicate-type https://spdx.dev/Document/v2.3   # the SBOM
```

With the bundle from the release instead of the API (works with the Gitea release alone):

```sh
base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.2.5
curl -fsSLO "$base/deps_linux_amd64" -O "$base/deps_linux_amd64.sigstore.json"
gh attestation verify deps_linux_amd64 -R jakobmoellerdev/deps --bundle deps_linux_amd64.sigstore.json
```

Fully offline, add `--custom-trusted-root trusted_root.jsonl` from an earlier `gh attestation trusted-root > trusted_root.jsonl`. Or with [cosign](https://github.com/sigstore/cosign) v3:

```sh
cosign verify-blob-attestation --bundle deps_linux_amd64.sigstore.json --type slsaprovenance1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/jakobmoellerdev/deps/\.github/workflows/release\.yml@refs/(tags/v0\.2\.5|heads/main)$' \
  deps_linux_amd64
```

The signing identity is the release workflow at the tag (`…/release.yml@refs/tags/<tag>`), or at `refs/heads/main` when a release was republished by hand (`workflow_dispatch`). For the SBOM use `deps_linux_amd64.sbom.sigstore.json` with `--type https://spdx.dev/Document/v2.3`.

**Checksum chain.** `checksums.txt` is attested too, so verifying it covers every file it lists:

```sh
gh attestation verify checksums.txt -R jakobmoellerdev/deps && sha256sum -c --ignore-missing checksums.txt
```

## Local usage

```sh
deps plan                  # unified diff on stdout, one line per update or error on stderr
deps plan --show-skipped   # also list skipped deps and why
deps apply                 # write the updates (plus go mod tidy and lockfiles)
deps run --dry-run         # which PRs would be created, updated or closed
deps run                   # push branches and open or update PRs
deps explain 'actions/*'   # rules, candidates and the decision for matching deps
deps schema                # JSON Schema of .deps.yaml
```

```
update  .github/workflows/ci.yml:12  actions/checkout  v6.0.0 -> v7.0.1  (major)
1 updates in 1 files, 0 skipped, 0 errors
```

Exit codes: 0 success, 1 errors, 2 invalid flags or config.

- **Where `run` gets its settings:** the platform comes from the `origin` remote; the token comes from `--token`, `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN` or the git credential helper.
- **More:** every flag and environment variable is in the [CLI reference](reference/cli.md).
