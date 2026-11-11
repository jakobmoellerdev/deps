# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.12.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_darwin_arm64.tar.xz"
      sha256 "af4ff7ce2e028616a1e0741ff6b51f54da15959842d8de47a7e7fa4b6892013c"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_darwin_amd64.tar.xz"
      sha256 "e4739c0bec278e46bc3300631043dcc95e01334a90ec69080a27f72a92a9d31b"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_linux_arm64.tar.xz"
      sha256 "d92e43f93df95febee37206d3a31655f4dcd1639edf1329312a79c3af778bc86"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_linux_amd64.tar.xz"
      sha256 "ddfa02b715a6ebcc4e67f4dea8d68b5c64ebcbce696eb7bc6609ee0c05d868cd"
    end
  end

  def install
    bin.install "deps"
    bash_completion.install "completions/deps.bash" => "deps"
    zsh_completion.install "completions/_deps"
    fish_completion.install "completions/deps.fish"
  end

  test do
    assert_equal "v#{version}", shell_output("#{bin}/deps version").strip
  end
end
