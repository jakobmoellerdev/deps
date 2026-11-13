# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.15.1"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.1/deps_darwin_arm64.tar.xz"
      sha256 "0e29b65aaa522be822a3aa46ade403e2f0b3be72099077e1aa89bf2602a8be99"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.1/deps_darwin_amd64.tar.xz"
      sha256 "590f8c3613436a83258a95a4ff28be74f3b468046ab6761c70894a832d6d347b"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.1/deps_linux_arm64.tar.xz"
      sha256 "90a692aa946aff976aa3eb9741fe9609c4d4a2691158b4136f2d516c64e43f92"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.1/deps_linux_amd64.tar.xz"
      sha256 "13347e92aa4332ceeaf1bc2a22370bd334ea47823a8dd658442f3e0d6c297000"
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
