# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.13.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.13.0/deps_darwin_arm64.tar.xz"
      sha256 "00b72ebf78000fb70b8f430eed5c23f6c8982a0f518cc5bd8aefeb893111783b"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.13.0/deps_darwin_amd64.tar.xz"
      sha256 "4eaec710cbda80a4cd96a1e473870169255580e1a1d288ba8608633ed6a6feed"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.13.0/deps_linux_arm64.tar.xz"
      sha256 "1ebf841a375c14afd4c3b555fc4bcb09763fe60905f3f11973f01f0072716188"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.13.0/deps_linux_amd64.tar.xz"
      sha256 "5fd619fe4697e2d649806ff3ee430054ad2295006cc3f11bf5efac4b155c834b"
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
