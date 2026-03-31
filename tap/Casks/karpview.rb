cask "karpview" do
  version "0.1.0"
  sha256 "REPLACE_WITH_SHA256_FROM_MAKE_RELEASE"

  url "https://github.com/nikgibson/karpview/releases/download/v#{version}/karpview-v#{version}-darwin-arm64.tar.gz"
  name "KarpView"
  desc "Karpenter node disruption visualizer"
  homepage "https://github.com/nikgibson/karpview"

  binary "karpview-darwin-arm64", target: "karpview"
end
