package main

import (
	"errors"
	"fmt"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// configReadError maps a config blob read failure to an exit-1 internal
// error: the file could not be read at all, so nothing was judged.
func configReadError(path, rev string, err error) error {
	if rev == "" {
		return fmt.Errorf("cannot read release config %q: %v. Create %s or fix the path", path, err, intent.DefaultConfigPath)
	}
	return fmt.Errorf("cannot read release config %q at %q: %v. Confirm the revision carries the config file", path, rev, err)
}

// runCheckConfig implements the check-config command. An unreadable file
// exits 1; an invalid file exits 2 with the config-invalid error line.
func runCheckConfig(flags flagValues) error {
	path := flags.ValueOr(intent.FlagConfig, intent.DefaultConfigPath)
	if _, err := loadConfig(intent.CommandCheckConfig, path); err != nil {
		return err
	}
	return emit(outBool(intent.OutputOK, true))
}

// loadConfig reads one working-tree release config through the contract
// package: a read failure stays a plain error for exit 1, a parse failure is
// the typed config-invalid rejection for exit 2.
func loadConfig(command intent.Command, path string) (intent.Config, error) {
	cfg, err := intent.LoadConfig(path)
	if err != nil {
		var ie *intent.Error
		if errors.As(err, &ie) {
			return intent.Config{}, rejectAs(command, err)
		}
		return intent.Config{}, configReadError(path, "", err)
	}
	return cfg, nil
}
