package rubyversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the ruby-version datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "any (`ruby`)",
	Endpoints:    "`<DEPS_RUBY_RELEASES_URL or https://raw.githubusercontent.com/ruby/www.ruby-lang.org/master/_data/releases.yml>`, the list the ruby-lang.org release pages are built from, once per run; published versions plus a derived `X.Y` and `X` per stable line",
	Auth:         "none",
	ReleaseTimes: "`date`",
}
