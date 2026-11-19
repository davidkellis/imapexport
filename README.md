# imapexport

`imapexport` saves an IMAP account's mail as one mbox file per mailbox, packaged in a single ZIP. Extract the ZIP and open the individual mailboxes with tools that read mbox.

## Build and run

Install Go 1.26 or later, then build from this directory.

```sh
go build -o imapexport .
```

Use the incoming IMAP server hostname and username from your mail provider's settings. Replace these example values with yours.

```sh
./imapexport list --host imap.example.com --user you@example.com
./imapexport export --host imap.example.com --user you@example.com --output mail.zip
unzip -t mail.zip
unzip mail.zip -d exported-mail
```

The commands prompt for the password without echoing it. Set `IMAP_PASSWORD` in the environment to run unattended. Use an app password if your provider requires one and supports password-based IMAP access. The tool supports IMAP `LOGIN` and `AUTHENTICATE PLAIN` over verified TLS.

Implicit TLS on port 993 is the default. Use `--starttls` for a server configured for STARTTLS on port 143. `--port` overrides either default.

### Example export

```console
$ ./imapexport export --host imap.example.com --port 993 --user you@example.com --output mail.zip
IMAP password:
Exporting "Archive" (0 messages)
Exporting "Drafts" (2 messages)
Exporting "INBOX" (240 messages)
Exporting "INBOX.spam" (0 messages)
Exporting "Junk" (3 messages)
Exporting "Sent" (37 messages)
Exporting "Trash" (12 messages)
Saved mail.zip (294 messages, 7 mailboxes)
```

### Select specific mailboxes

Export includes every mailbox the server lists for that login, including Junk, Trash, and nested folders. To export particular mailboxes, use the exact names reported by `list` and repeat `--mailbox` as needed. Selecting a mailbox by name includes its own messages. Select any desired subfolders by name as well.

```sh
./imapexport export --host imap.example.com --user you@example.com \
  --mailbox INBOX --mailbox 'INBOX/spam' --output inbox-and-spam.zip
```

The hierarchy delimiter depends on the server. For example, the same subfolder could appear as `INBOX/spam` or `INBOX.spam`.

## Archive format

A mailbox and its subfolders can coexist as paths such as `INBOX.mbox` and `INBOX/spam.mbox`. Empty mailboxes have empty mbox files. Folder names use percent-encoding where needed for portable filenames, with a numeric suffix for case-insensitive collisions. Very long names may be shortened.

The exporter writes the mboxrd variant. It uses LF line endings, quotes lines that could be mistaken for message boundaries, and keeps the message headers and MIME attachments. The separator carries the IMAP internal date in UTC and uses `MAILER-DAEMON` as the envelope sender placeholder. The archive contains only mbox files and folder directories. Server-side UIDs, flags such as read status, and custom keywords are omitted because mbox has no portable representation for them. [Mbox media type](https://www.rfc-editor.org/rfc/rfc4155.html) · [mboxrd format](https://sources.debian.org/src/qmail/1.01-5/mbox.5/#L297)

In Apple Mail, choose **File → Import Mailboxes → Files in mbox format** and select the extracted mbox files. [Apple Mail import guide](https://support.apple.com/en-ie/guide/mail/mlhlp1030/mac)

The exporter opens each mailbox read-only and fetches with `BODY.PEEK[]` to preserve unread status. It takes a list of message UIDs when it reaches each mailbox. Mail arriving after that lookup belongs to a later export. If any requested message disappears before it is fetched, the export fails so the ZIP cannot silently omit it. Mailboxes are captured one at a time, so a message moved between folders during export can affect what appears in the archive. [IMAP specification](https://www.rfc-editor.org/rfc/rfc9051.html)

The destination must be a new file in an existing directory on a filesystem that supports hard links. The tool writes a temporary file in that directory and publishes the archive after the ZIP is complete. The CLI provides full mail exports. Restore, incremental backups, and OAuth sign-in are outside this version. Contacts, calendars, and webmail settings are outside the IMAP mail export.
