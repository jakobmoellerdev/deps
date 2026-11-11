#!/usr/bin/env bash
# Attaches the GitHub mirror's Sigstore attestations of a release to the Forgejo release of the same
# tag. Usage: attest-from-github.sh <tag> [dir]
#
# Forgejo Actions has no OIDC tokens, so Forgejo cannot sign keyless itself. The GitHub release of the
# same tag is built from the same commit with the same reproducible build and attested there. This
# script waits until the GitHub release has every bundle, verifies each one with cosign against the
# Forgejo-built file (which also proves both builders produced the same bytes) and uploads the
# bundles and SBOMs to the Forgejo release. Any mismatch fails the script before anything is uploaded.
#
# <dir> holds the Forgejo-built release files (dist/ in the release job). Without it, the files are
# downloaded from the Forgejo release.
#
# Environment:
#   COSIGN            cosign binary (default cosign; `task release:attest-from-github` passes the
#                     pinned, checksum-verified one)
#   GITHUB_REPO       GitHub repository with the attestations (default jakobmoellerdev/deps)
#   ATTEST_TIMEOUT    seconds to wait for the GitHub assets (default 1800)
#   ATTEST_INTERVAL   seconds between polls (default 30)
#   FORGEJO_TOKEN     forge API token for packaging/forge-release.sh (the job token in Actions)
#
# Re-runs are idempotent: an asset of the same name is replaced.
set -euo pipefail

tag=${1:?usage: attest-from-github.sh <tag> [dir]}
dir=${2:-}
cosign=${COSIGN:-cosign}
repo=${GITHUB_REPO:-jakobmoellerdev/deps}
timeout=${ATTEST_TIMEOUT:-1800}
interval=${ATTEST_INTERVAL:-30}
issuer=https://token.actions.githubusercontent.com
spdx_type=https://spdx.dev/Document/v2.3
# Tag pushes sign as the workflow at the tag; workflow_dispatch republishes as the workflow at the
# branch they were dispatched on (refs/heads/main).
identity="^https://github\\.com/${repo//./\\.}/\\.github/workflows/release\\.yml@refs/(tags/${tag//./\\.}|heads/main)\$"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [[ -z $dir ]]; then
	dir=$tmp/release
	packaging/forge-release.sh download "$tag" "$dir" checksums.txt
	mapfile -t files < <(awk '{print $2}' "$dir/checksums.txt")
	packaging/forge-release.sh download "$tag" "$dir" "${files[@]}"
fi
(cd "$dir" && sha256sum -c --quiet checksums.txt)

# The provenance bundle of every release file, and the SBOM and its bundle of every Linux archive
# deps_linux_<arch>.tar.xz (the SBOM describes the binary in it; releases up to v0.6.0 have the
# bare binary deps_linux_<arch> instead).
mapfile -t files < <(awk '{print $2}' "$dir/checksums.txt")
files+=(checksums.txt)
linux=()
want=()
for f in "${files[@]}"; do
	want+=("$f.sigstore.json")
	if [[ $f == deps_linux_* ]]; then
		linux+=("$f")
		want+=("$f.spdx.json" "$f.sbom.sigstore.json")
	fi
done

out=$tmp/attest
mkdir -p "$out"
base=https://github.com/$repo/releases/download/$tag
deadline=$((SECONDS + timeout))
while :; do
	missing=()
	for a in "${want[@]}"; do
		[[ -s $out/$a ]] && continue
		curl -sSfL --retry 3 -o "$out/$a" "$base/$a" 2>/dev/null || {
			rm -f "$out/$a"
			missing+=("$a")
		}
	done
	((${#missing[@]} == 0)) && break
	if ((SECONDS >= deadline)); then
		echo "attest-from-github.sh: after ${timeout}s, $base still lacks: ${missing[*]}" >&2
		exit 1
	fi
	echo "waiting ${interval}s for ${#missing[@]} of ${#want[@]} assets on $base"
	sleep "$interval"
done

verify() { # <bundle> <type> <file>
	"$cosign" verify-blob-attestation --bundle "$1" --type "$2" \
		--certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" \
		--certificate-github-workflow-repository "$repo" "$3" || {
		echo "attest-from-github.sh: $3 does not match ${1##*/} from $base; the Gitea and GitHub builds differ, or the bundle is not from $repo's release workflow" >&2
		exit 1
	}
}
for f in "${files[@]}"; do
	verify "$out/$f.sigstore.json" slsaprovenance1 "$dir/$f"
done
for f in "${linux[@]}"; do
	verify "$out/$f.sbom.sigstore.json" "$spdx_type" "$dir/$f"
	# The SBOM asset must be the attested one.
	jq -r .dsseEnvelope.payload "$out/$f.sbom.sigstore.json" | base64 -d | jq -S .predicate >"$tmp/predicate.json"
	jq -S . "$out/$f.spdx.json" | cmp -s - "$tmp/predicate.json" || {
		echo "attest-from-github.sh: $f.spdx.json differs from the SBOM in $f.sbom.sigstore.json" >&2
		exit 1
	}
done
echo "verified ${#want[@]} attestation assets of $tag against $dir"

packaging/forge-release.sh upload "$tag" "${want[@]/#/$out/}"
