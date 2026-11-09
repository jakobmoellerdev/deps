class Tool < Formula
  desc "Per-platform binaries from a Gitea release, a resource, mirrors and blocks to leave alone"
  homepage "https://git.example.org/o/tool"
  version "1.2.3"
  license "MIT"
  head "https://git.example.org/o/tool.git", branch: "main"

  livecheck do
    url "https://git.example.org/o/tool/releases"
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  on_macos do
    on_arm do
      url "https://git.example.org/o/tool/releases/download/v1.2.3/tool_darwin_arm64"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111"
    end
    on_intel do
      url "https://git.example.org/o/tool/releases/download/v1.2.3/tool_darwin_amd64"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222"
    end
  end

  on_linux do
    url "https://git.example.org/o/tool/releases/download/v1.2.3/tool_linux_amd64"
    mirror "https://mirror.example.net/tool/11.2.3/1.2.3/tool_linux_amd64"
    sha256 "3333333333333333333333333333333333333333333333333333333333333333"
  end

  head do
    url "https://git.example.org/o/tool.git", branch: "next"
  end

  bottle do
    sha256 cellar: :any_skip_relocation, arm64_sonoma: "4444444444444444444444444444444444444444444444444444444444444444"
  end

  patch do
    url "https://github.com/o/tool/commit/0a1b2c3d.patch?full_index=1"
    sha256 "5555555555555555555555555555555555555555555555555555555555555555"
  end

  def install
    kind = if OS.mac?
      "darwin"
    else
      "linux"
    end
    (buildpath/"cfg").write <<~EOS
      url "https://git.example.org/o/tool/releases/download/v0.0.1/x"
      end
    EOS
    # A url call in a method body is code, not a download; the if and heredoc above must not close the method.
    url "https://git.example.org/o/tool/releases/download/v0.0.2/y"
    bin.install "tool_#{kind}" => "tool"
  end

  resource "docs" do
    url "https://gitlab.com/o/docs/-/archive/v4.5/docs-v4.5.tar.gz"
    sha256 "6666666666666666666666666666666666666666666666666666666666666666"
  end

  test do
    url "https://git.example.org/o/tool/releases/download/v9.9.9/never"
    system bin/"tool", "version"
  end
end
