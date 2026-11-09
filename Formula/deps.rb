# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.6.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_darwin_arm64"
      sha256 "17715ff5b3a4b7bd9fa5edb9afe84a373c803cf810ed648a66c64c25a0218ca9"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_darwin_amd64"
      sha256 "c04bcac0137fd6cfd9ce432cf7937697d34d93cceaaa4f7b92fd1e7204f80486"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_linux_arm64"
      sha256 "c77e0a9c5058a4a5854befd06861d33f95770f3d6f53022adfd80134e2ea6760"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_linux_amd64"
      sha256 "93e9a84364ee8e86588b71dbfc1a066b8ead8c1974ae0023eb97f55cf9404dfd"
    end
  end

  def install
    # The download is the bare binary, which Homebrew stages without the executable bit.
    binary = "deps_#{OS.mac? ? "darwin" : "linux"}_#{Hardware::CPU.arm? ? "arm64" : "amd64"}"
    chmod 0755, binary
    bin.install binary => "deps"
    generate_completions_from_executable(bin/"deps", "completion")
  end

  test do
    assert_equal "v#{version}", shell_output("#{bin}/deps version").strip
  end
end
