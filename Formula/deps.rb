# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.15.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.0/deps_darwin_arm64.tar.xz"
      sha256 "a0ad4f2277ac34aa268dd9353996fbb2c05ad4f6e203c5575752c549ee1fd4bc"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.0/deps_darwin_amd64.tar.xz"
      sha256 "f36744edf2245d302d6e5e814ace95eedb38df708b634309929c5adbb565b6b1"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.0/deps_linux_arm64.tar.xz"
      sha256 "7fd7355b1367090d47a6b484cdbaf2948d913ce4634664c7252cb46a8426f782"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.15.0/deps_linux_amd64.tar.xz"
      sha256 "f244feb4c3fe94b64e2a699a9503ad72c393a53da9afb32ffd1603f55625b88d"
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
