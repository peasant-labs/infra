// Command runner-dispatcher runs one GitHub runner scale set on a pool of
// per-job VMs. The hypervisor driver is not wired yet: the command currently
// runs against the in-memory driver to exercise queue handling end to end.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/dispatcher"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/githubapp"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat/githubvar"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/scalesetadapter"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/dryrun"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/subprocess"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/systemdvm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "runner-dispatcher: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		githubConfigURL = flag.String("github-config-url", "https://github.com/peasant-labs", "GitHub organization or enterprise URL")
		scaleSetName    = flag.String("scale-set-name", "desktop-microvm", "runner scale set name, also the workflow label")
		runnerGroup     = flag.String("runner-group", "default", "runner group name")
		labels          = flag.String("labels", "", "comma-separated extra scale-set labels")
		maxCapacity     = flag.Int("max-capacity", 4, "maximum concurrent runner VMs")
		vmClass         = flag.String("vm-class", "default", "VM sizing class handed to the driver")
		vmNamePrefix    = flag.String("vm-name-prefix", "runner-vm", "runner VM name prefix; names are <prefix>-1..max-capacity")
		workFolder      = flag.String("runner-work-folder", "_work", "work folder inside the runner VM")
		appClientID     = flag.String("app-client-id", "", "GitHub App client id")
		appInstallation = flag.Int64("app-installation-id", 0, "GitHub App installation id")
		appKeyFile      = flag.String("app-private-key-file", "", "path to the GitHub App private key (PEM)")
		drainTimeout    = flag.Duration("drain-timeout", 10*time.Minute, "maximum wait for running jobs on shutdown")
		vmBootCommand   = flag.String("vm-boot-command", "", "host-provided VM boot command for -vm-driver=subprocess")
		vmJITDir        = flag.String("vm-jit-dir", filepath.Join(os.TempDir(), "runner-dispatcher-jit"), "directory holding per-VM JIT config files")
		vmCacheDir      = flag.String("vm-cache-dir", "", "shared cache directory handed to every VM")
		vmKillTimeout   = flag.Duration("vm-kill-timeout", 10*time.Second, "SIGTERM-to-SIGKILL grace period per VM")
		vmDriver        = flag.String("vm-driver", "dryrun", "VM driver: dryrun, subprocess or systemd")
		vmUnitTemplate  = flag.String("vm-unit-template", "microvm@%s.service", "systemd unit template for -vm-driver=systemd")
		vmSlots         = flag.String("vm-slots", "", "comma-separated systemd slots for -vm-driver=systemd; defaults to <name-prefix>-1..max-capacity")
		heartbeatRepo   = flag.String("heartbeat-repo", "peasant-labs/infra", "owner/name of the repository holding the pool-health variable")
		heartbeatVar    = flag.String("heartbeat-variable", "RUNNER_POOL_HEALTH", "repository variable for the pool-health record; empty disables publishing")
	)
	var vmBootArgs []string
	flag.Func("vm-boot-arg", "VM boot argument template (repeatable); placeholders {name} {jit-file} {cache-dir} {job-id}", func(value string) error {
		vmBootArgs = append(vmBootArgs, value)
		return nil
	})
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *appClientID == "" || *appInstallation == 0 || *appKeyFile == "" {
		return errors.New("-app-client-id, -app-installation-id and -app-private-key-file are required")
	}
	key, err := os.ReadFile(*appKeyFile)
	if err != nil {
		return fmt.Errorf("read app private key: %w", err)
	}

	client, err := scalesetadapter.NewClient(scalesetadapter.AppConfig{
		GitHubConfigURL: *githubConfigURL,
		ClientID:        *appClientID,
		InstallationID:  *appInstallation,
		PrivateKey:      string(key),
	})
	if err != nil {
		return err
	}

	owner := *scaleSetName
	scaleSetID, err := client.EnsureScaleSet(context.Background(), *scaleSetName, *runnerGroup, splitLabels(*labels))
	if err != nil {
		return err
	}

	var heartbeatPublisher heartbeat.Publisher
	if *heartbeatVar != "" {
		tokens, err := githubapp.New(githubapp.Config{
			ClientID:       *appClientID,
			InstallationID: *appInstallation,
			PrivateKeyPEM:  string(key),
		})
		if err != nil {
			return err
		}
		pub, err := githubvar.New(githubvar.Config{
			Repo:     *heartbeatRepo,
			Variable: *heartbeatVar,
			Tokens:   tokens,
		})
		if err != nil {
			return err
		}
		heartbeatPublisher = pub
	}
	session, err := client.Session(context.Background(), scaleSetID, owner)
	if err != nil {
		return err
	}
	defer func() {
		if err := session.Close(context.Background()); err != nil {
			logger.Warn("close session", "err", err)
		}
	}()

	// Driver selection: the in-memory driver exercises queue handling, the
	// subprocess driver supervises a host boot command, and the systemd driver
	// starts the microvm.nix units declared by the host configuration.
	var driver vm.Driver
	switch *vmDriver {
	case "dryrun":
		driver = dryrun.New()
		logger.Warn("using the in-memory VM driver; no runner VMs are booted")
	case "subprocess":
		if *vmBootCommand == "" {
			return errors.New("-vm-boot-command is required for -vm-driver=subprocess")
		}
		sub, err := subprocess.New(subprocess.Config{
			Command:     *vmBootCommand,
			Args:        vmBootArgs,
			JITDir:      *vmJITDir,
			CacheDir:    *vmCacheDir,
			KillTimeout: *vmKillTimeout,
			Logger:      logger,
		})
		if err != nil {
			return err
		}
		driver = sub
	case "systemd":
		slots := splitLabels(*vmSlots)
		if len(slots) == 0 {
			for i := 1; i <= *maxCapacity; i++ {
				slots = append(slots, fmt.Sprintf("%s-%d", *vmNamePrefix, i))
			}
		}
		sd, err := systemdvm.New(systemdvm.Config{
			Slots:        slots,
			UnitTemplate: *vmUnitTemplate,
			JITDir:       *vmJITDir,
			Logger:       logger,
		})
		if err != nil {
			return err
		}
		driver = sd
	default:
		return fmt.Errorf("unknown -vm-driver %q", *vmDriver)
	}

	d := dispatcher.New(dispatcher.Config{
		MaxCapacity: *maxCapacity,
		Class:       *vmClass,
		NamePrefix:  *vmNamePrefix,
		Logger:      logger,
		Heartbeat:   heartbeatPublisher,
	}, session, client.JITMinter(scaleSetID, *workFolder), driver)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("dispatch: %w", err)
	}

	logger.Info("draining", "timeout", drainTimeout.String())
	drainCtx, cancel := context.WithTimeout(context.Background(), *drainTimeout)
	defer cancel()
	if err := d.Drain(drainCtx); err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	logger.Info("drained")
	return nil
}

func splitLabels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if label := strings.TrimSpace(part); label != "" {
			out = append(out, label)
		}
	}
	return out
}
