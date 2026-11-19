package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

type mailbox struct {
	Name       string
	Delimiter  string
	Selectable bool
}

func login(ctx context.Context, address, host, username, password string, startTLS bool) (*imapclient.Client, func(), error) {
	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", address, err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	var client *imapclient.Client
	closeClient := func() {
		stop()
		conn.Close()
		if client != nil {
			client.Close()
		}
	}

	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if startTLS {
		client, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: tlsConfig})
	} else {
		tlsConn := tls.Client(conn, tlsConfig)
		handshakeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = tlsConn.HandshakeContext(handshakeCtx)
		cancel()
		if err == nil {
			client = imapclient.New(tlsConn, nil)
		}
	}
	if err != nil {
		closeClient()
		return nil, nil, fmt.Errorf("establish TLS with %s: %w", address, err)
	}
	if err := client.WaitGreeting(); err != nil {
		closeClient()
		return nil, nil, fmt.Errorf("read IMAP greeting: %w", err)
	}

	if client.Caps().Has(imap.AuthCap("PLAIN")) {
		err = client.Authenticate(sasl.NewPlainClient("", username, password))
	} else {
		err = client.Login(username, password).Wait()
	}
	if err != nil {
		closeClient()
		return nil, nil, fmt.Errorf("authenticate %q: %w", username, err)
	}
	// Servers advertising both revisions use modified UTF-7 until IMAP4rev2 is enabled.
	if caps := client.Caps(); caps.Has(imap.CapIMAP4rev1) && caps.Has(imap.CapIMAP4rev2) {
		data, err := client.Enable(imap.CapIMAP4rev2).Wait()
		if err != nil {
			closeClient()
			return nil, nil, fmt.Errorf("enable IMAP4rev2: %w", err)
		}
		if !data.Caps.Has(imap.CapIMAP4rev2) {
			closeClient()
			return nil, nil, fmt.Errorf("server advertised IMAP4rev2 but did not enable it")
		}
	}
	return client, closeClient, nil
}

func listMailboxes(client *imapclient.Client) ([]mailbox, error) {
	data, err := client.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	mailboxes := make([]mailbox, 0, len(data))
	seen := make(map[string]bool, len(data))
	for _, item := range data {
		key := mailboxKey(item.Mailbox)
		if seen[key] {
			return nil, fmt.Errorf("server listed mailbox %q more than once", item.Mailbox)
		}
		seen[key] = true
		box := mailbox{
			Name:       item.Mailbox,
			Selectable: true,
		}
		if item.Delim != 0 {
			box.Delimiter = string(item.Delim)
		}
		for _, attr := range item.Attrs {
			if strings.EqualFold(string(attr), string(imap.MailboxAttrNoSelect)) ||
				strings.EqualFold(string(attr), string(imap.MailboxAttrNonExistent)) {
				box.Selectable = false
			}
		}
		mailboxes = append(mailboxes, box)
	}
	slices.SortFunc(mailboxes, func(a, b mailbox) int { return strings.Compare(a.Name, b.Name) })
	return mailboxes, nil
}

func mailboxKey(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

func selectMailboxes(mailboxes []mailbox, names []string) ([]mailbox, error) {
	if len(names) == 0 {
		return mailboxes, nil
	}

	available := make(map[string]*mailbox, len(mailboxes))
	for i := range mailboxes {
		mailbox := &mailboxes[i]
		available[mailboxKey(mailbox.Name)] = mailbox
	}
	selected := make([]mailbox, 0, len(names))
	for _, name := range names {
		key := mailboxKey(name)
		mailbox, ok := available[key]
		if !ok {
			return nil, fmt.Errorf("mailbox %q was not listed by the server", name)
		}
		if mailbox == nil {
			continue
		}
		if !mailbox.Selectable {
			return nil, fmt.Errorf("mailbox %q is a container with no selectable messages", name)
		}
		selected = append(selected, *mailbox)
		available[key] = nil
	}
	return selected, nil
}
