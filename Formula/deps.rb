# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.14.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.14.0/deps_darwin_arm64.tar.xz"
      sha256 "90e126cc05d9f3ec9c8fab5a05f624926c410c4de07ba3223618111a9db9f159"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.14.0/deps_darwin_amd64.tar.xz"
      sha256 "187bbb34ee38eeaa33c286d18928af452795b16f09e5d6d8d6fd6055ab390d31"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.14.0/deps_linux_arm64.tar.xz"
      sha256 "1a98a4b3f4362f15691523188478ce8d0321af2d28579d2381be9bc0fde5e43a"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.14.0/deps_linux_amd64.tar.xz"
      sha256 "739307b668a30318a82a78b061a53bd83df3bcf9eb9fb94d5e3086949d012900"
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
