package copier

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the copier manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates projects generated from [Copier](https://copier.readthedocs.io) templates to newer template tags: the `_commit` of the answers file names the template version, `_src_path` the template repository. " +
		"`apply` and `run` let `copier update` apply the template's changes to the project; `plan` shows only the new `_commit`.",
	Files: []string{"`.copier-answers.yml` and `.copier-answers.<name>.yml` (also `.yaml`) at any depth, as Renovate matches them"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "the template: `_src_path` (`https://…`, `git+https://…`, `git@host:owner/repo`, `ssh://…` and Copier's `gh:owner/repo`/`gl:owner/repo` aliases) at the tag `_commit`; named after the repository path (`owner/repo`), looked up over https on any git host", Versioning: "`pep440`", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocal, When: "`_src_path` is a local path or bundle (`../template`, `/srv/tpl`, `~/tpl`, `file://…`)"},
		{Reason: skipUnsupported, When: "`_src_path` is no git URL deps can look up (`hg+https://…`)"},
		{Reason: skipCommit, When: "`_commit` is the commit Copier records for a template without tags (`3f2a1bc`) or between tags (`v1.2.3-4-gabc1234`)"},
		{Reason: skipVersion, When: "`_commit` is no PEP 440 version (`main`)"},
		{Reason: skipLocate, When: "`_commit` is written with escapes or as a block scalar"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"Template tags are ordered by PEP 440, as Copier orders them; a `v` prefix is kept.",
			"Answers files without a string `_src_path` and `_commit` have no dep.",
		}},
		{Title: "Template update", Items: []string{
			"`apply` and `run` copy the project (the answers file's directory; the files `git ls-files` lists as tracked or untracked and not ignored, outside a repository every file but `.git`) into a temporary git repository, commit it, and run `copier update --skip-answered --defaults --answers-file <file> --vcs-ref <new tag>` there, as Renovate does; several answers files of one directory are updated one after another. " +
				"Every file the update changed, added or deleted joins the update, including the answers file Copier rewrites; the checkout is not touched until then.",
			"`copier` from `PATH` runs, else `uvx --from copier copier`; without either the update fails with `copier not found (needed to update from Copier templates)`. Copier clones the template with git and the runner's git configuration and credentials; deps passes none and disables git's terminal prompts.",
			"deps never passes `--trust`, as Renovate without `allowScripts`: a template using unsafe features (`_tasks`, `_migrations`, `_jinja_extensions`) fails with Copier's `UnsafeTemplateError` unless Copier's own settings trust it (`trust:` in `~/.config/copier/settings.yml` or `$COPIER_SETTINGS_PATH`); trusted templates run their tasks on the runner.",
			"An update that leaves merge conflicts (Copier's inline conflict markers) fails and names the files; Renovate commits them and comments on the PR. Fix them with `copier update` by hand.",
			"A file Copier creates is written with mode 0644 and an existing one keeps its mode: executable bits a template adds or changes are not carried over. An update that turns a file into a symlink fails.",
		}},
	},
}
