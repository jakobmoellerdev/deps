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

// describe renders the fields of d that Extract sets, with the bytes RefSpan points at.
func describe(src []byte, d dep.Dep) string {
	ref := "-"
	if d.RefSpan != (dep.Span{}) {
		ref = string(src[d.RefSpan.Start:d.RefSpan.End])
	}
	s := fmt.Sprintf("%d %s %s range=%q version=%q ref=%s source=%s", d.Line, d.Name, d.DepType, d.Range, d.Version, ref, d.Source)
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
			`12 ruby ruby range=">= 3.3.5" version="" ref=- source= skip=` + skipRequiredRuby,
			`37 i18n runtime range=">= 1.6, < 2" version="1.6" ref=- source= fixed`,
			`38 tzinfo runtime range="~> 2.0, >= 2.0.5" version="2.0" ref=- source= fixed`,
			`39 concurrent-ruby runtime range="~> 1.0, >= 1.3.1" version="1.0" ref=- source= fixed`,
			`40 connection_pool runtime range=">= 2.2.5" version="2.2.5" ref=2.2.5 source=`,
			`41 minitest runtime range=">= 5.1" version="5.1" ref=5.1 source=`,
			`42 base64 runtime range=">= 0" version="" ref=- source= fixed`,
			`43 drb runtime range=">= 0" version="" ref=- source= fixed`,
			`44 bigdecimal runtime range=">= 0" version="" ref=- source= fixed`,
			`45 json runtime range=">= 0" version="" ref=- source= fixed`,
			`46 psych runtime range=">= 4" version="4" ref=4 source=`,
			`47 logger runtime range=">= 1.4.2" version="1.4.2" ref=1.4.2 source=`,
			`48 securerandom runtime range=">= 0.3" version="0.3" ref=0.3 source=`,
			`49 uri runtime range=">= 0.13.1" version="0.13.1" ref=0.13.1 source=`,
			`50 ractor-dispatch runtime range=">= 0.3.0" version="0.3.0" ref=0.3.0 source=`,
		},
		"devise.gemspec": {
			`28 ruby ruby range=">= 2.7.0" version="" ref=- source= skip=` + skipRequiredRuby,
			`30 warden runtime range="~> 1.2.3" version="1.2.3" ref=1.2.3 source=`,
			`31 orm_adapter runtime range="~> 0.1" version="0.1" ref=0.1 source=`,
			`32 bcrypt runtime range="~> 3.0" version="3.0" ref=3.0 source=`,
			`33 railties runtime range=">= 7.0" version="7.0" ref=7.0 source=`,
			`34 responders runtime range=">= 0" version="" ref=- source= fixed`,
		},
		"shapes.gemspec": {
			`3 nokogiri runtime range="~> 1.18" version="1.18" ref=1.18 source=`,
			`4 rspec development range="~> 3.13" version="3.13" ref=3.13 source=`,
			`6 faraday runtime range="" version="" ref=- source= skip=` + skipNonLiteral,
			`7 rack runtime range=">= 2.2, < 4" version="2.2" ref=- source= fixed`,
			`8 ruby ruby range="" version="" ref=- source= skip=` + skipRequiredRuby,
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
	if got, want := describe(src, r), `3 ruby ruby range="4.0.7" version="4.0.7" ref=4.0.7 source=`; got != want {
		t.Errorf("ruby = %s\nwant   %s", got, want)
	}
	if r.Manager != Name || r.Datasource != RubyDatasource || r.Versioning != "ruby" {
		t.Errorf("ruby = %+v", r)
	}
	if got, want := describe(src, d), `5 fastlane dependencies range="2.241.2" version="2.241.2" ref=2.241.2 source=https://rubygems.org`; got != want {
		t.Errorf("dep = %s\nwant  %s", got, want)
	}
	if d.Manager != Name || d.Datasource != "rubygems" || d.Versioning != "ruby" || d.File != "apps/ios/Gemfile" {
		t.Errorf("dep = %+v", d)
	}
}

func TestExtractRuby(t *testing.T) {
	for src, want := range map[string]string{
		"ruby \"3.4.7\"\n":                            `1 ruby ruby range="3.4.7" version="3.4.7" ref=3.4.7 source=`,
		"ruby('~> 3.4.0')\n":                          `1 ruby ruby range="~> 3.4.0" version="3.4.0" ref=3.4.0 source=`,
		"ruby \">= 3.2\", \"< 5\"\n":                  `1 ruby ruby range=">= 3.2, < 5" version="3.2" ref=- source= fixed`,
		"ruby \"3.3.0\"\ngem \"a\"\nruby \"3.4.1\"\n": `2 a dependencies range=">= 0" version="" ref=- source= fixed` + "\n" + `3 ruby ruby range="3.4.1" version="3.4.1" ref=3.4.1 source=`,
		"ruby \"3.4.7\", :engine => \"ruby\", :engine_version => \"3.4.7\"\n": `1 ruby ruby range="3.4.7" version="3.4.7" ref=3.4.7 source=`,
		"ruby \"3.4.7\", engine: \"jruby\", engine_version: \"9.4.8.0\"\n":    `1 ruby ruby range="" version="" ref=- source= skip=` + skipRubyEngine,
		"ruby \"3.3.6\", patchlevel: \"108\"\n":                               `1 ruby ruby range="" version="" ref=- source= skip=` + skipRubyPatchlevel,
		"ruby file: \".tool-versions\"\n":                                     `1 ruby ruby range="" version="" ref=- source= skip=` + skipRubyFile,
		"ruby File.read(\".ruby-version\").strip\n":                           `1 ruby ruby range="" version="" ref=- source= skip=` + skipNonLiteral,
		"ruby RUBY_VERSION\n":                                                 `1 ruby ruby range="" version="" ref=- source= skip=` + skipNonLiteral,
		"ruby \"< 4\"\n":                                                      `1 ruby ruby range="< 4" version="" ref=- source= fixed skip=` + skipRubyNoFloor,
		"ruby\n":                                                              `1 ruby ruby range="" version="" ref=- source= skip=` + skipRubyNoFloor,
		"platforms :ruby do\n  gem \"a\"\nend\n":                              `2 a dependencies range=">= 0" version="" ref=- source= fixed`,
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
		`6 ruby ruby range="" version="" ref=- source= skip=` + skipRubyFile,
		`10 propshaft dependencies range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`11 rails dependencies range="~> 8.1.0" version="8.1.0" ref=8.1.0 source=` + rg,
		`12 haml-rails dependencies range="~>3.0" version="3.0" ref=3.0 source=` + rg,
		`13 aws-sdk-s3 dependencies range="~> 1.123" version="1.123" ref=1.123 source=` + rg,
		`14 fog-core dependencies range="<= 2.6.0" version="" ref=- source=` + rg + ` fixed`,
		`15 omniauth-cas dependencies range="~> 3.0.0.beta.1" version="3.0.0.beta.1" ref=3.0.0.beta.1 source=` + rg,
		`16 bootsnap dependencies range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`17 fastlane dependencies range="2.241.2" version="2.241.2" ref=2.241.2 source=` + rg,
		`18 rack dependencies range=">= 2.2.8, < 4" version="2.2.8" ref=- source=` + rg + ` fixed`,
		`19 nokogiri dependencies range=">= 1.16, < 2" version="1.16" ref=- source=` + rg + ` fixed`,
		`20 puma dependencies range="= 6.4.2" version="6.4.2" ref=6.4.2 source=` + rg,
		`21 sidekiq dependencies range=">= 7.0" version="7.0" ref=7.0 source=` + rg,
		`22 webpush dependencies range="~> 1.1" version="1.1" ref=1.1 source=` + rg,
		`26 pg dependencies range="" version="" ref=- source=` + rg + ` skip=non-literal requirement`,
		`27 redis dependencies range="" version="" ref=- source=` + rg + ` skip=non-literal requirement`,
		`28 foo dependencies range="" version="" ref=- source=` + rg + ` skip=non-literal requirement`,
		`29 x dependencies range="" version="latest" ref=- source=` + rg + ` skip=unsupported requirement`,
		`30 debug dependencies range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`31 rubocop group range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`32 raindrops dependencies range="" version="" ref=- source=` + rg + ` skip=git dependency`,
		`33 rails-html dependencies range="" version="" ref=- source=` + rg + ` skip=git dependency`,
		`34 mine dependencies range="" version="" ref=- source=` + rg + ` skip=git dependency`,
		`35 migrations-core dependencies range="" version="" ref=- source=` + rg + ` skip=path dependency`,
		`36 private-gem dependencies range="~> 2.0" version="2.0" ref=2.0 source=https://gems.example.com`,
		`37 env-source dependencies range="" version="" ref=- source= skip=non-literal source`,
		`38 lt dependencies range="< 2" version="" ref=- source=` + rg + ` fixed`,
		`45 devise_pam_authenticatable2 group range="~> 9.2" version="9.2" ref=9.2 source=` + rg,
		`49 rspec-rails group range="~> 7.0" version="7.0" ref=7.0 source=` + rg,
		`51 byebug group range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`56 sidekiq-pro dependencies range="~> 7.3" version="7.3" ref=7.3 source=https://gems.contribsys.com/`,
		`60 activesupport dependencies range="" version="" ref=- source=` + rg + ` skip=git dependency`,
		`64 engine dependencies range="" version="" ref=- source=` + rg + ` skip=path dependency`,
		`68 discourse_dev_assets dependencies range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`71 faker group range="~> 2.16" version="2.16" ref=2.16 source=` + rg,
		`76 rb-fsevent dependencies range=">= 0" version="" ref=- source=` + rg + ` fixed`,
		`79 heredoc dependencies range="" version="" ref=- source=` + rg + ` skip=non-literal requirement`,
		`83 last dependencies range="1.0.0" version="1.0.0" ref=1.0.0 source=` + rg,
		`83 same-line dependencies range="2.0" version="2.0" ref=2.0 source=` + rg,
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
	if len(deps) != 1 || deps[0].Range != "1.0" || deps[0].Line != 1 {
		t.Errorf("line continuation: %+v", deps)
	}
}

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
		{"fastlane", "2.242.0", `gem "fastlane", "2.242.0"`},
		{"puma", "7.0.0.1", `gem("puma", "= 7.0.0.1")`},
		{"sidekiq", "8.0.1", `gem 'sidekiq', '>= 8.0' # comment with "quotes" and gem "fake", "1.0"`},
		{"webpush", "2.0.0.rc1", "    '~> 2.0.0.rc1',"},
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
		if len(out)-len(src) != len(edits[0].New)-len(edits[0].Old) {
			t.Errorf("%s: more than the version changed", tt.name)
		}
	}
	for _, name := range []string{"rack", "propshaft", "lt"} {
		if _, err := New().Edits(dep.Update{Dep: byName[name], NewVersion: "9.0.0"}); err == nil {
			t.Errorf("%s: a requirement that is never rewritten was edited", name)
		}
	}
}
