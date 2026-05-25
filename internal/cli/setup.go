package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"dotenv-sync/internal/config"
	"dotenv-sync/internal/provider/keepass"
	"dotenv-sync/internal/report"
	"golang.org/x/term"
)

// ensureConfig checks whether .envsync.yaml exists. If it doesn't and stdin
// is a TTY, it runs the interactive first-run setup and writes the config.
// Returns (true, nil) if setup ran and succeeded — the caller should reload
// config and continue. Returns (false, nil) if config already exists.
// Returns (false, err) if setup was cancelled or failed — the caller should
// stop cleanly without printing another error.
func ensureConfig(s streams, opts *rootOptions) (setupRan bool, cfg config.Config, err error) {
	cfg, err = loadConfig(opts)
	if err != nil {
		return false, config.Config{}, err
	}

	_, statErr := os.Stat(cfg.ConfigFile)
	isInteractive := term.IsTerminal(int(os.Stdin.Fd()))
	if !os.IsNotExist(statErr) || !isInteractive {
		// Config exists, or we're not interactive — nothing to do.
		return false, cfg, nil
	}

	if err := runFirstTimeSetup(s, cfg); err != nil {
		// Setup cancelled or failed — message already printed.
		return false, config.Config{}, err
	}

	// Reload config so callers get the freshly written values.
	cfg, err = loadConfig(opts)
	if err != nil {
		return false, config.Config{}, err
	}
	return true, cfg, nil
}

// runFirstTimeSetup prompts the user for provider preferences and writes
// .envsync.yaml. Nothing is written to disk until all inputs are validated.
func runFirstTimeSetup(s streams, cfg config.Config) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintln(s.stdout, "No .envsync.yaml found. Let's set up dotenv-sync.")
	fmt.Fprintln(s.stdout, "")

	// Ask which provider to use. Default is bitwarden.
	fmt.Fprint(s.stdout, "Provider? [bitwarden/keepass] (default: bitwarden): ")
	providerInput, _ := reader.ReadString('\n')
	providerInput = strings.TrimSpace(strings.ToLower(providerInput))
	if providerInput == "" {
		providerInput = "bitwarden"
	}

	if providerInput != "bitwarden" && providerInput != "keepass" {
		fmt.Fprintf(s.stdout, "Setup failed: unknown provider %q — must be bitwarden or keepass. Please run again.\n", providerInput)
		return fmt.Errorf("setup cancelled")
	}

	var dbPath, groupName string

	if providerInput == "keepass" {
		fmt.Fprint(s.stdout, "Path to KeePass database (.kdbx): ")
		dbPath, _ = reader.ReadString('\n')
		dbPath = strings.TrimSpace(dbPath)
		if dbPath == "" {
			fmt.Fprintln(s.stdout, "Setup failed: KeePass database path cannot be empty. Please run again.")
			return fmt.Errorf("setup cancelled")
		}

		if _, err := os.Stat(dbPath); err != nil {
			fmt.Fprintf(s.stdout, "Setup failed: database file not found at %q. Please run again.\n", dbPath)
			return fmt.Errorf("setup cancelled")
		}

		fmt.Fprint(s.stdout, "Group name for env vars (default: dotenv): ")
		groupName, _ = reader.ReadString('\n')
		groupName = strings.TrimSpace(groupName)
		if groupName == "" {
			groupName = "dotenv"
		}

		// Verify the group exists in the vault before writing anything.
		client := keepass.NewKPXCClient()
		fmt.Fprintf(os.Stderr, "Enter KeePass master password for %s: ", dbPath)
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(s.stdout, "Setup failed: could not read master password. Please run again.")
			return fmt.Errorf("setup cancelled")
		}
		client.Password = strings.TrimSpace(string(pw))

		if _, err := client.ListGroup(context.Background(), dbPath, groupName); err != nil {
			fmt.Fprintf(s.stdout, "Setup failed: group %q not found in %s. Please run again.\n", groupName, dbPath)
			return fmt.Errorf("setup cancelled")
		}
	}

	// All inputs valid — write the config.
	var sb strings.Builder
	sb.WriteString("provider: " + providerInput + "\n")
	sb.WriteString("schema_file: .env.example\n")
	sb.WriteString("env_file: .env\n")
	if providerInput == "keepass" {
		sb.WriteString("keepass_database: " + dbPath + "\n")
		sb.WriteString("keepass_group: " + groupName + "\n")
	}

	if err := os.WriteFile(cfg.ConfigFile, []byte(sb.String()), 0o600); err != nil {
		return report.NewAppError("E006", report.ExitOperational, "config file could not be written", "setup could not create .envsync.yaml", "check file permissions and retry", err)
	}

	fmt.Fprintln(s.stdout, "")
	fmt.Fprintln(s.stdout, "WRITTEN "+cfg.ConfigFile)
	fmt.Fprintln(s.stdout, "")
	return nil
}