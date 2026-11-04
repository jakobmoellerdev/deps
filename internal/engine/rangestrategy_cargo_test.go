package engine

import (
	"maps"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
)

const cargoManifest = `[dependencies]
caret = "1.2"
list = ">= 0.6.8, < 0.7"
any = "*"
upper = "<2"
zero = "0.4"
`

func cargoLock(pkgs ...string) string {
	var b strings.Builder
	b.WriteString("version = 4\n")
	for _, p := range pkgs {
		name, version, _ := strings.Cut(p, "@")
		b.WriteString("\n[[package]]\nname = \"" + name + "\"\nversion = \"" + version + "\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n")
	}
	return b.String()
}

// cargoReleases lists what the crate datasource returns, including the release lines it
// derives for partial requirements.
var cargoReleases = fakeGo{
	"caret": {"1.2.0", "1.2.5", "1.3.0", "2.0.1", "1.2", "1.3", "2.0", "1", "2"},
	"list":  {"0.6.8", "0.6.12", "0.7.1"},
	"any":   {"1.0.0", "1.1.0"},
	"upper": {"1.5.0", "1.9.0", "2.3.0"},
	"zero":  {"0.4.1", "0.4.3", "0.5.0", "0.4", "0.5", "0"},
}

// Cargo requirements under rangeStrategy: Cargo.lock gives the locked versions; a bare `1.2`
// is ^1.2, and floorless requirements (`*`, `<2`) only ever move the lock under bump too.
func TestRangeStrategyCargo(t *testing.T) {
	lock := cargoLock("caret@1.2.5", "list@0.6.8", "any@1.0.0", "upper@1.5.0", "zero@0.4.1")
	for _, tt := range []struct {
		name, cfg string
		files     map[string]string
		want      []string
	}{
		{
			name: "auto with Cargo.lock", files: map[string]string{"Cargo.lock": lock},
			want: []string{
				"caret 1.2.5->1.3.0 minor lockfile", "caret 1.2->2.0.1 major", // the manifest gets 2.0
				"list 0.6.8->0.6.12 patch lockfile", "list 0.6.8->0.7.1 major", // 0.6 -> 0.7 breaks in Cargo
				"any 1.0.0->1.1.0 minor lockfile",
				"upper 1.5.0->1.9.0 minor lockfile", "upper 1.5.0->2.3.0 major",
				"zero 0.4.1->0.4.3 patch lockfile", "zero 0.4->0.5.0 major",
			},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n", files: map[string]string{"Cargo.lock": lock},
			want: []string{
				"caret 1.2->1.3 minor", "caret 1.2->2.0 major", // partial: derived release lines
				"list 0.6.8->0.6.12 patch", "list 0.6.8->0.7.1 major",
				"any 1.0.0->1.1.0 minor lockfile", // nothing to bump
				"upper 1.5.0->1.9.0 minor lockfile", "upper 1.5.0->2.3.0 major",
				"zero 0.4->0.5 major",
			},
		},
		{
			name: "no Cargo.lock",
			want: []string{"caret 1.2->2.0 major", "list 0.6.8->0.7.1 major", "zero 0.4->0.5 major"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"Cargo.toml": cargoManifest}
			maps.Copy(files, tt.files)
			root, cfg := setup(t, tt.cfg, files)
			o := Options{Managers: []manager.Manager{cargo.New()}, Datasources: map[string]Datasource{"crate": cargoReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			if got := renderUpdates(res.Updates); got != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", got, strings.Join(tt.want, "\n"))
			}
		})
	}
}
