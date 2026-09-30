{ config
, pkgs
, lib ? pkgs.lib
, ...
}:
let
  cfg = config.CUSTOM.services.github-runner;

  inherit (lib)
    concatMapStringsSep
    escapeShellArg
    genAttrs
    mkEnableOption
    mkIf
    mkOption
    types
    ;

  podman = "${config.virtualisation.podman.package}/bin/podman";

  # Identity model: every writer in a runner's cgroup tree is the host user.
  # The runner container runs as its userns root (container uid 0 -> the host
  # user), and root inside job containers plus `sudo` in the runner container
  # map the same way, because they all share this user's user namespace. A
  # directory written by any of them is therefore owned by the host user and
  # can be cleaned by the others. Running the runner as a non-root container
  # user (for example uid 1001) maps it onto a subuid instead and splits the
  # workspace between two writers that cannot clean up after each other. The
  # de-facto standard runner images run the agent as root for the same reason.
  #
  # Rootless podman still needs the user's subuid/subgid ranges for the job
  # containers' own user namespaces; the assertion below keeps that true.

  # Each runner lives in its own systemd slice so its cgroup tree (the runner
  # container and the job processes inside it) can carry resource limits. The
  # slice name is index-based to keep the implicit slice hierarchy shallow:
  # systemd nests `a-b-c.slice` under `a-b.slice` under `a.slice`.
  instances = map (i: {
    name = "${cfg.name}-${toString i}";
    slice = "github-runner-${toString i}";
  }) (lib.range 1 cfg.count);

  instanceNames = map (instance: instance.name) instances;

  # systemd slice settings for one resource tier; null options are dropped so
  # the slice keeps systemd's default for that knob.
  mkSliceConfig = tier: lib.filterAttrs (_: value: value != null) {
    MemoryMax = tier.memoryMax;
    MemoryHigh = tier.memoryHigh;
    CPUQuota = tier.cpuQuota;
    CPUWeight = tier.cpuWeight;
    TasksMax = tier.tasksMax;
  };

  # The runner image is published by this repository's
  # .github/workflows/runner-image.yml and pinned by digest, so every host runs
  # the same bits with no local build and no registry trust: the pull is
  # verified against the workflow's keyless Sigstore signature before any
  # container starts. `runner-image/Containerfile` stays in this repository as
  # the recipe that produces it.
  #
  # Both values are the peasant-labs defaults, overridable per host. A host
  # running its own pool MUST override BOTH together: imageRef is a digest that
  # only exists once a build has published and signed it, so pointing it at an
  # image this signer never signed fails closed at pull time and no runner
  # starts.

  mkPodmanRunArgs = instance: slice: [
    "run" "--rm"
    # --replace removes a leftover container with the same name after a crash.
    "--replace"
    "--name" "github-runner-${instance}"
    # Run the agent as the userns root (the host user). See the identity-model
    # comment above: this is what keeps the workspace single-owner. The runner
    # refuses to configure or start as root without this acknowledgement.
    "--user" "0"
    "-e" "RUNNER_ALLOW_RUNASROOT=1"
    # Keep the container payload inside the runner's own systemd slice so the
    # slice's MemoryMax/CPUQuota apply to the jobs, not just to the CLI.
    "--cgroup-parent=${slice}.slice"
    "--network=host"
    # The whole state tree is mounted at its host path so sibling containers
    # started by jobs can bind mount workspace paths by identical path.
    "-v" "${cfg.stateDir}:${cfg.stateDir}"
    "-e" "DOCKER_HOST=unix:///var/run/docker.sock"
    "-e" "CONTAINER_HOST=unix:///var/run/docker.sock"
    "-e" "GITHUB_RUNNER_URL=${cfg.url}"
    "-e" "GITHUB_RUNNER_NAME=${instance}"
    "-e" "GITHUB_RUNNER_GROUP=${lib.optionalString (cfg.runnerGroup != null) cfg.runnerGroup}"
    "-e" "GITHUB_RUNNER_LABELS=${lib.concatStringsSep "," cfg.labels}"
    "-e" "GITHUB_RUNNER_EPHEMERAL=${if cfg.ephemeral then "1" else "0"}"
    # No token file is mounted: the PAT is handed to the container through its
    # environment and unset by the entrypoint before the listener starts, so a
    # job step cannot read it from the filesystem or inherit it.
    "-e" "RUNNER_ROOT=${cfg.stateDir}/runners/${instance}"
    "-e" "RUNNER_WORK=${cfg.stateDir}/work/${instance}"
    "-e" "TMPDIR=${cfg.stateDir}/work/${instance}/tmp"
    "-e" "AGENT_TOOLSDIRECTORY=${cfg.stateDir}/cache/_tool"
    "-e" "GOMODCACHE=${cfg.stateDir}/cache/gomod"
    "-e" "GOCACHE=${cfg.stateDir}/cache/gobuild"
  ];

  # The PAT is read by the host user (the module's sops secret is readable
  # there) and handed to the container through podman's own environment with
  # `--env GITHUB_RUNNER_TOKEN` (a name without a value), so the secret never
  # appears in the podman command line. The entrypoint unsets it before the
  # listener starts, so job steps cannot inherit it. The podman socket is
  # resolved when the script runs: systemd specifiers (%t) only expand in the
  # unit's ExecStart line, not inside a script body.
  mkRunScript = instance: slice: pkgs.writeShellScript "github-runner-run-${instance}" ''
    set -euo pipefail
    runtime="''${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
    socket="$runtime/podman/podman.sock"
    GITHUB_RUNNER_TOKEN="$(cat ${cfg.tokenFile})"
    export GITHUB_RUNNER_TOKEN
    exec ${podman} ${lib.escapeShellArgs (mkPodmanRunArgs instance slice)} \
      --volume "$socket:/var/run/docker.sock" \
      --env GITHUB_RUNNER_TOKEN ${escapeShellArg cfg.imageRef}
  '';

  # Pull once and verify the signature; the stamp records the verified
  # reference, so a reboot needs neither the registry nor Sigstore.
  pullImage = pkgs.writeShellScript "github-runner-pull-image" ''
    set -euo pipefail
    stamp=${escapeShellArg "${cfg.stateDir}/.image-verified"}
    if ${podman} image exists ${escapeShellArg cfg.imageRef} \
      && [ -f "$stamp" ] && [ "$(cat "$stamp")" = ${escapeShellArg cfg.imageRef} ]; then
      exit 0
    fi
    ${podman} pull ${escapeShellArg cfg.imageRef}
    ${pkgs.cosign}/bin/cosign verify \
      --certificate-identity ${escapeShellArg cfg.imageSigner} \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      ${escapeShellArg cfg.imageRef}
    printf '%s' ${escapeShellArg cfg.imageRef} > "$stamp"
  '';

  # Drain: wait until every instance this pool manages reports idle in
  # GitHub, so a host rebuild or manual stop never cancels an in-flight job.
  # The busy flag lives in GitHub, not in the container, so the PAT is the
  # only way to observe it. The runner list is re-read every cycle and matched
  # by name: re-registration rotates runner ids (PAT rotation, --replace,
  # ephemeral runners), and an instance that is not registered between jobs is
  # drained. The script never stops anything itself: it exits 0 when drained,
  # 1 on timeout (runners still busy), 2 when the API cannot be read — the
  # caller decides what a timeout means. Failing open here would silently
  # cancel jobs, so a caller that proceeds past exit 1 must say so
  # deliberately.
  drainPackage = pkgs.writeShellScriptBin "github-runner-drain" ''
    set -euo pipefail
    org=${escapeShellArg (lib.last (lib.splitString "/" cfg.url))}
    api="https://api.github.com"
    token="$(cat ${cfg.tokenFile})"
    names_json=${escapeShellArg (builtins.toJSON instanceNames)}
    timeout_s=900 interval_s=15
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --timeout) timeout_s=$2; shift 2 ;;
        --interval) interval_s=$2; shift 2 ;;
        *) printf 'usage: github-runner-drain [--timeout SECONDS] [--interval SECONDS]\n' >&2; exit 64 ;;
      esac
    done
    auth="Authorization: Bearer $token"

    # One name per line for every pool instance currently reporting busy.
    # Names are matched across the whole runner list every cycle, so id
    # rotation does not matter; a runner that is absent is not busy.
    pool_busy() {
      page=1
      while :; do
        resp="$(curl -fsS -H "$auth" "$api/orgs/$org/actions/runners?per_page=100&page=$page")" || return 1
        printf '%s' "$resp" | ${pkgs.jq}/bin/jq -r --argjson names "$names_json" '
          .runners[]
          | select(.name | IN($names[]))
          | select((.busy // false) == true)
          | .name
        '
        count="$(printf '%s' "$resp" | ${pkgs.jq}/bin/jq '(.runners | length) // 0')"
        [ "$count" -lt 100 ] && break
        page=$((page + 1))
      done
    }

    deadline=$(( $(date +%s) + timeout_s ))
    while :; do
      if ! busy="$(pool_busy)"; then
        printf 'github-runner-drain: runner list request failed\n' >&2
        exit 2
      fi
      busy="$(printf '%s' "$busy" | tr '\n' ' ')"
      if [ -z "''${busy//[[:space:]]/}" ]; then
        printf 'drained: no pool runner is busy\n'
        exit 0
      fi
      now="$(date +%s)"
      if [ "$now" -ge "$deadline" ]; then
        printf 'github-runner-drain: TIMEOUT after %ss, still busy: %s\n' "$timeout_s" "$busy" >&2
        exit 1
      fi
      printf 'waiting for in-flight jobs: %s(%ss left)\n' "$busy" "$((deadline - now))"
      sleep "$interval_s"
    done
  '';

  prepareState = pkgs.writeShellScript "github-runner-prepare-state" ''
    set -euo pipefail
    ${concatMapStringsSep "\n" (instance: ''
      mkdir -p ${escapeShellArg cfg.stateDir}/runners/${instance} \
               ${escapeShellArg cfg.stateDir}/work/${instance}/tmp
    '') instanceNames}
    mkdir -p ${escapeShellArg cfg.stateDir}/cache
    # One owner for the whole tree: the host user. The runner container runs as
    # the userns root (host user) and job containers' root and `sudo` map the
    # same way, so nothing here needs a subuid mapping or a socket ACL.
    ${concatMapStringsSep "\n" (instance: ''
      ${podman} unshare chown -R 0:0 \
        ${escapeShellArg cfg.stateDir}/runners/${instance} \
        ${escapeShellArg cfg.stateDir}/work/${instance}
    '') instanceNames}
    ${podman} unshare chown -R 0:0 ${escapeShellArg cfg.stateDir}/cache
  '';
in
{
  options.CUSTOM.services.github-runner = {
    enable = mkEnableOption "GitHub Actions self-hosted runner containers (rootless podman)";

    imageRef = mkOption {
      type = types.str;
      default = "quay.io/peasant-labs/github-runner@sha256:078b20f289f2852c45a92b862894536624b90e759ce9ff2e405b12db9ca565ae";
      description = ''
        Digest-pinned reference to the runner container image. The host pulls
        this exact digest and verifies it against {option}`imageSigner` before
        any container starts, so it must be a digest, never a mutable tag.
        Move it together with {option}`imageSigner`.
      '';
    };

    imageSigner = mkOption {
      type = types.str;
      default = "https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main";
      description = ''
        Sigstore certificate identity the image's signature must carry, the
        exact form
        `https://github.com/<owner>/<repo>/.github/workflows/<workflow>@refs/heads/<branch>`.
        A host publishing its own image overrides this to its own workflow.
      '';
    };

    url = mkOption {
      type = types.str;
      default = "https://github.com/peasant-labs";
      description = "GitHub organization URL the runners register against";
    };

    name = mkOption {
      type = types.str;
      default = "${config.networking.hostName}-container";
      description = "Base runner name; instances are suffixed -1..count";
    };

    count = mkOption {
      type = types.int;
      default = 1;
      description = "Runner containers to run; each handles one job at a time";
    };

    labels = mkOption {
      type = types.listOf types.str;
      default = [ "container" ];
      description = "Extra labels on top of the default self-hosted/linux/x64 labels";
    };

    runnerGroup = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = ''
        Organization runner group. Must already exist in GitHub before the
        containers start, otherwise registration fails.
      '';
    };

    tokenFile = mkOption {
      type = types.path;
      description = ''
        File holding a fine-grained PAT with organization "Self-hosted
        runners: Read and write" permission (or a classic admin:org PAT).
        Must be readable by {option}`user`.
      '';
      example = "/run/secrets/github-runner/token";
    };

    resources = {
      pool = {
        memoryMax = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "48G";
          description = ''
            `MemoryMax` for the pool's parent slice (`github-runner.slice`),
            the ceiling for all runners together; `null` leaves it unlimited.
          '';
        };

        memoryHigh = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "40G";
          description = ''
            `MemoryHigh` soft limit for the pool's parent slice: the kernel
            reclaims and throttles before the hard {option}`resources.pool.memoryMax`
            kill; `null` leaves it unset.
          '';
        };

        cpuQuota = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "2400%";
          description = ''
            `CPUQuota` for the pool's parent slice (100% is one core): a hard
            bandwidth ceiling that reserves headroom for the rest of the
            machine; `null` leaves it unlimited.
          '';
        };

        cpuWeight = mkOption {
          type = types.nullOr types.int;
          default = null;
          example = 100;
          description = ''
            `CPUWeight` for the pool's parent slice, its relative share against
            the rest of the user session when CPU is contended (systemd default
            100); `null` leaves it at the default.
          '';
        };

        tasksMax = mkOption {
          type = types.nullOr types.int;
          default = null;
          example = 16384;
          description = ''
            `TasksMax` for the pool's parent slice; `null` leaves it at
            systemd's default.
          '';
        };
      };

      runner = {
        memoryMax = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "12G";
          description = ''
            `MemoryMax` for each runner's systemd slice. The runner container
            and the job processes inside it run under this limit; `null`
            leaves it unlimited. Containers that jobs start through the podman
            socket (service containers, `docker run` steps, job containers)
            are separate scopes and are NOT covered.
          '';
        };

        memoryHigh = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "10G";
          description = ''
            `MemoryHigh` soft limit per runner slice: reclaim pressure before
            the hard {option}`resources.runner.memoryMax` kill; `null` leaves
            it unset.
          '';
        };

        cpuQuota = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "800%";
          description = ''
            `CPUQuota` hard bandwidth cap per runner slice (100% is one core).
            Prefer {option}`resources.runner.cpuWeight` for CI: a quota caps
            throughput even when the machine is idle.
          '';
        };

        cpuWeight = mkOption {
          type = types.nullOr types.int;
          default = null;
          example = 100;
          description = ''
            `CPUWeight` per runner slice: equal weights divide the pool's CPU
            fairly under contention while a lone runner can still burst across
            idle cores; `null` leaves it at systemd's default (100).
          '';
        };

        tasksMax = mkOption {
          type = types.nullOr types.int;
          default = null;
          example = 4096;
          description = ''
            `TasksMax` for each runner's systemd slice (process/thread cap);
            `null` leaves it at systemd's default.
          '';
        };
      };
    };

    ephemeral = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Register per job and wipe runner state afterwards. Ephemeral runners
        re-register through the PAT before every job.
      '';
    };

    user = mkOption {
      type = types.str;
      default = "minttea";
      description = "Host user whose rootless podman runs the containers and owns the state tree";
    };

    stateDir = mkOption {
      type = types.str;
      default = "/home/${cfg.user}/.local/share/github-runner-containers";
      description = ''
        Host directory holding the runner install copies, workspaces and
        shared caches. It is mounted into the containers at the same path so
        sibling containers can bind mount workspace paths.
      '';
    };

  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = config.users.users.${cfg.user}.subUidRanges != [ ];
        message = "CUSTOM.services.github-runner.user must have subuid ranges for rootless podman";
      }
    ];

    # The runners are rootless podman containers and the docker CLI inside them
    # talks to the host's podman socket, so the host needs podman with the
    # docker-compatible socket exposed. These are nixpkgs options rather than a
    # host-specific wrapper, so the module is usable without any other
    # repository's module present. A host that configures podman itself may set
    # the same values; identical values do not conflict.
    virtualisation.podman.enable = true;
    virtualisation.podman.dockerCompat = true;
    virtualisation.podman.dockerSocket.enable = true;

    systemd.user.services = {
      # Image: pull and verify the digest-pinned runner image from Quay. The
      # repository is public (anonymous pull); the signature check is the trust
      # anchor for what runs.
      github-runner-image = {
        description = "Pull and verify the GitHub Actions runner container image";
        after = [ "podman.socket" ];
        requires = [ "podman.socket" ];
        # User units are visible to every user manager on the host. Without
        # this, a second lingering user would start a second pool against the
        # same state tree (and fail on ownership), so the pool belongs to
        # exactly one account.
        unitConfig.ConditionUser = cfg.user;
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = pullImage;
        };
        wantedBy = [ "default.target" ];
      };

      # Shared directories and the podman socket ACL the containers need.
      github-runner-prepare = {
        description = "Prepare GitHub Actions runner container state";
        after = [ "podman.socket" ];
        requires = [ "podman.socket" ];
        unitConfig.ConditionUser = cfg.user;
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = prepareState;
        };
        wantedBy = [ "default.target" ];
      };

      # One container per instance. Each instance gets its own unit (NixOS
      # writes every systemd.user.services attribute as a unit file; a
      # separate template attribute does not supply ExecStart to them). The
      # unit runs `podman run` in the foreground so systemd owns the
      # lifecycle; the entrypoint registers on first start (or when the PAT
      # rotates) and then launches the listener.
    } // lib.listToAttrs (map (instance: {
      name = "github-runner-container@${instance.name}";
      value = {
        description = "GitHub Actions runner container ${instance.name}";
        after = [ "github-runner-image.service" "github-runner-prepare.service" ];
        requires = [ "github-runner-image.service" "github-runner-prepare.service" ];
        unitConfig.ConditionUser = cfg.user;
        serviceConfig = {
          Slice = "${instance.slice}.slice";
          ExecStart = mkRunScript instance.name instance.slice;
          ExecStop = "${podman} stop --time 60 github-runner-${instance.name}";
          TimeoutStopSec = 90;
          Restart = if cfg.ephemeral then "on-success" else "always";
          RestartSec = 5;
        };
        wantedBy = [ "default.target" ];
      };
    }) instances);

    # One resource slice per runner, all nested under the explicit
    # github-runner.slice pool group. The runner unit and its container payload
    # live inside the runner slice, so MemoryMax/CPUQuota/TasksMax bound what
    # jobs can use; the pool slice bounds all runners together.
    systemd.user.slices = {
      github-runner = {
        description = "GitHub Actions runner pool resource slice";
        sliceConfig = mkSliceConfig cfg.resources.pool;
      };
    } // lib.genAttrs (map (instance: instance.slice) instances) (slice: {
      description = "GitHub Actions runner resource slice ${slice}";
      sliceConfig = mkSliceConfig cfg.resources.runner;
    });

    # The drain tool goes on the system PATH so the host's deploy tooling can
    # call it before a rebuild; the PAT is read at run time from the same
    # tokenFile the registration path uses.
    environment.systemPackages = lib.mkIf cfg.enable [ drainPackage ];
  };
}
