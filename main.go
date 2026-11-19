package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "imapexport: canceled")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "imapexport:", err)
		os.Exit(1)
	}
}

type mailboxNames []string

func (names *mailboxNames) String() string { return strings.Join(*names, ", ") }

func (names *mailboxNames) Set(name string) error {
	if name == "" {
		return errors.New("mailbox name must not be empty")
	}
	*names = append(*names, name)
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()

	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: imapexport <list|export> [options]")
		fmt.Fprintln(stdout, "  list    Show the mailboxes available on the IMAP server")
		fmt.Fprintln(stdout, "  export  Save one mbox per mailbox in a single ZIP file")
		fmt.Fprintln(stdout, "Run imapexport <command> --help for its options.")
		return nil
	}
	command := args[0]
	if command != "list" && command != "export" {
		return fmt.Errorf("unknown command %q (use --help)", command)
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	host := flags.String("host", "", "IMAP server hostname or IP address")
	port := flags.Int("port", 0, "IMAP port (default 993, or 143 with --starttls)")
	username := flags.String("user", "", "IMAP username, usually the full email address")
	startTLS := flags.Bool("starttls", false, "Use STARTTLS instead of implicit TLS")
	var output string
	var names mailboxNames
	if command == "export" {
		flags.StringVar(&output, "output", "", "Path to a new ZIP file containing mbox files")
		flags.Var(&names, "mailbox", "Exact mailbox name from list (repeatable, default all)")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(stdout, "Usage: imapexport %s [options]\n", command)
		flags.SetOutput(stdout)
		flags.PrintDefaults()
		fmt.Fprintln(stdout, "\nSet IMAP_PASSWORD for unattended use, or enter the password at the prompt.")
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *host == "" || *username == "" {
		return errors.New("--host and --user are required")
	}
	if strings.Contains(*host, ":") && net.ParseIP(*host) == nil {
		return errors.New("--host expects a hostname or IP address without a port (use --port)")
	}
	if *port == 0 {
		*port = 993
		if *startTLS {
			*port = 143
		}
	}
	if *port < 1 || *port > 65535 {
		return errors.New("--port must be between 1 and 65535")
	}
	if command == "export" {
		if output == "" {
			return errors.New("--output is required")
		}
		if _, err := os.Lstat(output); err == nil {
			return fmt.Errorf("output already exists: %q", output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check output path: %w", err)
		}
	}

	password, err := readPassword(ctx, stderr)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	client, closeClient, err := login(ctx, address, *host, *username, password, *startTLS)
	if err != nil {
		return err
	}
	defer closeClient()

	mailboxes, err := listMailboxes(client)
	if err != nil {
		return err
	}
	if command == "list" {
		for _, mailbox := range mailboxes {
			label := ""
			if !mailbox.Selectable {
				label = " (container only)"
			}
			if _, err := fmt.Fprintf(stdout, "%q%s\n", mailbox.Name, label); err != nil {
				return err
			}
		}
		return nil
	}

	mailboxes, err = selectMailboxes(mailboxes, names)
	if err != nil {
		return err
	}
	counts, err := exportArchive(ctx, client, output, mailboxes, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved %s (%d messages, %d mailboxes)\n", output, counts.Messages, counts.Mailboxes)
	return nil
}

func readPassword(ctx context.Context, stderr io.Writer) (string, error) {
	if password := os.Getenv("IMAP_PASSWORD"); password != "" {
		return password, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("set IMAP_PASSWORD when running without an interactive terminal")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("prepare password prompt: %w", err)
	}
	defer term.Restore(fd, state)

	type result struct {
		password string
		err      error
	}
	input := make(chan result, 1)
	go func() {
		terminal := term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{os.Stdin, stderr}, "")
		password, err := terminal.ReadPassword("IMAP password: ")
		input <- result{password, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprint(stderr, "\r\n")
		return "", ctx.Err()
	case r := <-input:
		if errors.Is(r.err, io.EOF) {
			fmt.Fprint(stderr, "\r\n")
			return "", context.Canceled
		}
		if r.err != nil {
			return "", fmt.Errorf("read password: %w", r.err)
		}
		if r.password == "" {
			return "", errors.New("password must not be empty")
		}
		return r.password, nil
	}
}
