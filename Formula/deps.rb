# deps' homebrew manager moves the urls, sha256 sums and version to each new release (one PR).
class Deps < Formula
  desc "Renovate-style dependency update pull requests from one static binary"
  homepage "https://git.jakob-moeller.cloud/jakobmoellerdev/deps"
  version "0.18.2"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.18.2/deps_darwin_arm64.tar.xz"
      sha256 "240e04ed084ba115859aca4a22143e4d3c962e4bee3457415ab5b15cc5b68d69"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.18.2/deps_darwin_amd64.tar.xz"
      sha256 "5d626115c254aeec9d8f999cb184ad56dc811b57baaa3f06e1fe33a4da869afa"
    end
  end

  on_linux do
    on_arm do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.18.2/deps_linux_arm64.tar.xz"
      sha256 "581901d53b1a94eb46cb9efd359b9792c08b6dbbb8434c902859b160677e457b"
    end
    on_intel do
      url "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.18.2/deps_linux_amd64.tar.xz"
      sha256 "4135b5e3406b63023dcc1d40a455a881a68cd2b6ffe6483158c343ae6e58cb7f"
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
