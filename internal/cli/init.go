package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"dotenv-sync/internal/config"
	"dotenv-sync/internal/envfile"
	"dotenv-sync/internal/provider/keepass"
	"dotenv-sync/internal/report"
	syncpkg "dotenv-sync/internal/sync"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newInitCommand(s streams, opts *rootOptions) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate .env.example from .env, with first-run provider setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(opts)
			if err != nil {
				return err
			}

			// First-run setup: if no .envsync.yaml exists yet and we are
			// running interactively (stdin is a TTY), ask the user which
			// provider they want and write the config file before continuing.
			// When stdin is not a TTY (tests, pipes, CI) we skip setup and
			// let the normal init behaviour run with the default config.
			_, statErr := os.Stat(cfg.ConfigFile)
			isInteractive := term.IsTerminal(int(os.Stdin.Fd()))
			if os.IsNotExist(statErr) && !dryRun && isInteractive {
				if err := runFirstTimeSetup(s, cfg); err != nil {
					// Setup was cancelled or failed — message already printed, exit cleanly.
					return nil
				}
				// Setup succeeded — config is written. Stop here and let the
				// user run ds sync. Don't try to run PlanInit which needs .env.
				fmt.Fprintln(s.stdout, "Run 'ds sync' to populate your .env file.")
				return nil
			}

			plan, target, err := syncpkg.PlanInit(cfg)
			for _, change := range plan.Changes {
				fmt.Fprintln(s.stdout, report.ChangeLine(change.ChangeType, change.Key, change.After))
			}
			if err != nil {
				return err
			}
			summary := syncpkg.Summarize(plan.Changes)
			if dryRun {
				fmt.Fprintln(s.stdout, report.SummaryLine(report.StatusChecked, cfg.SchemaFile, summary, "dry-run"))
				return nil
			}
			if !plan.WriteRequired {
				fmt.Fprintln(s.stdout, report.SummaryLine(report.StatusUnchanged, cfg.SchemaFile, summary, "already up to date"))
				return nil
			}
			if _, err := envfile.WriteDocument(cfg.SchemaFile, target); err != nil {
				return report.NewAppError("E006", report.ExitOperational, "schema file could not be written", "init could not update .env.example", "check file permissions and retry", err)
			}
			fmt.Fprintln(s.stdout, report.SummaryLine(report.StatusWritten, cfg.SchemaFile, summary, ""))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

// runFirstTimeSetup prompts the user for provider preferences and writes
// .envsync.yaml. It is only called when no config file exists yet.
// Nothing is written to disk until all inputs are collected and validated.
func runFirstTimeSetup(s streams, cfg config.Config) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintln(s.stdout, "No .envsync.yaml found. Let's set up dotenv-sync.")
	fmt.Fprintln(s.stdout, "")

	// --- Collect all inputs first ---

	// Ask which provider to use. Default is bitwarden.
	fmt.Fprint(s.stdout, "Provider? [bitwarden/keepass] (default: bitwarden): ")
	providerInput, _ := reader.ReadString('\n')
	providerInput = strings.TrimSpace(strings.ToLower(providerInput))
	if providerInput == "" {
		providerInput = "bitwarden"
	}

	// Validate provider before asking any further questions.
	if providerInput != "bitwarden" && providerInput != "keepass" {
		fmt.Fprintf(s.stdout, "Setup failed: unknown provider %q — must be bitwarden or keepass. Please run ds init again.\n", providerInput)
		return fmt.Errorf("setup cancelled")
	}

	var dbPath, groupName string

	if providerInput == "keepass" {
		// Ask for the database path.
		fmt.Fprint(s.stdout, "Path to KeePass database (.kdbx): ")
		dbPath, _ = reader.ReadString('\n')
		dbPath = strings.TrimSpace(dbPath)
		if dbPath == "" {
			fmt.Fprintln(s.stdout, "Setup failed: KeePass database path cannot be empty. Please run ds init again.")
			return fmt.Errorf("setup cancelled")
		}

		// Validate the file exists before asking anything else.
		if _, err := os.Stat(dbPath); err != nil {
			fmt.Fprintf(s.stdout, "Setup failed: database file not found at %q. Please run ds init again.\n", dbPath)
			return fmt.Errorf("setup cancelled")
		}

		// Ask for the group name, defaulting to "dotenv".
		fmt.Fprint(s.stdout, "Group name for env vars (default: dotenv): ")
		groupName, _ = reader.ReadString('\n')
		groupName = strings.TrimSpace(groupName)
		if groupName == "" {
			groupName = "dotenv"
		}

		// Prompt for master password and verify the group actually exists
		// in the vault. Nothing is written to disk until this passes.
		client := keepass.NewKPXCClient()
		fmt.Fprintf(os.Stderr, "Enter KeePass master password for %s: ", dbPath)
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(s.stdout, "Setup failed: could not read master password. Please run ds init again.")
			return fmt.Errorf("setup cancelled")
		}
		client.Password = strings.TrimSpace(string(pw))

		_, err = client.ListGroup(context.Background(), dbPath, groupName)
		if err != nil {
			fmt.Fprintf(s.stdout, "Setup failed: group %q not found in %s. Please run ds init again.\n", groupName, dbPath)
			return fmt.Errorf("setup cancelled")
		}
	}

	// --- All inputs valid — now build and write the config ---

	var sb strings.Builder
	sb.WriteString("provider: " + providerInput + "\n")
	sb.WriteString("schema_file: .env.example\n")
	sb.WriteString("env_file: .env\n")
	if providerInput == "keepass" {
		sb.WriteString("keepass_database: " + dbPath + "\n")
		sb.WriteString("keepass_group: " + groupName + "\n")
	}

	if err := os.WriteFile(cfg.ConfigFile, []byte(sb.String()), 0o600); err != nil {
		return report.NewAppError("E006", report.ExitOperational, "config file could not be written", "init could not create .envsync.yaml", "check file permissions and retry", err)
	}

	fmt.Fprintln(s.stdout, "")
	fmt.Fprintln(s.stdout, "WRITTEN "+cfg.ConfigFile)
	fmt.Fprintln(s.stdout, "")
	return nil
}