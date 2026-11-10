# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.8.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.8.0/deps_darwin_arm64.tar.xz"
      sha256 "fed0de8c40c9163e0144b6518b038209a560f835c91929c0c4be2aa299cd130e"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.8.0/deps_darwin_amd64.tar.xz"
      sha256 "fabd0197fefe13eef8de63e7554722cfbd1122d93f2f17a24d10f371cfabc1f4"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.8.0/deps_linux_arm64.tar.xz"
      sha256 "8c1232e9f6023a942b1e5b23e3686e70ca827ef2bac47314cb3c00b2ef9a4916"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.8.0/deps_linux_amd64.tar.xz"
      sha256 "47f95abf606eb83a9a7d37a332232ea5f1ef8494a1838a059a23f7ede9d235ae"
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
