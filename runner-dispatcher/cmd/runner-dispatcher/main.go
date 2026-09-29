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
	"strings"
	"syscall"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/dispatcher"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/scalesetadapter"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/dryrun"
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
		vmNamePrefix    = flag.String("vm-name-prefix", "runner", "runner VM name prefix")
		workFolder      = flag.String("runner-work-folder", "_work", "work folder inside the runner VM")
		appClientID     = flag.String("app-client-id", "", "GitHub App client id")
		appInstallation = flag.Int64("app-installation-id", 0, "GitHub App installation id")
		appKeyFile      = flag.String("app-private-key-file", "", "path to the GitHub App private key (PEM)")
		drainTimeout    = flag.Duration("drain-timeout", 10*time.Minute, "maximum wait for running jobs on shutdown")
	)
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
	session, err := client.Session(context.Background(), scaleSetID, owner)
	if err != nil {
		return err
	}
	defer func() {
		if err := session.Close(context.Background()); err != nil {
			logger.Warn("close session", "err", err)
		}
	}()

	// The hypervisor driver lands with the guest-image slice; until then the
	// in-memory driver exercises queue handling without booting VMs.
	var driver vm.Driver = dryrun.New()
	logger.Warn("using the in-memory VM driver; no runner VMs are booted")

	d := dispatcher.New(dispatcher.Config{
		MaxCapacity: *maxCapacity,
		Class:       *vmClass,
		NamePrefix:  *vmNamePrefix,
		Logger:      logger,
	}, session, client.JITMinter(scaleSetID, *vmNamePrefix, *workFolder), driver)

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
