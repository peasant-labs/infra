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
          PEM private key read by the dispatcher service. With
          {option}`app.encryptAtRest` it is only the bootstrap source: a
          one-shot unit encrypts it with systemd-creds, and the plaintext file
          is made inaccessible to the running dispatcher.
        '';
      };
      encryptAtRest = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Encrypt the private key with systemd-creds (host key and TPM2 when
          available) and hand it to the dispatcher with
          `LoadCredentialEncrypted=`. The encrypted blob is provisioned once
          on first boot; reinstalling the host requires the original PEM again.
        '';
      };
      encryptedKeyPath = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/runner-dispatcher/app-private-key.cred";
        description = "Where the systemd-creds encrypted private key is provisioned.";
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

    # Bootstrap the encrypted credential once. The plaintext source stays the
    # unit's business only until this has run; the dispatcher itself cannot
    # read it (see InaccessiblePaths below).
    systemd.services.runner-dispatcher-credential = lib.mkIf cfg.app.encryptAtRest {
      description = "Provision the encrypted dispatcher credential";
      wantedBy = [ "multi-user.target" ];
      before = [ "runner-dispatcher.service" ];
      unitConfig.ConditionPathExists = "!${cfg.app.encryptedKeyPath}";
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      script = ''
        set -euo pipefail
        install -d -m 0700 "$(dirname ${lib.escapeShellArg cfg.app.encryptedKeyPath})"
        ${pkgs.systemd}/bin/systemd-creds encrypt --name=app-private-key \
          ${lib.escapeShellArg (toString cfg.app.privateKeyFile)} \
          ${lib.escapeShellArg cfg.app.encryptedKeyPath}
      '';
    };

    systemd.services.runner-dispatcher = lib.mkMerge [
      {
        description = "GitHub runner pool dispatcher (scale set to per-job VMs)";
        wantedBy = [ "multi-user.target" ];
        wants = [ "network-online.target" ];
        after = [ "network-online.target" ]
          ++ lib.optional cfg.app.encryptAtRest "runner-dispatcher-credential.service";
        requires = lib.optional cfg.app.encryptAtRest "runner-dispatcher-credential.service";
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
              "-app-private-key-file" "%d/app-private-key"
            ]
          );
          # The running dispatcher gets the key only through its credential
          # directory; the bootstrap plaintext (and the encrypted blob) are
          # out of its reach.
          InaccessiblePaths = [
            (toString cfg.app.privateKeyFile)
            cfg.app.encryptedKeyPath
          ];
          Restart = "always";
          RestartSec = "5s";
          # Shutdown runs the dispatcher's drain (default 10 minutes: stop a
          # boot in flight, reclaim idle slots, wait for running jobs), and
          # each stop call itself allows up to 5 minutes for a wedged guest.
          # systemd's 90-second default would SIGKILL a graceful drain, so the
          # stop timeout must cover the drain timeout plus one stop cycle.
          TimeoutStopSec = "660s";
        };
      }
      (lib.mkIf cfg.app.encryptAtRest {
        serviceConfig.LoadCredentialEncrypted = "app-private-key:${cfg.app.encryptedKeyPath}";
      })
      (lib.mkIf (!cfg.app.encryptAtRest) {
        serviceConfig.LoadCredential = "app-private-key:${toString cfg.app.privateKeyFile}";
      })
    ];
  };
}
