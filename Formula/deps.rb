# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.11.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.11.0/deps_darwin_arm64.tar.xz"
      sha256 "acf6840cbf93fee7f49164846c91ff70e3f62438b8ec3b2b952ce3b880e5ade7"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.11.0/deps_darwin_amd64.tar.xz"
      sha256 "ddcc9bcfa9c2cbafde4bb4ac0744ddd6500122de941054bd1705a7b51dc0a115"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.11.0/deps_linux_arm64.tar.xz"
      sha256 "00d17a8e63cc481503c1c3d240b095964c8064ffd7e2d1fad4ae2cba4c28c2cc"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.11.0/deps_linux_amd64.tar.xz"
      sha256 "bddfb1e98d1e6dbb9b2acd09aa07c688777c4ebc76380385917db5023267a49b"
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
