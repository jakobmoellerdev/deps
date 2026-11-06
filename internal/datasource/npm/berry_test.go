package npmds

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// yarn 2 and later is published as @yarnpkg/cli-dist; the yarn package stops at 1.x.
func TestPackageNameYarnBerry(t *testing.T) {
	for _, c := range []struct {
		d    dep.Dep
		want string
	}{
		{dep.Dep{Manager: npm.Name, DepType: npm.DepTypePackageManager, Name: "yarn", Version: "4.5.0"}, berryPackage},
		{dep.Dep{Manager: npm.Name, DepType: npm.DepTypeEngines, Name: "yarn", Version: "4"}, berryPackage},
		{dep.Dep{Manager: npm.Name, DepType: npm.DepTypeVolta, Name: "yarn", Version: "3.8.7"}, berryPackage},
		{dep.Dep{Manager: npm.Name, DepType: npm.DepTypePackageManager, Name: "yarn", Version: "1.22.22"}, "yarn"},
		{dep.Dep{Manager: npm.Name, DepType: "dependencies", Name: "yarn", Version: "4.5.0"}, "yarn"},
		{dep.Dep{Manager: "custom", DepType: npm.DepTypePackageManager, Name: "yarn", Version: "4.5.0"}, "yarn"},
		{dep.Dep{Manager: npm.Name, DepType: npm.DepTypePackageManager, Name: "pnpm", Version: "9.0.0"}, "pnpm"},
	} {
		if got := packageName(c.d); got != c.want {
			t.Errorf("%+v: %q, want %q", c.d, got, c.want)
		}
	}
}
