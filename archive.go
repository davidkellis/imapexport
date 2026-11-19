package main

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type exportCounts struct {
	Mailboxes int
	Messages  int
}

func exportArchive(ctx context.Context, client *imapclient.Client, output string, mailboxes []mailbox, progress io.Writer) (exportCounts, error) {
	var counts exportCounts
	if !slices.ContainsFunc(mailboxes, func(box mailbox) bool { return box.Selectable }) {
		return counts, errors.New("no selectable mailboxes to export")
	}
	directory := filepath.Dir(output)
	file, err := os.CreateTemp(directory, ".imapexport-*.partial")
	if err != nil {
		return counts, fmt.Errorf("create temporary archive: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	writer := zip.NewWriter(file)

	paths := mailboxPaths(mailboxes)
	for i, mailbox := range mailboxes {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		if !mailbox.Selectable {
			if _, err := writer.Create(paths[i] + "/"); err != nil {
				return counts, fmt.Errorf("create archive directory for %q: %w", mailbox.Name, err)
			}
			continue
		}
		entry, err := writer.Create(paths[i] + ".mbox")
		if err != nil {
			return counts, fmt.Errorf("create mbox for %q: %w", mailbox.Name, err)
		}
		count, err := exportMailbox(client, entry, mailbox.Name, directory, progress)
		if err != nil {
			return counts, fmt.Errorf("export mailbox %q: %w", mailbox.Name, err)
		}
		counts.Mailboxes++
		counts.Messages += count
	}
	if err := writer.Close(); err != nil {
		return counts, fmt.Errorf("finish ZIP: %w", err)
	}
	if err := file.Sync(); err != nil {
		return counts, fmt.Errorf("sync archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return counts, fmt.Errorf("close archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return counts, err
	}
	// Link the finished file into place atomically without replacing another export.
	if err := os.Link(file.Name(), output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return counts, fmt.Errorf("output already exists: %q", output)
		}
		return counts, fmt.Errorf("publish archive (output filesystem must support hard links): %w", err)
	}
	return counts, nil
}

func exportMailbox(client *imapclient.Client, entry io.Writer, name, spoolDir string, progress io.Writer) (int, error) {
	data, err := client.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, fmt.Errorf("open read-only: %w", err)
	}
	if data.UIDValidity == 0 {
		return 0, errors.New("server did not return a valid UIDVALIDITY value")
	}

	var searchOptions *imap.SearchOptions
	if client.Caps().Has(imap.CapESearch) {
		searchOptions = &imap.SearchOptions{ReturnAll: true, ReturnCount: true}
	}
	search, err := client.UIDSearch(&imap.SearchCriteria{}, searchOptions).Wait()
	if err != nil {
		return 0, fmt.Errorf("find message UIDs: %w", err)
	}
	uidSet, ok := search.All.(imap.UIDSet)
	// An empty ESEARCH result returns COUNT 0 and omits the UID set.
	if (!ok && search.All != nil) || uidSet.Dynamic() {
		return 0, errors.New("server returned an invalid UID search result")
	}
	uids := search.AllUIDs()
	if searchOptions != nil && uint64(search.Count) != uint64(len(uids)) {
		return 0, fmt.Errorf("UID search count was %d but the server listed %d UIDs", search.Count, len(uids))
	}
	fmt.Fprintf(progress, "Exporting %q (%d messages)\n", name, len(uids))
	writer := bufio.NewWriter(entry)
	const batchSize = 100
	for batch := range slices.Chunk(uids, batchSize) {
		if err := fetchMessages(client, writer, spoolDir, batch); err != nil {
			return 0, err
		}
	}
	if err := writer.Flush(); err != nil {
		return 0, fmt.Errorf("finish mbox: %w", err)
	}
	return len(uids), nil
}

func fetchMessages(client *imapclient.Client, writer *bufio.Writer, spoolDir string, uids []imap.UID) error {
	// Ask for the date before the literal, but accept either response order.
	body := &imap.FetchItemBodySection{Peek: true}
	command := client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{body},
	})
	count := 0
	for msg := command.Next(); msg != nil; msg = command.Next() {
		if err := writeMessage(writer, spoolDir, msg, body); err != nil {
			command.Close()
			return fmt.Errorf("fetch message at sequence %d: %w", msg.SeqNum, err)
		}
		count++
	}
	if err := command.Close(); err != nil {
		return fmt.Errorf("fetch messages: %w", err)
	}
	if count != len(uids) {
		return fmt.Errorf("server returned %d of %d requested messages in the batch starting at UID %d", count, len(uids), uids[0])
	}
	return nil
}

func writeMessage(writer *bufio.Writer, spoolDir string, msg *imapclient.FetchMessageData, body *imap.FetchItemBodySection) error {
	var uid imap.UID
	var date time.Time
	var staged *os.File
	bodyFetched := false
	for item := msg.Next(); item != nil; item = msg.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			uid = item.UID
		case imapclient.FetchItemDataInternalDate:
			date = item.Time
		case imapclient.FetchItemDataBodySection:
			if !item.MatchCommand(body) || item.Literal == nil {
				continue
			}
			if uid == 0 || bodyFetched {
				return errors.New("server returned an unexpected message body")
			}
			bodyFetched = true
			source := &countedReader{Reader: item.Literal}
			var err error
			if date.IsZero() {
				staged, err = os.CreateTemp(spoolDir, ".imapexport-message-*")
				if err != nil {
					return fmt.Errorf("stage message UID %d: %w", uid, err)
				}
				defer os.Remove(staged.Name())
				defer staged.Close()
				_, err = io.Copy(staged, source)
			} else {
				err = appendMboxMessage(writer, date, source)
			}
			if err != nil {
				return fmt.Errorf("copy message UID %d: %w", uid, err)
			}
			if source.count != item.Literal.Size() {
				return fmt.Errorf("message UID %d ended after %d of %d bytes", uid, source.count, item.Literal.Size())
			}
		}
	}
	if !bodyFetched || date.IsZero() {
		return fmt.Errorf("server omitted the body or internal date for UID %d", uid)
	}
	if staged != nil {
		if _, err := staged.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind message UID %d: %w", uid, err)
		}
		if err := appendMboxMessage(writer, date, staged); err != nil {
			return fmt.Errorf("write message UID %d: %w", uid, err)
		}
	}
	return nil
}

type countedReader struct {
	io.Reader
	count int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += int64(n)
	return n, err
}
