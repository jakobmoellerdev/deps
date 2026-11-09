# Every input form the nix manager reads, and the ones it skips.
{
  description = ''
    Inputs of every kind; "quotes" and ${"interpolations"} in strings; '''escaped''' too.
  '';

  inputs = {
    # a version tag in the path
    tagged.url = "github:acme/tagged/v1.2.3";
    pinned-tag = {
      url = "git+https://git.example.com/acme/pinned-tag?ref=refs/tags/v2.0.0&rev=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
      flake = false;
    };
    sub.url = "gitlab:group%2Fsub/project?ref=main";
    "hut".url = "sourcehut:~user/hut";
    attrs = { type = "github"; owner = "acme"; repo = "attrs"; ref = "v0.1.0"; };
    uri.url = github:acme/uri/v3.0.0;
    escaped.url = "github:acme/escaped/v1.\0";
    ssh.url = "git+ssh://git@github.com/acme/ssh?ref=refs/heads/develop";
    lix.url = "https://git.lix.systems/lix-project/lix/archive/2.93.3.tar.gz";
    hub.url = "https://flakehub.com/f/NixOS/nixpkgs/0.1.*.tar.gz";
    gh-archive.url = "https://github.com/acme/archive/archive/main.tar.gz";
    local.url = "path:./local";
    local-git.url = "git+file:///srv/repo";
    registry.url = "nixpkgs";
    hg.url = "hg+https://hg.example.com/acme/hg";
    stale.url = "github:acme/stale";
    followed.url = "github:acme/followed";
    alias.follows = "tagged";
    tagged.inputs.nixpkgs.follows = "";
  };
  inputs.channel.url = "https://nixos.org/channels/nixos-24.05/nixexprs.tar.xz";
  inputs.unstable-channel.url = "https://channels.nixos.org/nixos-unstable/nixexprs.tar.xz";
  # nixpkgs release branches move to the next release; a fork's branch does not
  inputs.stable.url = "github:NixOS/nixpkgs/nixos-24.05";
  inputs.darwin.url = "github:nixos/nixpkgs/nixpkgs-24.05-darwin";
  inputs.release.url = "git+https://github.com/NixOS/nixpkgs?ref=refs/heads/release-24.05";
  inputs.fork.url = "github:acme/nixpkgs/nixos-24.05";
  /* inputs.commented.url = "github:acme/commented"; */

  outputs =
    inputs@{ self, ... }:
    let
      x = {
        inputs.fake.url = "github:acme/fake";
      };
    in
    with inputs;
    {
      packages = { };
    };

  nixConfig.extra-substituters = [ "https://cache.example.com" ];
}
