package pypi

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pypi datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "package name; PEP 503-normalized for lookups (`zope.interface` → `zope-interface`)",
	Endpoints: "PyPI: `GET <DEPS_PYPI_URL or https://pypi.org>/pypi/<name>/json`; other indexes: `GET <index>/<name>/` with " +
		"`Accept: application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.2, text/html;q=0.01` " +
		"(simple API: JSON, PEP 691/700, else HTML, PEP 503; an HTTP 406 is retried with `Accept: text/html`); once per (index, name); " +
		"versions without files or with every file yanked dropped; 404 → `package <name> not found`",
	Auth:         "userinfo of the index URL (`https://user:pass@host/simple`) sent as basic auth, redacted in errors and logs",
	ReleaseTimes: "earliest file upload time of the version on any of the dep's indexes (`upload_time_iso_8601`, `upload-time`, HTML `data-upload-time`)",
	Notes: []refdoc.Section{{
		Title: "Python index selection",
		Body:  "deps looks up a Python package on its primary index and on its extra indexes, and merges the releases.",
		Items: []string{
			"Primary index: the one declared by the manifest (requirements `--index-url`, a uv `default = true` index, a Poetry primary source, the first Pipfile `[[source]]`, or the index a dep is pinned to) wins, then `PIP_INDEX_URL`, then PyPI.",
			"Extra indexes: those declared by the manifest (requirements `--extra-index-url`; uv `[[tool.uv.index]]` entries that are neither `default` nor `explicit`; Poetry `priority = \"supplemental\"` sources; the other Pipfile `[[source]]`s with `[pipenv] install_search_all_sources = true`; pdm's other `[[tool.pdm.source]]`s), then every URL of the space-separated `PIP_EXTRA_INDEX_URL`. A dep pinned to an index (uv `index = …`, Poetry `source = …`, Pipfile `index = …`) has none.",
			"The releases of all indexes are merged: each version once, with the earliest upload time; an index without the package is fine as long as one has it, any other error fails the dep. This is pip's behavior; uv's default `first-index` strategy, which stops at the first index with the package, may see fewer versions.",
			"PyPI, also when named as `https://pypi.org/simple`, is asked through its JSON API. Its `info.project_urls` (`Source`, `Source Code`, `Repository`, else `Code`, `GitHub` or `Homepage` on github.com, gitlab.com or codeberg.org), else a forge `info.home_page`, names the source repository for release notes; it describes the latest release.",
			"Any other index is asked for the JSON simple API (PEP 691) and may answer HTML (PEP 503, as download.pytorch.org does): anchors give the filename (text, else the last `href` path segment), the `#sha256=` digest, PEP 592 `data-yanked` and PEP 503 `data-requires-python`. Other content types fail with `unsupported content type`. Files are mapped to versions by filename (wheels, sdists, eggs).",
		},
	}, {
		Title: "Python version compatibility",
		Body:  "Like Renovate, deps drops the releases a project cannot install on every Python version it supports.",
		Items: []string{
			"The project's Python constraint comes from `[project] requires-python` (pep621), `python` in `[tool.poetry.dependencies]` else `[project] requires-python` (poetry), or Pipfile `[requires]` (`python_full_version` exactly, else `python_version` as `==<version>.*`); requirements files have none.",
			"A release is kept when the `requires-python` shared by all of its files that are not yanked (JSON API `requires_python`, simple API `requires-python`, HTML `data-requires-python`) admits every final Python release the project's constraint admits; a release whose files have none or disagree is kept. Specifier sets follow PEP 440 (`>=3.8,!=3.9.*,<4`, `~=3.10`, `==3.11.*`) and Poetry (`^3.9`, `~3.9`, `>=3.9 <4`, `||`).",
			"An invalid project constraint is ignored with a warning; an invalid `requires-python` keeps its release.",
		},
	}},
}
