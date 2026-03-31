cask "karpview" do
  version "0.1.0"
  sha256 "6ab5f3c1a8df90fe79adf965bb8d8e11a8d0c913c7775749cbffb3fcef5fab05"

  url "https://github.com/nikgibson/karpview/releases/download/v#{version}/karpview-v#{version}-darwin-arm64.tar.gz"
  name "KarpView"
  desc "Karpenter node disruption visualizer"
  homepage "https://github.com/nikgibson/karpview"

  binary "karpview-darwin-arm64", target: "karpview"
end
