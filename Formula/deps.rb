# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.10.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.10.0/deps_darwin_arm64.tar.xz"
      sha256 "57f2c23c3840fb11ea4f99d60978305303da0b9bc3d0dfb6ccf20886b072921d"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.10.0/deps_darwin_amd64.tar.xz"
      sha256 "d05bdc76348453e48e2e21e8a44c3f2f0c3b9921940c0366ac65726dea9c6e21"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.10.0/deps_linux_arm64.tar.xz"
      sha256 "c51562045724cee1ee15dc9bc98c6960f81fc7599f5c36a5701ae467459741eb"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.10.0/deps_linux_amd64.tar.xz"
      sha256 "b62724fa101ba28b161bbfa6f1359ee51fc919aaf7e1b72d6078009ee9393df1"
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
