{
  description = "Renovate-style dependency update pull requests from one static binary";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      # The release binaries per Nix system. deps' nix manager moves the urls, hashes and the
      # version to each new release in one pull request.
      version = "0.9.0";
      downloads = {
        x86_64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_linux_amd64.tar.xz";
          hash = "sha256-gIaexU6MbeLYCU+UvXz5q7vcfWP0aqk7zh1trCJyL0c=";
        };
        aarch64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_linux_arm64.tar.xz";
          hash = "sha256-6HPP6wZQkWvb0Bt/aHSLDdwrG18ogpKv3kcHN3RaYUw=";
        };
        x86_64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_darwin_amd64.tar.xz";
          hash = "sha256-Q2+gWNvI2qBnUV2v7fHSwoJ06tmJoLoBfu1W1iPAfCs=";
        };
        aarch64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.9.0/deps_darwin_arm64.tar.xz";
          hash = "sha256-79sEyFfaesx2iqeaMFyNLS2JnRV5PhfTO5OADXD8Dt0=";
        };
      };
      systems = builtins.attrNames downloads;
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      package =
        pkgs:
        pkgs.stdenvNoCC.mkDerivation {
          pname = "deps";
          inherit version;
          src = pkgs.fetchurl downloads.${pkgs.stdenv.hostPlatform.system};
          sourceRoot = ".";
          nativeBuildInputs = [ pkgs.installShellFiles ];
          installPhase = ''
            runHook preInstall
            install -Dm755 deps "$out/bin/deps"
            installShellCompletion --cmd deps \
              --bash completions/deps.bash \
              --zsh completions/_deps \
              --fish completions/deps.fish
            runHook postInstall
          '';
          doInstallCheck = pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform;
          installCheckPhase = ''
            [ "$("$out/bin/deps" version)" = "v${version}" ]
          '';
          meta = {
            description = "Renovate-style dependency update pull requests from one static binary";
            homepage = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "deps";
            platforms = systems;
            sourceProvenance = [ pkgs.lib.sourceTypes.binaryNativeCode ];
          };
        };
    in
    {
      packages = forAllSystems (pkgs: rec {
        deps = package pkgs;
        default = deps;
      });
      overlays.default = final: _prev: { deps = package final; };
      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/deps";
          meta.description = "Run deps";
        };
      });
    };
}
