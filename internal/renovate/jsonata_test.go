package renovate

import "testing"

func TestJSONataVersions(t *testing.T) {
	tests := []struct{ in, versions, re string }{
		// OCM's envtest datasource.
		{`{ "releases": $map($distinct($keys($.releases)), function($v){ {"version": $replace($v, /^v/, "")} }) }`, ".releases | keys", stripV},
		{`{"releases": $map($.items, function($r) { {"version": $r.tag_name} })}`, ".items[].tag_name", ""},
		{`{"releases": $map(items, function($r) { {"version": $replace($r.name, /^v/, '')} })}`, ".items[].name", stripV},
		{`{ "releases": [ $.versions.{ "version": name } ] }`, ".versions[].name", ""},
		{`{"releases": data.tags.{"version": id}}`, ".data.tags[].id", ""},
	}
	for _, tt := range tests {
		v, re, ok := jsonataVersions(tt.in)
		if !ok || v != tt.versions || re != tt.re {
			t.Errorf("jsonataVersions(%s) = %q, %q, %v, want %q, %q", tt.in, v, re, ok, tt.versions, tt.re)
		}
	}
	for _, in := range []string{
		`{"releases": $map($keys($.r), function($v){ {"version": $v.x} })}`, // keys have no fields
		`{"releases": $filter($.r, function($v){ $v.stable })}`,
		`{"releases": $map($, function($v){ {"version": $v} })}`,
		`{"releases": $map($.r, function($v){ {"version": $uppercase($v)} })}`,
		`$.releases`,
	} {
		if v, re, ok := jsonataVersions(in); ok {
			t.Errorf("jsonataVersions(%s) = %q, %q, want no translation", in, v, re)
		}
	}
}
