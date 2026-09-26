{
  description = "Peasant Labs shared infrastructure: reusable CI, container image maintenance, cloud control plane";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      # The runner-pool module. A host adds `infra` as a flake input and imports
      # `inputs.infra.nixosModules.default`; everything else is an option on
      # `CUSTOM.services.github-runner`.
      nixosModules.default = import ./modules/nixos/services/github-runner;

      # No home-manager module is required. The pool is a system service: image
      # pull, the systemd user units, and the podman socket are all host-level.
      # A consumer that also wants a per-user convenience does not need this
      # output to exist.
      #
      # formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
