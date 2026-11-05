package manager_test

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/precommit"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
)

// regexpOnly is a Match that only runs the manager's path regexp (plus its non-regexp
// exclusions), as Match did before the literal checks.
func regexpOnly(pattern string, exclude ...string) func(string) bool {
	re := regexp.MustCompile(pattern)
	return func(p string) bool {
		for _, dir := range exclude {
			if strings.HasPrefix(p, dir+"/") || strings.Contains(p, "/"+dir+"/") {
				return false
			}
		}
		return re.MatchString(p)
	}
}

var matchCases = []struct {
	name string
	m    manager.Manager
	old  func(string) bool
}{
	{"cargo", cargo.New(), regexpOnly(`(^|/)Cargo\.toml$`)},
	{"gomod", gomod.New(), regexpOnly(`(^|/)go\.mod$`)},
	{"npm", npm.New(), regexpOnly(`(^|/)package\.json$`)},
	{"pep621", pep621.New(), regexpOnly(`(^|/)pyproject\.toml$`)},
	{"pipenv", pipenv.New(), regexpOnly(`(^|/)Pipfile$`)},
	{"piprequirements", piprequirements.New(), regexpOnly(`(^|/)[\w-]*requirements([-.]\w+)?\.(txt|pip)$|\.in$`)},
	{"poetry", poetry.New(), regexpOnly(`(^|/)pyproject\.toml$`)},
	{"precommit", precommit.New(), regexpOnly(`(^|/)\.pre-commit-config\.ya?ml$`)},
	{"terragrunt", terraform.NewTerragrunt(), regexpOnly(`(^|/)terragrunt\.hcl$`, ".terragrunt-cache")},
	{"terraform-version", terraform.NewVersion(), regexpOnly(`(^|/)\.(terraform|opentofu)-version$`)},
	{"tflint", terraform.NewTFLint(), regexpOnly(`(^|/)\.tflint\.hcl$`)},
	{"nvm", toolversions.NewNvm(), regexpOnly(`(^|/)\.nvmrc$`)},
	{"nodenv", toolversions.NewNodenv(), regexpOnly(`(^|/)\.node-version$`)},
	{"asdf", toolversions.NewAsdf(), regexpOnly(`(^|/)\.tool-versions$`)},
	{"mise", toolversions.NewMise(), regexpOnly(`(^|/)(\.?mise\.toml|\.?mise/config\.toml|\.config/mise\.toml|\.config/mise/config\.toml)$`)},
}

var matchSeeds = []string{
	"Cargo.toml", "a/Cargo.toml", "xCargo.toml", "go.mod", "a/go.mod", "ago.mod", "package.json",
	"a/package.json", "pyproject.toml", "Pipfile", "a/Pipfile.lock", "requirements.txt",
	"a/dev-requirements.txt", "requirements-dev.pip", "requirements/base.txt", "requirements/base.in", "MANIFEST.in", ".pre-commit-config.yaml",
	"a/.pre-commit-config.yml", "terragrunt.hcl", ".terragrunt-cache/x/terragrunt.hcl",
	"a/.terragrunt-cache/terragrunt.hcl", ".terraform-version", "a/.opentofu-version",
	".tflint.hcl", ".nvmrc", "a/.node-version", ".tool-versions", "mise.toml", ".mise.toml",
	".mise/config.toml", ".config/mise.toml", ".config/mise/config.toml", "a/config.toml", "",
}

// repoPaths returns the slash path of every file of this repository, the mix discovery sees.
func repoPaths(tb testing.TB) []string {
	tb.Helper()
	var paths []string
	root := filepath.Join("..", "..")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return paths
}

// BenchmarkMatch runs each manager's Match over every file path of this repository.
func BenchmarkMatch(b *testing.B) {
	paths := repoPaths(b)
	for _, c := range matchCases {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				for _, p := range paths {
					c.m.Match(p)
				}
			}
		})
	}
}

func checkMatch(t *testing.T, p string) {
	t.Helper()
	for _, c := range matchCases {
		if got, want := c.m.Match(p), c.old(p); got != want {
			t.Errorf("%s: Match(%q) = %v, regexp says %v", c.name, p, got, want)
		}
	}
}

func TestMatchParity(t *testing.T) {
	for _, p := range append(repoPaths(t), matchSeeds...) {
		checkMatch(t, p)
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range matchSeeds {
		f.Add(p)
	}
	f.Fuzz(checkMatch)
}
