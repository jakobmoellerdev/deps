// Command digests records the sha256 of the toolchain artifacts deps publishes a digest for
// itself: the Linux builds of Ruby and PHP, whose publishers ship none (see
// internal/toolchain/pinned.go). It downloads every architecture of each version named on
// the command line and merges the result into internal/toolchain/digests.json, which is
// reviewed and committed like code.
//
//	go run ./hack/digests ruby@3.4.12 php@8.3.9
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolchain"
)

const file = "internal/toolchain/digests.json"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./hack/digests <tool>@<version>...")
		os.Exit(2)
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "digests:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	digests := map[string]map[string]map[string]string{}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &digests); err != nil {
		return err
	}
	for _, arg := range args {
		name, version, ok := strings.Cut(arg, "@")
		if !ok {
			return fmt.Errorf("%q: want <tool>@<version>", arg)
		}
		tool := toolchain.Tool(name)
		for _, goarch := range []string{"amd64", "arm64"} {
			url, ok := toolchain.PinnedURL(tool, version, "linux", goarch)
			if !ok {
				return fmt.Errorf("%s is not a tool deps digests itself", name)
			}
			sum, err := digestOf(url)
			if err != nil {
				return fmt.Errorf("%s: %w", url, err)
			}
			if digests[name] == nil {
				digests[name] = map[string]map[string]string{}
			}
			if digests[name][version] == nil {
				digests[name][version] = map[string]string{}
			}
			digests[name][version]["linux-"+goarch] = sum
			fmt.Printf("%s %s linux-%s %s\n", name, version, goarch, sum)
		}
	}
	// encoding/json sorts map keys, so a regeneration is always a readable diff.
	out, err := json.MarshalIndent(digests, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(out, '\n'), 0o644) //nolint:gosec // a committed data file
}

func digestOf(url string) (string, error) {
	resp, err := http.Get(url) //nolint:noctx,gosec // a one-off maintenance command
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
