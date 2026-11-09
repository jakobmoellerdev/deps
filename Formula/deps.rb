# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.7.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.7.0/deps_darwin_arm64.tar.xz"
      sha256 "57989d9d4d8ce8da9644e78a19d39bce77db30cdaf71e6c11ea8e4a19406968e"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.7.0/deps_darwin_amd64.tar.xz"
      sha256 "8da3f1079f55a4f00ba3ec6ab4a55f9cfd6f88b929a8ca667eef13e11f9f77fb"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.7.0/deps_linux_arm64.tar.xz"
      sha256 "6d6386c4e927a4de21740e8f43a66fab53ea696cb3aa44f3b4725528dc308f39"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.7.0/deps_linux_amd64.tar.xz"
      sha256 "0b41a77372433b500474d76861e2aae0e575f727a5ca1724bc8c5f791b53e5c0"
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
