package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/db"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// The Owner CLI (ADR 0003): bootstrap and password recovery happen on the
// server, never over HTTP and never by email. Passwords are prompted twice on
// a terminal or read once from standard input for automation; they are never
// accepted as arguments or printed.
func runAdmin(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: api [serve|create-owner|reset-password]")
	}
	switch args[0] {
	case "create-owner":
		return createOwner(args[1:], logger)
	case "reset-password":
		return resetPassword(args[1:], logger)
	default:
		return fmt.Errorf("unknown command %q (expected serve, create-owner or reset-password)", args[0])
	}
}

func adminService(ctx context.Context, logger *slog.Logger) (*auth.Service, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	pool, err := db.Connect(ctx, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	ring, err := keys.Load(cfg.MasterKeyFile)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return auth.New(pool, auth.NewKeyring(ring), audit.New(pool, logger)), pool.Close, nil
}

func createOwner(args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("create-owner", flag.ContinueOnError)
	username := fs.String("username", "", "sign-in name")
	displayName := fs.String("display-name", "", "name shown in the app")
	timezone := fs.String("timezone", "Asia/Calcutta", "IANA timezone for Save times and billing months")
	stdin := fs.Bool("password-stdin", false, "read the password once from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *displayName == "" {
		return errors.New("--username and --display-name are required")
	}
	password, err := readPassword(*stdin, true)
	if err != nil {
		return err
	}
	ctx := context.Background()
	svc, closePool, err := adminService(ctx, logger)
	if err != nil {
		return err
	}
	defer closePool()
	account, err := svc.CreateOwner(ctx, *username, *displayName, *timezone, password)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created owner %s (%s), timezone %s\n", account.Username, account.ID, account.Timezone)
	return nil
}

func resetPassword(args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	stdin := fs.Bool("password-stdin", false, "read the password once from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	password, err := readPassword(*stdin, true)
	if err != nil {
		return err
	}
	ctx := context.Background()
	svc, closePool, err := adminService(ctx, logger)
	if err != nil {
		return err
	}
	defer closePool()
	if err := svc.ResetPassword(ctx, password); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "password reset; every device session was revoked and pending pairings rejected")
	return nil
}

// readPassword prompts twice on a terminal, or reads one line from stdin
// when asked to. A password never appears in argv or in any output.
func readPassword(fromStdin, confirm bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if !term.IsTerminal(int(syscall.Stdin)) {
		return "", errors.New("no terminal to prompt on; pass --password-stdin")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Again: ")
		second, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("passwords did not match")
		}
	}
	return string(first), nil
}
