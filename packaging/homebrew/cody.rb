# Reference formula for a manual tap release. GoReleaser generates and
# updates this file automatically in the tap repository on every tagged
# release once HOMEBREW_TAP_TOKEN is configured.
#
# Manual use: replace the url tag and sha256 (shasum -a 256 cody_*.tar.gz),
# then copy this file to Formula/cody.rb in a repository named
# ajhous44/homebrew-tap.
class Cody < Formula
  desc "A small coding agent for the terminal"
  homepage "https://github.com/ajhous44/cody"
  url "https://github.com/ajhous44/cody/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_TARBALL_SHA256"
  license "MIT"
  head "https://github.com/ajhous44/cody.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X github.com/ajhous44/cody/internal/cli.Version=#{version}"
    system "go", "build", *std_go_args(ldflags: ldflags), "."
  end

  test do
    assert_match "cody #{version}", shell_output("#{bin}/cody version")
  end
end
