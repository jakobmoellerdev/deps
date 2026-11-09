# shellcheck shell=bash
# Helpers shared by the release build scripts (packaging/archive.sh, packaging/nfpm/build.sh).
# Source it after `set -euo pipefail`; BIN, the directory of the release binaries
# deps_<os>_<arch>[.exe] (task release:build writes them to dist/bin), must be set.

# export_source_date_epoch <tag> exports SOURCE_DATE_EPOCH, unless it is already set, as the
# commit time of the tag, else of HEAD. Every mtime in the archives and packages is this time.
export_source_date_epoch() {
	if [[ -z ${SOURCE_DATE_EPOCH:-} ]]; then
		SOURCE_DATE_EPOCH=$(git log -1 --format=%ct "refs/tags/$1^{commit}" 2>/dev/null || git log -1 --format=%ct HEAD)
	fi
	export SOURCE_DATE_EPOCH
}

# check_binaries fails unless both Linux release binaries exist.
check_binaries() {
	local goarch
	for goarch in amd64 arm64; do
		[[ -f $BIN/deps_linux_$goarch ]] || {
			echo "${0##*/}: $BIN/deps_linux_$goarch is missing; run task release:build first" >&2
			return 1
		}
	done
}

# write_completions <dir> writes <dir>/bash, <dir>/zsh, <dir>/fish and <dir>/powershell. They come
# from the release binary that runs on this machine and are the same for every platform.
write_completions() {
	local host shell
	case "$(uname -m)" in
	x86_64 | amd64) host=amd64 ;;
	aarch64 | arm64) host=arm64 ;;
	*) echo "${0##*/}: unsupported host architecture $(uname -m)" >&2 && return 1 ;;
	esac
	if [[ $(uname -s) != Linux ]]; then
		echo "${0##*/}: needs Linux to run $BIN/deps_linux_$host for the completions" >&2
		return 1
	fi
	mkdir -p "$1"
	for shell in bash zsh fish powershell; do
		"$BIN/deps_linux_$host" completion "$shell" >"$1/$shell"
	done
}
