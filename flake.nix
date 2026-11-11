{
  description = "Renovate-style dependency update pull requests from one static binary";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      # The release binaries per Nix system. deps' nix manager moves the urls, hashes and the
      # version to each new release in one pull request.
      version = "0.12.0";
      downloads = {
        x86_64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_linux_amd64.tar.xz";
          hash = "sha256-3foCtxWm68xOZ/TeqNaLXGTry85pbre8ZgnuDAXYaM0=";
        };
        aarch64-linux = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_linux_arm64.tar.xz";
          hash = "sha256-2S5D+T35X+vuNyBtOjFlX03NFjnt8TKTEqecOvd4vIY=";
        };
        x86_64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_darwin_amd64.tar.xz";
          hash = "sha256-5HOcC+wnjka8MwBjEEPcyV4BM0qQ7GkICif3KpKp0xs=";
        };
        aarch64-darwin = {
          url = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.12.0/deps_darwin_arm64.tar.xz";
          hash = "sha256-r0/3zi4Chhah4HQf9rUfVNoVlZhC2N5Hp+f6S2iSATw=";
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
