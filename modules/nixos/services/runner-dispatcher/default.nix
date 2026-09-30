# The scale-set dispatcher: one VM per job, plus the pool-health record the
# router reads. The package is supplied by the flake output so this module can
# be imported as `infra.nixosModules.runner-dispatcher` without threading the
# flake through the consumer's specialArgs.
{ config
, pkgs
, lib ? pkgs.lib
, dispatcherPackage ? null
, ...
}:
let
  cfg = config.CUSTOM.services.runner-dispatcher;
in
{
  options.CUSTOM.services.runner-dispatcher = {
    enable = lib.mkEnableOption "the GitHub runner scale-set dispatcher";

    scaleSetName = lib.mkOption {
      type = lib.types.str;
      default = "desktop-microvm";
      description = "Runner scale set name; also the workflow label.";
    };

    runnerGroup = lib.mkOption {
      type = lib.types.str;
      default = "default";
      description = "Runner group the scale set registers in.";
    };

    labels = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "self-hosted" "container" ];
      description = "Labels carried by the scale set.";
    };

    maxCapacity = lib.mkOption {
      type = lib.types.ints.positive;
      default = 4;
      description = "Maximum concurrent runner VMs; bounds the host's VM slots.";
    };

    jitDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/runner-vm/jit";
      description = "Parent directory holding one JIT directory per VM slot.";
    };

    heartbeatRepo = lib.mkOption {
      type = lib.types.str;
      default = "peasant-labs/infra";
      description = "Repository holding the pool-health variable the router reads.";
    };

    heartbeatVariable = lib.mkOption {
      type = lib.types.str;
      default = "RUNNER_POOL_HEALTH";
      description = "Repository variable for the pool-health record.";
    };

    app = {
      clientId = lib.mkOption {
        type = lib.types.str;
        description = "GitHub App client id used for scale sets, JIT and the heartbeat.";
      };
      installationId = lib.mkOption {
        type = lib.types.int;
        description = "GitHub App installation id on the organization.";
      };
      privateKeyFile = lib.mkOption {
        type = lib.types.path;
        description = ''
          PEM private key read by the dispatcher service, which runs as root
          (for example a sops secret owned by root with mode 0400). The guest
          VMs never receive this key; they only get a single-use JIT config.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = dispatcherPackage != null;
        message = "CUSTOM.services.runner-dispatcher must be imported through the infra flake output so the dispatcher package is available";
      }
    ];

    systemd.services.runner-dispatcher = {
      description = "GitHub runner pool dispatcher (scale set to per-job VMs)";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      serviceConfig = {
        ExecStart = lib.escapeShellArgs (
          [ "${dispatcherPackage}/bin/runner-dispatcher" ]
          ++ [
            "-scale-set-name" cfg.scaleSetName
            "-runner-group" cfg.runnerGroup
            "-labels" (lib.concatStringsSep "," cfg.labels)
            "-max-capacity" (toString cfg.maxCapacity)
            "-vm-driver" "systemd"
            "-vm-jit-dir" cfg.jitDir
            "-heartbeat-repo" cfg.heartbeatRepo
            "-heartbeat-variable" cfg.heartbeatVariable
            "-app-client-id" cfg.app.clientId
            "-app-installation-id" (toString cfg.app.installationId)
            "-app-private-key-file" (toString cfg.app.privateKeyFile)
          ]
        );
        Restart = "always";
        RestartSec = "5s";
      };
    };
  };
}
