package renovate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// converted are the top-level and rule keys the translator handles by hand that have no row
// in config.RenovateOptions.
var converted = []string{"pin", "registryUrls", "replacementNameTemplate", "replacementVersionTemplate", "rollback", "stabilityDays"}

// TestKeyMapping keeps the translator on config.RenovateOptions: every top-level or rule key
// it reads has a row there or is listed in converted, and the keys it copies through the
// table are not also translated by hand.
func TestKeyMapping(t *testing.T) {
	table := map[string]bool{}
	for _, o := range config.RenovateOptions {
		for _, k := range o.Keys {
			table[k] = true
		}
	}
	read, cases := translatorKeys(t)
	for _, k := range converted {
		if table[k] {
			t.Errorf("converted %s has a row in config.RenovateOptions", k)
		}
		if !read[k] {
			t.Errorf("converted %s is not read by the translator", k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(read)) {
		if !table[k] && !slices.Contains(converted, k) {
			t.Errorf("the translator reads %s, which has no row in config.RenovateOptions and is not in converted", k)
		}
	}
	for k := range cases {
		if _, ok := ruleCopies[k]; ok {
			t.Errorf("%s is copied through config.RenovateOptions and translated by hand", k)
		}
	}
	for _, k := range []string{"groupName", "allowedVersions", "dependencyDashboardApproval", "draftPR"} {
		if _, ok := ruleCopies[k]; !ok {
			t.Errorf("ruleCopies lacks %s", k)
		}
	}
	for _, k := range []string{"gitAuthor", "dependencyDashboard", "timezone", "automerge"} {
		if _, ok := topCopies[k]; !ok {
			t.Errorf("topCopies lacks %s", k)
		}
	}
}

// translatorKeys returns the Renovate keys the top-level and rule translation (translate,
// rulesFrom, rule, effects) reads, and the case labels of their `switch k` statements: string
// literals passed to get and copied, used as index of cfg, r and done, ranged over in a
// literal list whose loop reads one of those, or as case labels.
func translatorKeys(t *testing.T) (read, cases map[string]bool) {
	t.Helper()
	read, cases = map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	funcs := map[string]bool{"translate": true, "rulesFrom": true, "rule": true, "effects": true}
	for _, name := range []string{"translate.go", "rules.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !funcs[fd.Name.Name] {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "get" || id.Name == "copied") && len(n.Args) > 0 {
						addLit(read, n.Args[0])
					}
				case *ast.IndexExpr:
					if id, ok := n.X.(*ast.Ident); ok && (id.Name == "cfg" || id.Name == "r" || id.Name == "done") {
						addLit(read, n.Index)
					}
				case *ast.RangeStmt:
					if cl, ok := n.X.(*ast.CompositeLit); ok && readsKey(n.Body) {
						ast.Inspect(cl, func(n ast.Node) bool { addLit(read, n); return true })
					}
				case *ast.SwitchStmt:
					if id, ok := n.Tag.(*ast.Ident); ok && id.Name == "k" {
						for _, s := range n.Body.List {
							for _, e := range s.(*ast.CaseClause).List {
								addLit(read, e)
								addLit(cases, e)
							}
						}
					}
				}
				return true
			})
		}
	}
	return read, cases
}

// readsKey reports whether n calls get or copied or indexes cfg or done.
func readsKey(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "get" || id.Name == "copied") {
				found = true
			}
		case *ast.IndexExpr:
			if id, ok := n.X.(*ast.Ident); ok && (id.Name == "cfg" || id.Name == "done") {
				found = true
			}
		}
		return !found
	})
	return found
}

func addLit(m map[string]bool, n ast.Node) {
	if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
		if s, err := strconv.Unquote(l.Value); err == nil {
			m[s] = true
		}
	}
}
