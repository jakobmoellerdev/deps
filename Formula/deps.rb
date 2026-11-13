# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.16.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.16.0/deps_darwin_arm64.tar.xz"
      sha256 "0d1bf7debfba1bde7de22c1a1a7e9f9fcf96d67b5e373852d85f064cf14aa8f9"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.16.0/deps_darwin_amd64.tar.xz"
      sha256 "9686b9c21598dda989288e7f6c8ca4fa83c67e7a9cfd3d8d3ced4d6ddbd5e8c1"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.16.0/deps_linux_arm64.tar.xz"
      sha256 "ae63ebcc6936778e91cc98468411b8fb885d9e454a763aa203b1a9731fbbd2f5"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.16.0/deps_linux_amd64.tar.xz"
      sha256 "018ea162357ef644b7133114ab26cac1ed2f33cbb508db9b6d079a5a0768cacc"
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
