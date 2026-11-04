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

  # The legacy persistent-container pool, when this host imports its module.
  # Both modules are in this repository, so reading the sibling's option adds
  # no dependency on another repository's module; `or null` keeps this module
  # evaluating on a host that imports only the scale-set dispatcher.
  legacyPool = config.CUSTOM.services.github-runner or null;

  # Labels the host still advertises for a pool it has already disabled. They
  # are legitimate only while a caller still routes to them: the pool is
  # retired, the label is a transitional residue until the last caller
  # migrates. This is a warning, not an assertion, because failing here would
  # forbid exactly the intermediate state a retirement passes through.
  transitionalLabels =
    if legacyPool == null || legacyPool.enable then [ ]
    else lib.intersectLists cfg.labels legacyPool.labels;

  quoted = labels: lib.concatMapStringsSep ", " (label: ''"${label}"'') labels;
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
      default = [ "self-hosted" "linux" "x64" "microvm" ];
      description = ''
        Labels carried by the scale set: the selectors a caller puts in
        `runs-on`. They are applied when the scale set is created; the
        dispatcher reuses an existing scale set unchanged, so a change here
        reaches a scale set only after that scale set is created again.

        A label belonging to a legacy persistent-container pool may stay while a
        caller still routes to it, and the module warns while it does. Removing
        it is an operator action in two steps, described in
        `docs/runner-pool.md`.
      '';
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
    # A retired pool's label is not an error while a caller still routes to it;
    # it becomes a stale selector the moment the last caller migrates. Warn
    # every time the configuration still advertises one, so the residue cannot
    # be forgotten, and say what removal actually takes.
    warnings = lib.optional (transitionalLabels != [ ]) ''
      CUSTOM.services.runner-dispatcher.labels still carries ${quoted transitionalLabels} while
      CUSTOM.services.github-runner.enable is false.

      While a caller still routes to ${quoted transitionalLabels}, the label is a
      known transitional residue, not a configuration error. Once the last such
      caller has migrated it is a stale selector for a label this host no longer
      needs, and removing it takes two steps: drop it from this list, then update the
      scale set itself while that set is idle. A switch never rewrites a scale set's
      labels and the pool-health record does not carry them, so only a read of the
      scale set can prove the live set.
      See docs/runner-pool.md, "Retiring a host's persistent container pool".
    '';

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
              # Applied when the scale set is created: the dispatcher reuses an
              # existing scale set by name and never rewrites its labels, so a
              # label removed here disappears only once that scale set is
              # recreated.
              "-labels" (lib.concatStringsSep "," cfg.labels)
              "-max-capacity" (toString cfg.maxCapacity)
              "-vm-driver" "systemd"
              "-vm-jit-dir" cfg.jitDir
              # Reset each slot's virtiofsd before boot in a transaction of its
              # own; the daemons exit with the VM and are never respawned.
              # Doubled percent: systemd expands %s (user shell) in ExecStart
              # lines, so the binary would receive its own shell path instead
              # of a format verb. %% survives to the process as %s.
              "-vm-virtiofsd-template" "microvm-virtiofsd@%%s.service"
              "-heartbeat-repo" cfg.heartbeatRepo
              "-heartbeat-variable" cfg.heartbeatVariable
              # Bound the shutdown wait for in-flight jobs so a rebuild is not
              # held up. A job still running past it is left alone: its VM
              # powers itself off when the job ends, and the next start
              # reconciles the slot.
              "-drain-timeout" "90s"
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
          # The drain above waits at most 90 s. systemd SIGKILLs at
          # TimeoutStopSec and its default is exactly 90 s, so a drain of the
          # same size would be killed as it finished. Give the stop path a
          # little room.
          TimeoutStopSec = 120;
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
