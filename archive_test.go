package main

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

const fixtureMessage = "Subject: Test attachment\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=parts\r\n\r\n" +
	"--parts\r\nContent-Type: text/plain\r\n\r\nFrom here\r\n>From before\r\n>>From earlier\r\n" +
	"--parts\r\nContent-Type: application/octet-stream; name=sample.bin\r\n" +
	"Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment\r\n\r\nAAEC/w==\r\n--parts--"

func TestExportArchive(t *testing.T) {
	for _, scenario := range []string{"complete", "missing UID"} {
		t.Run(scenario, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := imapclient.New(clientConn, nil)
			t.Cleanup(func() { client.Close() })
			done := make(chan struct{})
			go func() {
				defer close(done)
				serveArchiveFixture(t, serverConn, scenario == "complete")
			}()
			t.Cleanup(func() { serverConn.Close(); <-done })

			mailboxes, err := listMailboxes(client)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			output := filepath.Join(dir, "mail.zip")
			counts, err := exportArchive(t.Context(), client, output, mailboxes, io.Discard)
			if scenario == "missing UID" {
				if err == nil || !strings.Contains(err.Error(), "1 of 2 requested messages") {
					t.Fatalf("expected incomplete FETCH error, got %v", err)
				}
				if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
					t.Fatalf("failed export left files: %v, %v", files, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if counts != (exportCounts{Mailboxes: 2, Messages: 2}) {
				t.Fatalf("unexpected export totals: %+v", counts)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
				t.Fatalf("expected only the finished ZIP: %v, %v", files, err)
			}
			verifyFixtureArchive(t, output)
		})
	}
}

func serveArchiveFixture(t *testing.T, conn net.Conn, complete bool) {
	t.Helper()
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	capabilities := "IMAP4rev1"
	if complete {
		capabilities += " ESEARCH"
	}
	if _, err := fmt.Fprintf(conn, "* PREAUTH [CAPABILITY %s] test ready\r\n", capabilities); err != nil {
		t.Error(err)
		return
	}

	message := func(seq, uid int, dateFirst bool) string {
		const date = "INTERNALDATE \"07-Nov-2026 08:09:10 -0600\""
		body := fmt.Sprintf("BODY[] {%d}\r\n%s", len(fixtureMessage), fixtureMessage)
		items := body + " " + date
		if dateFirst {
			items = date + " " + body
		}
		return fmt.Sprintf("* %d FETCH (UID %d %s)\r\n", seq, uid, items)
	}
	selectResponse := func(count int) string {
		return fmt.Sprintf("* %d EXISTS\r\n* OK [UIDVALIDITY 123] valid\r\n", count)
	}
	fetched := message(1, 9, true)
	searchCommand := "UID SEARCH ALL"
	emptyResult, fullResult := "* SEARCH\r\n", "* SEARCH 9 11\r\n"
	if complete {
		fetched += message(2, 11, false)
		searchCommand = "UID SEARCH RETURN"
		emptyResult = "* ESEARCH (TAG \"{tag}\") UID COUNT 0\r\n"
		fullResult = "* ESEARCH (TAG \"{tag}\") UID COUNT 2 ALL 9,11\r\n"
	}
	exchanges := []struct {
		command  string
		response string
	}{
		{`LIST "" "*"`, "* LIST (\\HasChildren) \"/\" INBOX\r\n" +
			"* LIST (\\HasNoChildren) \"/\" INBOX/spam\r\n" +
			"* LIST (\\NOSELECT) \"/\" Shared\r\n"},
		{`EXAMINE INBOX`, selectResponse(0)},
		{searchCommand, emptyResult},
		{`EXAMINE INBOX/spam`, selectResponse(2)},
		{searchCommand, fullResult},
		{"UID FETCH 9,11 ", fetched},
	}
	reader := bufio.NewReader(conn)
	for _, exchange := range exchanges {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Errorf("read command %q: %v", exchange.command, err)
			return
		}
		tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
		// The library may quote mailbox arguments and vary the order of FETCH items.
		if !strings.HasPrefix(strings.ReplaceAll(cmd, `"`, ""), strings.ReplaceAll(strings.TrimSpace(exchange.command), `"`, "")) ||
			strings.HasPrefix(cmd, "UID FETCH") && !strings.Contains(cmd, "BODY.PEEK[]") {
			t.Errorf("expected %q, got %q", exchange.command, cmd)
			return
		}
		response := strings.ReplaceAll(exchange.response, "{tag}", tag)
		if _, err := fmt.Fprintf(conn, "%s%s OK done\r\n", response, tag); err != nil {
			t.Error(err)
			return
		}
	}
}

func verifyFixtureArchive(t *testing.T, filename string) {
	t.Helper()
	reader, err := zip.OpenReader(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	read := func(filename string) []byte {
		file, err := reader.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var paths []string
	for _, file := range reader.File {
		paths = append(paths, file.Name)
	}
	if !slices.Equal(paths, []string{"INBOX.mbox", "INBOX/spam.mbox", "Shared/"}) {
		t.Fatalf("unexpected archive paths: %v", paths)
	}
	if data := read("INBOX.mbox"); len(data) != 0 {
		t.Fatalf("expected an empty mbox, got %q", data)
	}
	message := strings.ReplaceAll(fixtureMessage, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\nFrom here\n>From before\n>>From earlier\n",
		"\n>From here\n>>From before\n>>>From earlier\n")
	record := "From MAILER-DAEMON Sat Nov  7 14:09:10 2026\n" + message + "\n\n"
	if data := read("INBOX/spam.mbox"); string(data) != record+record {
		t.Fatalf("unexpected mbox records: %q", data)
	}
}

func TestMboxLongLines(t *testing.T) {
	var output strings.Builder
	writer := bufio.NewWriter(&output)
	date := time.Date(2026, 11, 7, 14, 9, 10, 0, time.UTC)
	longQuote := strings.Repeat(">", 8192)
	longLine := strings.Repeat("a", 4095)
	source := strings.NewReader(longQuote + "From here\r\n" + longLine + "\r\n>")
	if err := appendMboxMessage(writer, date, source); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "From MAILER-DAEMON Sat Nov  7 14:09:10 2026\n>" + longQuote + "From here\n" + longLine + "\n>\n\n"
	if output.String() != want {
		t.Fatalf("long or unterminated lines did not round-trip (got %d bytes, want %d)", output.Len(), len(want))
	}
}

func TestMailboxPaths(t *testing.T) {
	names := []mailbox{
		{Name: "Fêtes", Delimiter: "/"},
		{Name: "Folder/..", Delimiter: "/"},
		{Name: "Folder.mbox", Delimiter: "/"},
		{Name: "folder", Delimiter: "/"},
		{Name: "folder/CON", Delimiter: "/"},
		{Name: "INBOX.spam", Delimiter: "."},
		{Name: "flat/box"},
	}
	want := []string{"F%C3%AAtes", "Folder/%2E%2E", "Folder%2Embox", "folder~2", "folder~2/%43ON", "INBOX/spam", "flat%2Fbox"}
	if got := mailboxPaths(names); !slices.Equal(got, want) {
		t.Fatalf("mailboxPaths() = %v, want %v", got, want)
	}
}
