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
      nixosModules = {
        default = import ./modules/nixos/services/github-runner;

        # The per-job dispatcher: maps the scale-set queue onto one VM per job
        # and publishes the pool-health record the router reads. The wrapper
        # injects this flake's dispatcher package so consumers need only the
        # module, not the package output.
        runner-dispatcher =
          { pkgs, ... }@args:
          import ./modules/nixos/services/runner-dispatcher (
            args
            // {
              dispatcherPackage = self.packages.${pkgs.stdenv.hostPlatform.system}.runner-dispatcher;
            }
          );
      };

      # The per-job dispatcher: maps the scale-set queue onto one VM per job
      # and publishes the pool-health record the router reads.
      packages = forAllSystems (pkgs: rec {
        runner-dispatcher = pkgs.buildGoModule {
          pname = "runner-dispatcher";
          version = "0.1.0";
          src = ./runner-dispatcher;
          subPackages = [ "cmd/runner-dispatcher" ];
          vendorHash = "sha256-i9/m2v+5Fb2kzY3cUU9MwQKPZtoNz/9MYYQb7cNJ8qg=";
        };
        default = runner-dispatcher;
      });

      # No home-manager module is required. The pool is a system service: image
      # pull, the systemd user units, and the podman socket are all host-level.
      # A consumer that also wants a per-user convenience does not need this
      # output to exist.
      #
      # formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
