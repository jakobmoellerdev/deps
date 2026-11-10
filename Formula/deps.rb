# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.9.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_darwin_arm64.tar.xz"
      sha256 "efdb04c857da7acc768aa79a305c8d2d2d899d15793e17d33b93800d70fc0edd"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_darwin_amd64.tar.xz"
      sha256 "436fa058dbc8daa067515dafedf1d2c28274ead989a0ba017eed56d623c07c2b"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_linux_arm64.tar.xz"
      sha256 "e873cfeb0650916bdbd01b7f68748b0ddc2b1b5f288292afde470737745a614c"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_linux_amd64.tar.xz"
      sha256 "80869ec54e8c6de2d8094f94bd7cf9abbbdc7d63f46aa93bce1d6dac22722f47"
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
