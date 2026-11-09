{
  description = "Renovate-style dependency update pull requests from one static binary";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # The release binaries per Nix system. deps' nix manager moves the urls, hashes and the
      # version to each new release in one pull request.
      version = "0.6.0";
      downloads = {
        x86_64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_linux_amd64";
          hash = "sha256-k+moQ2TujoZYi3Hb/BoGa46tjBl0rgAj65f1XPlATf0=";
        };
        aarch64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_linux_arm64";
          hash = "sha256-x34KnFBYpKWFS+/QaGHTP5V3Dz1vUwIq39gBNOLqZ2A=";
        };
        x86_64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_darwin_amd64";
          hash = "sha256-wEvKwBN/1s/ZzkMs95N2l9NNk8zqqk97kv0ecgT4BIY=";
        };
        aarch64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.6.0/deps_darwin_arm64";
          hash = "sha256-F3Ff9bOkt72fpe25r+hKNzyAPPgQ7WSKZsZMJaAhjKk=";
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
          dontUnpack = true;
          nativeBuildInputs = [ pkgs.installShellFiles ];
          installPhase = ''
            runHook preInstall
            install -Dm755 "$src" "$out/bin/deps"
            runHook postInstall
          '';
          postInstall = pkgs.lib.optionalString (pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform) ''
            installShellCompletion --cmd deps \
              --bash <("$out/bin/deps" completion bash) \
              --zsh <("$out/bin/deps" completion zsh) \
              --fish <("$out/bin/deps" completion fish)
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
