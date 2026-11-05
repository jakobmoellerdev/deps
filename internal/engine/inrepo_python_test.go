package engine

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
)

// A Python dep the index does not have (HTTP 404) is skipped when a scanned pyproject.toml
// defines it (names compare PEP 503-normalized, also Poetry's [tool.poetry] name and across
// the pep621 and poetry managers); a 404 for a name no file defines stays an error, and a
// package the index has is updated even when the repository defines it too.
func TestResolvePythonInRepositoryPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pypi/shared/json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info": null, "releases": {"1.0": [{"yanked": false}], "1.1": [{"yanked": false}]}}`))
	}))
	t.Cleanup(srv.Close)
	root, cfg := setup(t, "", map[string]string{
		"python/app/pyproject.toml":  "[project]\nname = \"app\"\ndependencies = [\"omp_rpc>=0.1.0\", \"poet>=1.0\", \"shared==1.0\", \"missing>=1.0\"]\n",
		"sdk/omp-rpc/pyproject.toml": "[project]\nname = \"omp-rpc\"\nversion = \"0.1.0\"\n",
		"libs/shared/pyproject.toml": "[project]\nname = \"Shared\"\n",
		"poetry/pyproject.toml":      "[tool.poetry]\nname = \"poet\"\n\n[tool.poetry.dependencies]\nOmp_Rpc = \"^0.1\"\n",
	})
	getenv := func(k string) string {
		if k == "DEPS_PYPI_URL" {
			return srv.URL
		}
		return ""
	}
	o := Options{
		Managers:    []manager.Manager{pep621.New(), poetry.New()},
		Datasources: map[string]Datasource{"pypi": pypi.New(srv.Client(), nil, getenv)},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "missing: package missing not found") {
		t.Errorf("errors = %v, want only the 404 of missing", res.Errors)
	}
	var skips []string
	for _, d := range res.Skipped {
		skips = append(skips, d.File+" "+d.Name+" "+d.Skip)
	}
	slices.Sort(skips)
	if want := []string{
		"poetry/pyproject.toml Omp_Rpc in-repository package (sdk/omp-rpc/pyproject.toml)",
		"python/app/pyproject.toml omp_rpc in-repository package (sdk/omp-rpc/pyproject.toml)",
		"python/app/pyproject.toml poet in-repository package (poetry/pyproject.toml)",
	}; !slices.Equal(skips, want) {
		t.Errorf("skips:\n%s\nwant:\n%s", strings.Join(skips, "\n"), strings.Join(want, "\n"))
	}
	if got, want := updateStrings(res.Updates), []string{"python/app/pyproject.toml project.dependencies shared 1.0->1.1"}; !slices.Equal(got, want) {
		t.Errorf("updates = %q, want %q", got, want)
	}
}
