cask "karpview" do
  version "0.1.0"

  on_arm do
    sha256 "6ab5f3c1a8df90fe79adf965bb8d8e11a8d0c913c7775749cbffb3fcef5fab05"
    url "https://github.com/n-gibs/karpview/releases/download/v#{version}/karpview-darwin-arm64.tar.gz"
    binary "karpview-darwin-arm64", target: "karpview"
  end

  on_intel do
    sha256 "PLACEHOLDER_UPDATE_ON_NEXT_RELEASE"
    url "https://github.com/n-gibs/karpview/releases/download/v#{version}/karpview-darwin-amd64.tar.gz"
    binary "karpview-darwin-amd64", target: "karpview"
  end

  name "KarpView"
  desc "Karpenter node disruption visualizer"
  homepage "https://github.com/n-gibs/karpview"
end
