package nuget

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// graphTree is a repository of five projects with a lockfile each: App references Lib, Lib
// references Core, Tool imports build/Common.props and Legacy imports a file through an
// expression deps does not evaluate, which makes it opaque. src/Directory.Build.props is a
// convention import of the src subtree, build/Unused.props is imported by nobody.
func graphTree(t *testing.T) string {
	t.Helper()
	return write(t, map[string]string{
		".git/HEAD":                        "",
		lockRepo:                           "<Project />",
		"src/Directory.Build.props":        "<Project />",
		"src/App/App.csproj":               `<Project><ItemGroup><ProjectReference Include="..\Lib\Lib.csproj" /></ItemGroup></Project>`,
		"src/App/packages.lock.json":       "app\n",
		"src/Lib/Lib.csproj":               `<Project><ItemGroup><ProjectReference Include="../Core/Core.csproj" /></ItemGroup></Project>`,
		"src/Lib/packages.lock.json":       "lib\n",
		"src/Core/Core.csproj":             "<Project />",
		"src/Core/packages.lock.json":      "core\n",
		"tools/Tool/Tool.csproj":           `<Project><Import Project="$(MSBuildThisFileDirectory)../../build/Common.props" /></Project>`,
		"tools/Tool/packages.lock.json":    "tool\n",
		"legacy/Legacy/Legacy.csproj":      `<Project><Import Project="$(Custom)\x.props" /><Import Project="$(MSBuildToolsPath)\Microsoft.CSharp.targets" /></Project>`,
		"legacy/Legacy/packages.lock.json": "legacy\n",
		"build/Common.props":               "<Project />",
		"build/Unused.props":               "<Project />",
	})
}

// lockFiles is the siblings of root, the content of every packages.lock.json by path.
func lockFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, l := range (Locker{}).ScopeSiblings(root) {
		b, err := os.ReadFile(root + "/" + l)
		if err != nil {
			t.Fatal(err)
		}
		files[l] = b
	}
	return files
}

// TestRestoreTargets checks which projects a patched file is restored for: its own, those
// reaching it through a ProjectReference, those below the directory of a convention import
// and those importing a patched .props, always with the opaque Legacy project, and every
// project for a file deps cannot attribute.
func TestRestoreTargets(t *testing.T) {
	root := graphTree(t)
	files := lockFiles(t, root)
	const (
		legacy = "legacy/Legacy/packages.lock.json"
		app    = "src/App/packages.lock.json"
		core   = "src/Core/packages.lock.json"
		lib    = "src/Lib/packages.lock.json"
		tool   = "tools/Tool/packages.lock.json"
	)
	all := strings.Join([]string{legacy, app, core, lib, tool}, " ")
	for _, tt := range []struct {
		name  string
		patch []string
		want  string
	}{
		{"a leaf project and its dependents", []string{"src/Core/Core.csproj"}, legacy + " " + app + " " + core + " " + lib},
		{"a project without dependents", []string{"src/App/App.csproj"}, legacy + " " + app},
		{"a project in the middle", []string{"src/Lib/Lib.csproj"}, legacy + " " + app + " " + lib},
		{"a convention import of a subtree", []string{"src/Directory.Build.props"}, legacy + " " + app + " " + core + " " + lib},
		{"a convention import of the repository", []string{lockRepo}, all},
		{"an imported props", []string{"build/Common.props"}, legacy + " " + tool},
		{"two patched files", []string{"src/App/App.csproj", "tools/Tool/Tool.csproj"}, legacy + " " + app + " " + tool},
		{"a props no project evaluates", []string{"build/Unused.props"}, all},
		{"a file that is not MSBuild", []string{"nuget.config"}, all},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifests := map[string][]byte{}
			for _, p := range tt.patch {
				b, err := os.ReadFile(root + "/" + p)
				if err != nil {
					b = []byte("<Project />")
				}
				manifests[p] = b
			}
			if got := strings.Join(restoreTargets(root, manifests, files), " "); got != tt.want {
				t.Errorf("restoreTargets = %q, want %q", got, tt.want)
			}
		})
	}
	if got := strings.Join(restoreTargets(root, nil, files), " "); got != all {
		t.Errorf("lock file maintenance targets = %q, want %q", got, all)
	}
	if got := restoreTargets(root, map[string][]byte{"src/App/App.csproj": nil}, nil); got != nil {
		t.Errorf("without lockfiles = %q", got)
	}
}

// TestLockerProcessRestoresAffectedOnly checks that a bump in one project of a repository of
// 12 projects with a lockfile each runs one dotnet restore instead of 12, leaves the other
// lockfiles untouched, and that lock file maintenance still restores all of them.
func TestLockerProcessRestoresAffectedOnly(t *testing.T) {
	log := stubDotnet(t)
	const n = 12
	tree := map[string]string{".git/HEAD": "", lockRepo: "<Project />"}
	for p := range n {
		tree[fmt.Sprintf("src/P%02d/P%02d.csproj", p, p)] = "<Project />"
		tree[fmt.Sprintf("src/P%02d/packages.lock.json", p)] = fmt.Sprintf("p%02d\n", p)
	}
	root := write(t, tree)
	locks := lockFiles(t, root)

	out, err := Locker{}.Process(t.Context(), root, map[string][]byte{"src/P03/P03.csproj": []byte("<Project>new</Project>")}, locks)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if got := strings.Count(string(b), "args: restore "); got != 1 {
		t.Errorf("%d restores, want 1:\n%s", got, b)
	}
	if len(out) != 1 || !strings.HasPrefix(string(out["src/P03/packages.lock.json"]), "p03\nrestore src/P03/P03.csproj") {
		t.Errorf("changed lockfiles = %q", out)
	}

	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if out, err := (Locker{}).Maintain(t.Context(), root, locks); err != nil || len(out) != n {
		t.Errorf("Maintain changed %d lockfiles: %v", len(out), err)
	}
	b, _ = os.ReadFile(log)
	if got := strings.Count(string(b), "args: restore "); got != n {
		t.Errorf("%d restores of a lock file maintenance, want %d", got, n)
	}
}
