package manager

import "testing"

func TestGitRepo(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://github.com/apple/swift-log.git": {"apple/swift-log", "https://github.com/apple/swift-log"},
		"https://github.com/apple/swift-log/":    {"apple/swift-log", "https://github.com/apple/swift-log"},
		"https://h:8443/o/r.git":                 {"o/r", "https://h:8443/o/r"},
		"http://h:3000/o/r":                      {"o/r", "http://h:3000/o/r"},
		"https://user:tok@h/o/r":                 {"o/r", "https://h/o/r"},
		"git@github.com:apple/swift-nio.git":     {"apple/swift-nio", "https://github.com/apple/swift-nio"},
		"github.com:apple/swift-nio":             {"apple/swift-nio", "https://github.com/apple/swift-nio"},
		"ssh://git@gitlab.com/g/sub/proj.git":    {"g/sub/proj", "https://gitlab.com/g/sub/proj"},
		"ssh://git@git.example:2222/o/r":         {"o/r", "https://git.example/o/r"},
		"git://git.example:9418/o/r":             {"o/r", "https://git.example/o/r"},
		"file:///tmp/x":                          {"", ""},
		"https://github.com/single":              {"", ""},
		"../relative/path":                       {"", ""},
	} {
		if n, s := GitRepo(in); n != want[0] || s != want[1] {
			t.Errorf("GitRepo(%q) = %q, %q; want %q, %q", in, n, s, want[0], want[1])
		}
	}
}
