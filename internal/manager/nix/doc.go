package nix

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the nix manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Moves the inputs of Nix flakes to the newest commit of the branch they track (a `flake.lock` update with `nix flake update <input>`), or to the newest version tag when their ref is one, as Renovate's `nix` manager does. " +
		"Renovate's manager is an opt-in beta; deps enables it like every other manager.",
	Files: []string{"`flake.nix` at any depth (its lock: the `flake.lock` next to it; without one the inputs are skipped)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeInput, What: "the inputs of `flake.nix` (`inputs.<name>.url = …;`, `inputs = { <name>.url = …; };`, or the attribute form `{ type = \"github\"; owner = …; repo = …; ref = …; rev = …; }`) that `flake.lock` locks to a commit: " +
			"`github:`, `gitlab:`, `sourcehut:`, `git+https:`/`git+ssh:` references and forge archive tarballs locked through the lockable tarball protocol (Gitea, Forgejo: `https://<host>/<owner>/<repo>/archive/<ref>.tar.gz`). " +
			"The name is the input name (Renovate's depName), the source the repository's `https://` URL, the version the ref (none: `HEAD`, the default branch; `refs/heads/` and `refs/tags/` are dropped), the digest the rev `flake.nix` pins, else the commit `flake.lock` locks",
			Datasource: "`git-refs` (a branch); `git-tags` when the ref is a version tag", Versioning: "`tag`", Datasources: []string{dsRefs, dsTags}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoLock, When: "the `flake.nix` has no `flake.lock` next to it"},
		{Reason: skipUnreadable, When: "`flake.lock` is not JSON or has no root node (also reported as an error)"},
		{Reason: skipNotLocked, When: "`flake.lock` has no root input of that name, or the input's node has no `locked`/`original` reference (run `nix flake lock`)"},
		{Reason: skipFollows, When: "`flake.lock` records the input as a `follows` of another input"},
		{Reason: skipIndirect, When: "an indirect reference (`nixpkgs`, `flake:nixpkgs/nixos-24.05`) the flake registry resolves"},
		{Reason: skipPath, When: "a `path:` input, or a `git+file:` repository"},
		{Reason: skipNoRev, When: "`flake.lock` locks the input without a commit: a tarball or file URL without the lockable tarball protocol (GitHub archive URLs, FlakeHub)"},
		{Reason: skipURL, When: "a git URL that is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: skipChannel, When: "a NixOS channel tarball (`https://channels.nixos.org/<channel>/nixexprs.tar.xz`): a channel advances only after Hydra passes, so its commit lags the `<channel>` branch of `NixOS/nixpkgs` and `nix flake update`, which locks the channel's commit, never reaches the branch's head. As in Renovate, which has no digest updates for inputs without a pinned rev, [lockFileMaintenance](configuration.md#lockfilemaintenance) moves it"},
		{Reason: skipTarball, When: "a tarball URL that is neither a NixOS channel nor a `…/<owner>/<repo>/archive/<ref>.tar.gz` archive"},
		{Reason: skipRefSpan, When: "a version-tag ref `flake.nix` does not write verbatim (percent-encoded, escaped or interpolated), or that differs from `flake.lock`'s"},
		{Reason: skipRevSpan, When: "a rev `flake.nix` pins but does not write verbatim, or that differs from `flake.lock`'s"},
		{Reason: skipUnsupported + "<type>", When: "a `file`, `hg` or other input type without a git repository"},
	},
	Sections: []refdoc.Section{
		{Title: "Updates", Items: []string{
			"An input tracking a branch (`github:NixOS/nixpkgs/nixos-unstable`, `git+https://…?ref=main`, or no ref: the default branch) gets a `digest` update when the branch's commit differs from the one `flake.lock` locks; `flake.nix` is unchanged and only `flake.lock` moves. As for git-submodules, such PRs have no release notes.",
			"An input whose ref is a version tag (`github:hyprwm/Hyprland/v0.40.0`, `?ref=v1.2.3`, `?ref=refs/tags/v1.2.3`) gets updates to newer tags like any `git-tags` dep, with their release notes: the tag is rewritten in `flake.nix`.",
			"An input pinned to a commit (`github:NixOS/nixpkgs/<rev>`, `?rev=<rev>`, `rev = \"<rev>\";`) gets `digest` updates to the newest commit of its branch (`HEAD` without a ref), as in Renovate: the rev is rewritten in `flake.nix`. To keep such a pin, ignore the input with a rule (`{managers: [nix], names: [nixpkgs-regression], ignore: true}`).",
			"Inputs are read from `flake.nix` as the attribute set Nix evaluates (comments, strings, `let … in` and other expressions skipped) and matched with `flake.lock`'s root inputs by name; what `flake.lock` records as the input's original reference is authoritative. Nested `inputs.<name>.inputs.<sub>.follows` and inputs that only `follows` are no deps.",
			"Renovate's `nixpkgs` versioning, which moves `nixos-24.05` to the next release branch `nixos-24.11`, is not implemented: NixOS release branches get digest updates only.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run nix, and prints `note    flake.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `flake.lock` next to a scanned `flake.nix` is refreshed with `nix flake update` in the temporary copy, which moves every input, transitive ones included, to the newest commit its reference admits, as Renovate does. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `flake.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a `flake.nix` is the `flake.lock` next to it; without one its inputs are skipped.\n" +
	"2. **Temporary copy.** The directory is mirrored to a temp dir: flake.nix (patched) and flake.lock are copied, every other entry but `.git` is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so relative path inputs resolve as in the checkout. nix gets the copy as a `path:` flake, so it does not look for a git repository around it.\n" +
	"3. **Update.** `nix --extra-experimental-features 'nix-command flakes' flake update <input>… --flake path:<copy>` re-locks every updated input, which needs Nix 2.19 or later: a branch input moves to the branch's newest commit at that time (which can be newer than the proposed digest, as in Renovate), an input whose ref or rev `flake.nix` now names moves to it. Other inputs keep their lock.\n" +
	"4. **Tool.** `nix` must be on `PATH`, e.g. from `cachix/install-nix-action` or `DeterminateSystems/nix-installer-action`; its own configuration (`NIX_CONFIG`, `nix.conf`) is in effect. deps adds its github.com token (the platform token on GitHub, else `DEPS_GITHUB_COM_TOKEN`) as `extra-access-tokens` to `NIX_CONFIG`, as Renovate passes `--extra-access-tokens`. Without nix, the flake.nix files fail with `nix not found (needed to update flake.lock)`.\n" +
	"5. **Failure.** A failed command reports `nix flake update in <dir>: <last 20 output lines>`, and the flake.nix's changes are dropped."
