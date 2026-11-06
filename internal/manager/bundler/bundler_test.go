package bundler

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// describe renders the fields of d that Extract sets; RefSpan must point at Range.
func describe(src []byte, d dep.Dep) string {
	if ref := d.RefSpan; ref != (dep.Span{}) && string(src[ref.Start:ref.End]) != d.Range {
		return fmt.Sprintf("%d %s: RefSpan %q is not Range %q", d.Line, d.Name, src[ref.Start:ref.End], d.Range)
	}
	s := fmt.Sprintf("%d %s %s range=%s constraint=%q version=%q source=%s", d.Line, d.Name, d.DepType, d.Range, d.Constraint, d.Version, d.Source)
	if d.RangeFixed {
		s += " fixed"
	}
	if d.Skip != "" {
		s += " skip=" + d.Skip
	}
	return s
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"Gemfile": true, "apps/ios/Gemfile": true, "Gemfile.lock": false, "MyGemfile": false, "Gemfile/x": false,
		"gems.rb": true, "sub/gems.rb": true, "gems.locked": false, "mygems.rb": false,
		"devise.gemspec": true, "lib/x/x.gemspec": true, ".gemspec": false, "x.gemspec.erb": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%s) = %v", p, !want)
		}
	}
}

// testdata/activesupport.gemspec is rails/rails' (1e235cee) and testdata/devise.gemspec
// heartcombo/devise's (05811fb8), verbatim, 2026-11-05.
func TestExtractGemspec(t *testing.T) {
	for file, want := range map[string][]string{
		"activesupport.gemspec": {
			`12 ruby ruby range=>= 3.3.5 constraint="" version="" source= skip=` + skipRequiredRuby,
			`37 i18n runtime range=">= 1.6", "< 2" constraint=">= 1.6, < 2" version="1.6" source=`,
			`38 tzinfo runtime range="~> 2.0", ">= 2.0.5" constraint="~> 2.0, >= 2.0.5" version="2.0" source=`,
			`39 concurrent-ruby runtime range="~> 1.0", ">= 1.3.1" constraint="~> 1.0, >= 1.3.1" version="1.0" source=`,
			`40 connection_pool runtime range=">= 2.2.5" constraint=">= 2.2.5" version="2.2.5" source=`,
			`41 minitest runtime range=">= 5.1" constraint=">= 5.1" version="5.1" source=`,
			`42 base64 runtime range=>= 0 constraint="" version="" source= fixed`,
			`43 drb runtime range=>= 0 constraint="" version="" source= fixed`,
			`44 bigdecimal runtime range=>= 0 constraint="" version="" source= fixed`,
			`45 json runtime range=>= 0 constraint="" version="" source= fixed`,
			`46 psych runtime range=">= 4" constraint=">= 4" version="4" source=`,
			`47 logger runtime range=">= 1.4.2" constraint=">= 1.4.2" version="1.4.2" source=`,
			`48 securerandom runtime range=">= 0.3" constraint=">= 0.3" version="0.3" source=`,
			`49 uri runtime range=">= 0.13.1" constraint=">= 0.13.1" version="0.13.1" source=`,
			`50 ractor-dispatch runtime range=">= 0.3.0" constraint=">= 0.3.0" version="0.3.0" source=`,
		},
		"devise.gemspec": {
			`28 ruby ruby range=>= 2.7.0 constraint="" version="" source= skip=` + skipRequiredRuby,
			`30 warden runtime range="~> 1.2.3" constraint="~> 1.2.3" version="1.2.3" source=`,
			`31 orm_adapter runtime range="~> 0.1" constraint="~> 0.1" version="0.1" source=`,
			`32 bcrypt runtime range="~> 3.0" constraint="~> 3.0" version="3.0" source=`,
			`33 railties runtime range=">= 7.0" constraint=">= 7.0" version="7.0" source=`,
			`34 responders runtime range=>= 0 constraint="" version="" source= fixed`,
		},
		"shapes.gemspec": {
			`3 nokogiri runtime range="~> 1.18" constraint="~> 1.18" version="1.18" source=`,
			`4 rspec development range="~> 3.13" constraint="~> 3.13" version="3.13" source=`,
			`6 faraday runtime range= constraint="" version="" source= skip=` + skipNonLiteral,
			`7 rack runtime range=">= 2.2", "< 4" constraint=">= 2.2, < 4" version="2.2" source=`,
			`8 ruby ruby range= constraint="" version="" source= skip=` + skipRequiredRuby,
		},
	} {
		src := read(t, "testdata/"+file)
		deps, err := New().Extract("gem/"+file, src, manager.Env{})
		var got []string
		for _, d := range deps {
			got = append(got, describe(src, d))
			if d.File != "gem/"+file || d.Manager != Name {
				t.Errorf("%s: %+v", file, d)
			}
		}
		if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); err != nil || g != w {
			t.Errorf("%s:\n%s\nwant:\n%s", file, g, w)
		}
	}
	// A gemspec requirement is rewritten like a Gemfile's.
	src := read(t, "testdata/devise.gemspec")
	deps, _ := New().Extract("devise.gemspec", src, manager.Env{})
	edits, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := patch.Apply(src, edits); !strings.Contains(string(out), `s.add_dependency("orm_adapter", "~> 1.3")`) {
		t.Errorf("edit = %v", edits)
	}
}

// testdata/openclaw holds openclaw/openclaw's apps/ios/Gemfile and Gemfile.lock verbatim
// (deecc08f, 2026-11-05); apps/android has the same files.
func TestExtractOpenclaw(t *testing.T) {
	src := read(t, "testdata/openclaw/Gemfile")
	deps, err := New().Extract("apps/ios/Gemfile", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	r, d := deps[0], deps[1]
	if got, want := describe(src, r), `3 ruby ruby range="4.0.7" constraint="4.0.7" version="4.0.7" source=`; got != want {
		t.Errorf("ruby = %s\nwant   %s", got, want)
	}
	if r.Manager != Name || r.Datasource != RubyDatasource || r.Versioning != "ruby" {
		t.Errorf("ruby = %+v", r)
	}
	if got, want := describe(src, d), `5 fastlane dependencies range="2.241.2" constraint="2.241.2" version="2.241.2" source=https://rubygems.org`; got != want {
		t.Errorf("dep = %s\nwant  %s", got, want)
	}
	if d.Manager != Name || d.Datasource != "rubygems" || d.Versioning != "ruby" || d.File != "apps/ios/Gemfile" {
		t.Errorf("dep = %+v", d)
	}
}

func TestExtractRuby(t *testing.T) {
	for src, want := range map[string]string{
		"ruby \"3.4.7\"\n":                            `1 ruby ruby range="3.4.7" constraint="3.4.7" version="3.4.7" source=`,
		"ruby('~> 3.4.0')\n":                          `1 ruby ruby range='~> 3.4.0' constraint="~> 3.4.0" version="3.4.0" source=`,
		"ruby \">= 3.2\", \"< 5\"\n":                  `1 ruby ruby range=">= 3.2", "< 5" constraint=">= 3.2, < 5" version="3.2" source=`,
		"ruby \"3.3.0\"\ngem \"a\"\nruby \"3.4.1\"\n": `2 a dependencies range=>= 0 constraint="" version="" source= fixed` + "\n" + `3 ruby ruby range="3.4.1" constraint="3.4.1" version="3.4.1" source=`,
		"ruby \"3.4.7\", :engine => \"ruby\", :engine_version => \"3.4.7\"\n": `1 ruby ruby range="3.4.7" constraint="3.4.7" version="3.4.7" source=`,
		"ruby \"3.4.7\", engine: \"jruby\", engine_version: \"9.4.8.0\"\n":    `1 ruby ruby range= constraint="" version="" source= skip=` + skipRubyEngine,
		"ruby \"3.3.6\", patchlevel: \"108\"\n":                               `1 ruby ruby range= constraint="" version="" source= skip=` + skipRubyPatchlevel,
		"ruby file: \".tool-versions\"\n":                                     `1 ruby ruby range= constraint="" version="" source= skip=` + skipRubyFile,
		"ruby File.read(\".ruby-version\").strip\n":                           `1 ruby ruby range= constraint="" version="" source= skip=` + skipNonLiteral,
		"ruby RUBY_VERSION\n":                                                 `1 ruby ruby range= constraint="" version="" source= skip=` + skipNonLiteral,
		"ruby \"< 4\"\n":                                                      `1 ruby ruby range="< 4" constraint="< 4" version="" source= skip=` + skipRubyNoFloor,
		"ruby\n":                                                              `1 ruby ruby range= constraint="" version="" source= skip=` + skipRubyNoFloor,
		"platforms :ruby do\n  gem \"a\"\nend\n":                              `2 a dependencies range=>= 0 constraint="" version="" source= fixed`,
	} {
		deps, err := New().Extract("Gemfile", []byte(src), manager.Env{})
		var got []string
		for _, d := range deps {
			got = append(got, describe([]byte(src), d))
		}
		if err != nil || strings.Join(got, "\n") != want {
			t.Errorf("%q:\n%s\nwant %s (%v)", src, strings.Join(got, "\n"), want, err)
		}
	}
	// The directive's version is rewritten like a gem requirement.
	src := []byte("source \"https://rubygems.org\"\nruby '~> 3.4.0' # comment\n")
	deps, _ := New().Extract("Gemfile", src, manager.Env{})
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "4.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := patch.Apply(src, edits); err != nil || string(out) != "source \"https://rubygems.org\"\nruby '~> 4.0.9' # comment\n" {
		t.Errorf("edit = %q, %v", out, err)
	}
}

func TestExtractShapes(t *testing.T) {
	src := read(t, "testdata/Gemfile")
	deps, err := New().Extract("Gemfile", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	const rg = "https://rubygems.org"
	want := []string{
		`6 ruby ruby range= constraint="" version="" source= skip=` + skipRubyFile,
		`10 propshaft dependencies range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`11 rails dependencies range='~> 8.1.0' constraint="~> 8.1.0" version="8.1.0" source=` + rg,
		`12 haml-rails dependencies range='~>3.0' constraint="~>3.0" version="3.0" source=` + rg,
		`13 aws-sdk-s3 dependencies range='~> 1.123' constraint="~> 1.123" version="1.123" source=` + rg,
		`14 fog-core dependencies range='<= 2.6.0' constraint="<= 2.6.0" version="" source=` + rg,
		`15 omniauth-cas dependencies range='~> 3.0.0.beta.1' constraint="~> 3.0.0.beta.1" version="3.0.0.beta.1" source=` + rg,
		`16 bootsnap dependencies range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`17 fastlane dependencies range="2.241.2" constraint="2.241.2" version="2.241.2" source=` + rg,
		`18 rack dependencies range=">= 2.2.8", "< 4" constraint=">= 2.2.8, < 4" version="2.2.8" source=` + rg,
		`19 nokogiri dependencies range=">= 1.16", "< 2" constraint=">= 1.16, < 2" version="1.16" source=` + rg,
		`20 puma dependencies range="= 6.4.2" constraint="= 6.4.2" version="6.4.2" source=` + rg,
		`21 sidekiq dependencies range='>= 7.0' constraint=">= 7.0" version="7.0" source=` + rg,
		`22 webpush dependencies range='~> 1.1' constraint="~> 1.1" version="1.1" source=` + rg,
		`26 pg dependencies range= constraint="" version="" source=` + rg + ` skip=non-literal requirement`,
		`27 redis dependencies range= constraint="" version="" source=` + rg + ` skip=non-literal requirement`,
		`28 foo dependencies range= constraint="" version="" source=` + rg + ` skip=non-literal requirement`,
		`29 x dependencies range= constraint="" version="latest" source=` + rg + ` skip=unsupported requirement`,
		`30 debug dependencies range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`31 rubocop group range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`32 raindrops dependencies range= constraint="" version="" source=` + rg + ` skip=git dependency`,
		`33 rails-html dependencies range= constraint="" version="" source=` + rg + ` skip=git dependency`,
		`34 mine dependencies range= constraint="" version="" source=` + rg + ` skip=git dependency`,
		`35 migrations-core dependencies range= constraint="" version="" source=` + rg + ` skip=path dependency`,
		`36 private-gem dependencies range="~> 2.0" constraint="~> 2.0" version="2.0" source=https://gems.example.com`,
		`37 env-source dependencies range= constraint="" version="" source= skip=non-literal source`,
		`38 lt dependencies range="< 2" constraint="< 2" version="" source=` + rg,
		`45 devise_pam_authenticatable2 group range='~> 9.2' constraint="~> 9.2" version="9.2" source=` + rg,
		`49 rspec-rails group range="~> 7.0" constraint="~> 7.0" version="7.0" source=` + rg,
		`51 byebug group range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`56 sidekiq-pro dependencies range="~> 7.3" constraint="~> 7.3" version="7.3" source=https://gems.contribsys.com/`,
		`60 activesupport dependencies range= constraint="" version="" source=` + rg + ` skip=git dependency`,
		`64 engine dependencies range= constraint="" version="" source=` + rg + ` skip=path dependency`,
		`68 discourse_dev_assets dependencies range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`71 faker group range="~> 2.16" constraint="~> 2.16" version="2.16" source=` + rg,
		`76 rb-fsevent dependencies range=>= 0 constraint="" version="" source=` + rg + ` fixed`,
		`79 heredoc dependencies range= constraint="" version="" source=` + rg + ` skip=non-literal requirement`,
		`83 last dependencies range='1.0.0' constraint="1.0.0" version="1.0.0" source=` + rg,
		`83 same-line dependencies range="2.0" constraint="2.0" version="2.0" source=` + rg,
	}
	var got []string
	for _, d := range deps {
		got = append(got, describe(src, d))
	}
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		t.Errorf("deps:\n%s\nwant:\n%s", g, w)
	}
}

func TestExtractSources(t *testing.T) {
	for _, tt := range []struct{ src, want string }{
		{"gem \"a\"\n", ""}, // no source: rubygems.org
		{"source :rubygems\ngem \"a\"\n", "https://rubygems.org"},
		{"gem \"a\"\nsource \"https://one.example\"\nsource 'https://two.example'\n", "https://one.example https://two.example"},
		{"source ENV['GEMS']\ngem \"a\"\n", ""},
		{"source \"https://x.example\" do\nend\ngem \"a\"\n", ""},
		{"source(\"https://p.example\")\ngem 'a', source: 'https://own.example'\n", "https://own.example"},
	} {
		deps, err := New().Extract("Gemfile", []byte(tt.src), manager.Env{})
		if err != nil || len(deps) != 1 || deps[0].Source != tt.want {
			t.Errorf("%q: deps = %+v, %v, want source %q", tt.src, deps, err, tt.want)
		}
	}
}

// TestExtractHashRocket covers unspaced `:key=>value` pairs, which must not lex as setter
// symbols (`:require=` followed by `>`).
func TestExtractHashRocket(t *testing.T) {
	for src, want := range map[string]string{
		"gem 'rails', '~> 6.0', :require=>false\n":     `1 rails dependencies range='~> 6.0' constraint="~> 6.0" version="6.0" source=`,
		"gem 'x', '~> 1.0', :group=>:test\n":           `1 x group range='~> 1.0' constraint="~> 1.0" version="1.0" source=`,
		"gem 'y', :git=>'https://example.com/y.git'\n": `1 y dependencies range= constraint="" version="" source= skip=git dependency`,
		"ruby '2.7.1', :patchlevel=>'1'\n":             `1 ruby ruby range= constraint="" version="" source= skip=` + skipRubyPatchlevel,
	} {
		deps, err := New().Extract("Gemfile", []byte(src), manager.Env{})
		var got []string
		for _, d := range deps {
			got = append(got, describe([]byte(src), d))
		}
		if err != nil || strings.Join(got, "\n") != want {
			t.Errorf("%q:\n%s\nwant %s (%v)", src, strings.Join(got, "\n"), want, err)
		}
	}
}

func TestExtractOddInput(t *testing.T) {
	for _, src := range []string{
		"", "end\nend\n", "gem", "gem 'unterminated", "gem \"a\", %w(x", "=begin\nno end", "gem <<~X\n", "group :a do\n",
		"gem \"a\", \"1.0\" \\\n  , require: false\n", "class << self\nend\n", ":\n::\n%\n", "gem 'ok'; gem 'x' if a ||\n b\n",
	} {
		if _, err := New().Extract("Gemfile", []byte(src), manager.Env{}); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	deps, _ := New().Extract("Gemfile", []byte("gem \"a\", \"1.0\" \\\n  , require: false\n"), manager.Env{})
	if len(deps) != 1 || deps[0].Range != `"1.0"` || deps[0].Line != 1 {
		t.Errorf("line continuation: %+v", deps)
	}
}

// TestEdits rewrites the fixture's requirements under the default strategy (""): versions
// outside them replace them, versions they admit bump them.
func TestEdits(t *testing.T) {
	src := read(t, "testdata/Gemfile")
	deps, err := New().Extract("Gemfile", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]dep.Dep{}
	for _, d := range deps {
		byName[d.Name] = d
	}
	for _, tt := range []struct {
		name, version, line string
	}{
		{"rails", "8.2.1", "gem 'rails', '~> 8.2.1'"},
		{"haml-rails", "4.1.2", "gem 'haml-rails', '~>4.1'"},
		{"aws-sdk-s3", "2.0.0", "gem 'aws-sdk-s3', '~> 2.0', require: false"},
		{"fog-core", "2.7.1", "gem 'fog-core', '<= 2.7.1'"},
		{"fastlane", "2.242.0", `gem "fastlane", "2.242.0"`},
		{"rack", "4.1.0", `gem "rack", ">= 4.1.0", "< 5"`},
		{"nokogiri", "2.1.0", `gem "nokogiri", [">= 2.1", "< 3"]`},
		{"puma", "7.0.0.1", `gem("puma", "= 7.0.0.1")`},
		{"sidekiq", "8.0.1", `gem 'sidekiq', '>= 8.0.1' # comment with "quotes" and gem "fake", "1.0"`}, // admitted: bumped
		{"webpush", "2.0.0.rc1", "    '~> 2.0.0.rc1',"},
		{"lt", "2.0.3", `gem "lt", "< 3"`},
		{"sidekiq-pro", "8.0.0", `  gem "sidekiq-pro", "~> 8.0"`},
	} {
		d := byName[tt.name]
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.version})
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		// The requirement's line: webpush's is the one after the gem's.
		i := d.Line - 1
		if tt.name == "webpush" {
			i++
		}
		if got := strings.Split(string(out), "\n")[i]; got != tt.line {
			t.Errorf("%s: line = %q, want %q", tt.name, got, tt.line)
		}
		grown := 0
		for _, e := range edits {
			grown += len(e.New) - len(e.Old)
		}
		if len(out)-len(src) != grown {
			t.Errorf("%s: more than the requirements changed", tt.name)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: byName["propshaft"], NewVersion: "9.0.0"}); err == nil {
		t.Error("propshaft: a gem without requirements was edited")
	}
}

// TestRangeStrategies rewrites each shape of requirements under each rangeStrategy, through
// NewRange and Edits: want is the requirement arguments afterwards, "" when they are not
// rewritten (unchanged, or not admitting the version).
func TestRangeStrategies(t *testing.T) {
	const (
		bump, replace, lockfile, widen, pin, inRange = "bump", "replace", "update-lockfile", dep.RangeWiden, dep.RangePin, "in-range-only"
	)
	for _, tt := range []struct{ reqs, strategy, v, want string }{
		// An exact version is replaced by a newer one under every strategy; widen cannot widen it.
		{`"= 1.2.3"`, bump, "1.2.3", ""},
		{`"= 1.2.3"`, bump, "1.3.0", `"= 1.3.0"`},
		{`"= 1.2.3"`, replace, "1.3.0", `"= 1.3.0"`},
		{`"= 1.2.3"`, lockfile, "1.3.0", `"= 1.3.0"`},
		{`"= 1.2.3"`, widen, "1.3.0", `"= 1.3.0"`},
		{`"= 1.2.3"`, pin, "1.3.0", `"= 1.3.0"`},
		{`"= 1.2.3"`, pin, "1.2.3", ""},
		{`"1.2.3"`, pin, "1.3.0", `"1.3.0"`},
		{`"1.2.3"`, replace, "2.0", `"2.0"`},
		// `~>` keeps its precision; a bump below it adds the floor.
		{`"~> 7.1"`, bump, "7.2.3", `"~> 7.2", ">= 7.2.3"`},
		{`"~> 7.1"`, bump, "7.2.0", `"~> 7.2"`},
		{`"~> 7.1"`, bump, "7.1", ""},
		{`"~> 7.1"`, inRange, "7.1.5", `"~> 7.1", ">= 7.1.5"`},
		{`"~> 7.1"`, replace, "8.0.1", `"~> 8.0"`},
		{`"~> 7.1"`, replace, "7.2.3", `"~> 7.2", ">= 7.2.3"`}, // admitted: bumped
		{`"~> 7.1"`, lockfile, "8.0.1", `"~> 8.0"`},
		{`"~> 7.1"`, lockfile, "8.0.0.rc1", `"~> 8.0.0.rc1"`},
		{`"~> 7.1"`, widen, "8.0.1", `">= 7.1", "< 9"`},
		{`"~> 7.1"`, widen, "10.2.0", `">= 7.1", "< 11"`},
		{`"~> 7.1"`, pin, "8.0.1", `"= 8.0.1"`},
		{`"~> 7.1"`, pin, "7.2.3", `"= 7.2.3"`},
		{`'~>7.1'`, widen, "8.0.1", `'>=7.1', '<9'`},
		{`'~>7.1'`, pin, "8.0.1", `'=8.0.1'`},
		{`"~> 7.1.2"`, bump, "7.1.5", `"~> 7.1.5"`},
		{`"~> 7.1.2"`, replace, "8.0.1", `"~> 8.0.1"`},
		{`"~> 7.1.2"`, replace, "7.2", `"~> 7.2.0"`},
		{`"~> 7.1.2"`, widen, "8.0.1", `">= 7.1.2", "< 8.1"`},
		{`"~> 7.1.2"`, widen, "7.2.0", `">= 7.1.2", "< 7.3"`},
		{`"~> 7.1.2"`, pin, "7.1.5", `"= 7.1.5"`},
		// Lower bounds.
		{`">= 1.2"`, bump, "1.4.5", `">= 1.4.5"`},
		{`">= 1.2"`, lockfile, "1.4.5", `">= 1.4.5"`},
		{`">= 1.2"`, widen, "1.4.5", `">= 1.4.5"`},
		{`">= 1.2"`, pin, "1.4.5", `"= 1.4.5"`},
		{`"> 1.2"`, bump, "1.5.0", `">= 1.5.0"`},
		// A list with an upper bound: bump raises the floor, replace moves both, widen the bound.
		{`">= 1.2", "< 2"`, bump, "1.5.0", `">= 1.5.0", "< 2"`},
		{`">= 1.2", "< 2"`, inRange, "1.5.0", `">= 1.5.0", "< 2"`},
		{`">= 1.2", "< 2"`, replace, "2.1.0", `">= 2.1", "< 3"`},
		{`">= 1.2", "< 2"`, lockfile, "2.1.0", `">= 2.1", "< 3"`},
		{`">= 1.2", "< 2"`, widen, "2.1.0", `">= 1.2", "< 3"`},
		{`">= 1.2", "< 2.0"`, widen, "2.1.0", `">= 1.2", "< 3.0"`},
		{`">= 1.2", "< 1.3"`, widen, "1.4.2", `">= 1.2", "< 1.5"`},
		{`">= 1.2", "<= 1.9"`, widen, "2.1.0", `">= 1.2", "<= 2.1.0"`},
		{`">= 1.2", "< 2"`, pin, "2.1.0", `"= 2.1.0"`},
		{`[">= 1.2", "< 2"]`, replace, "2.1.0", `[">= 2.1", "< 3"]`},
		{`[">= 1.2",
   "< 2"]`, pin, "1.5.0", `["= 1.5.0"]`},
		{`">= 1.2", # floor
   "< 2"`, pin, "1.5.0", ""}, // the comment would go
		{`">= 1.2", "!= 1.5.0", "< 2"`, replace, "1.5.0", ""}, // excluded
		{`">= 1.2", "!= 1.5.0", "< 2"`, bump, "1.6.0", `">= 1.6.0", "!= 1.5.0", "< 2"`},
		// `~>` with a floor: the floor follows, widen moves only the bound.
		{`"~> 7.1", ">= 7.1.2"`, bump, "7.2.3", `"~> 7.2", ">= 7.2.3"`},
		{`"~> 7.1", ">= 7.1.2"`, bump, "7.1.5", `"~> 7.1", ">= 7.1.5"`},
		{`"~> 7.1", ">= 7.1.2"`, replace, "8.0.1", `"~> 8.0", ">= 8.0.1"`},
		{`"~> 7.1", ">= 7.1.2"`, lockfile, "8.0.1", `"~> 8.0", ">= 8.0.1"`},
		{`"~> 7.1", ">= 7.1.2"`, widen, "8.0.1", `"< 9", ">= 7.1.2"`},
		{`"~> 7.1", ">= 7.1.2"`, pin, "8.0.1", `"= 8.0.1"`},
		// Upper bounds only.
		{`"< 2"`, bump, "1.5.0", ""},
		{`"< 2"`, replace, "2.1.0", `"< 3"`},
		{`"< 2"`, widen, "2.1.0", `"< 3"`},
		{`"< 2"`, pin, "1.5.0", `"= 1.5.0"`},
		// Without requirements nothing is written (RangeFixed): only the lock moves.
		{``, bump, "1.0.0", ""},
		{``, replace, "1.0.0", ""},
		{``, lockfile, "1.0.0", ""},
		{``, widen, "1.0.0", ""},
		{``, pin, "1.0.0", ""},
		{``, inRange, "1.0.0", ""},
	} {
		gem := `gem "x"`
		if tt.reqs != "" {
			gem += ", " + tt.reqs
		}
		src := []byte("source \"https://rubygems.org\"\n" + gem + ", require: false # x\n")
		deps, err := New().Extract("Gemfile", src, manager.Env{})
		if err != nil || len(deps) != 1 || deps[0].Skip != "" {
			t.Fatalf("%s: %+v, %v", gem, deps, err)
		}
		d := deps[0]
		got, ok := New().(manager.RangeRewriter).NewRange(d, tt.v, tt.strategy)
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.v, RangeStrategy: tt.strategy})
		if tt.want == "" {
			if ok || err == nil {
				t.Errorf("%s %s %s: rewritten to %s (edits %v, %v)", tt.reqs, tt.strategy, tt.v, got, edits, err)
			}
			continue
		}
		// Range starts at the first string: an array's brackets stay around it.
		if r := strings.TrimSuffix(strings.TrimPrefix(tt.want, "["), "]"); !ok || got != r {
			t.Errorf("%s %s %s: NewRange = %s, %v, want %s", tt.reqs, tt.strategy, tt.v, got, ok, tt.want)
		}
		out, aerr := patch.Apply(src, edits)
		want := strings.Replace(string(src), tt.reqs, tt.want, 1)
		if err != nil || aerr != nil || string(out) != want {
			t.Errorf("%s %s %s: edits %v (%v, %v) give\n%s\nwant\n%s", tt.reqs, tt.strategy, tt.v, edits, err, aerr, out, want)
		}
		// Only string contents change, except where pin joins a list into one string.
		for _, e := range edits {
			if strings.ContainsAny(e.Old, `"'`) && !(tt.strategy == pin && strings.Contains(tt.reqs, ",")) {
				t.Errorf("%s %s %s: edit %+v crosses a string", tt.reqs, tt.strategy, tt.v, e)
			}
		}
	}
}
