package main

import (
	"slices"

	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// The migration skill is fetched by agents outside this repository: every command and flag it
// tells them to run must exist, and its frontmatter must name its directory.
func TestMigrationSkill(t *testing.T) {
	const dir = "migrate-from-renovate"
	b, err := os.ReadFile(filepath.Join("..", "..", "skills", dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.HasPrefix(src, "---\nname: "+dir+"\ndescription: ") {
		t.Errorf("frontmatter must start with name: %s and a description", dir)
	}
	root := newRoot()
	hasFlag := func(c *cli.Command, name string) bool {
		if name == "help" {
			return true // every command has --help
		}
		for _, cc := range []*cli.Command{c, root} {
			for _, f := range cc.Flags {
				if slices.Contains(f.Names(), name) {
					return true
				}
			}
		}
		return false
	}
	cmdRe := regexp.MustCompile("`deps ([a-z|]+)((?: [^`]*)?)`|deps ([a-z]+)((?: -[^\n]*)?)\n")
	flagRe := regexp.MustCompile(`(?:^| )(--[a-z-]+)`)
	for _, m := range cmdRe.FindAllStringSubmatch(src, -1) {
		names, rest := m[1]+m[3], m[2]+m[4]
		for name := range strings.SplitSeq(names, "|") {
			c := root.Command(name)
			if c == nil {
				t.Errorf("deps %s: no such command", name)
				continue
			}
			for _, f := range flagRe.FindAllStringSubmatch(rest, -1) {
				if !hasFlag(c, strings.TrimPrefix(f[1], "--")) {
					t.Errorf("deps %s: no flag %s", name, f[1])
				}
			}
		}
	}
}
