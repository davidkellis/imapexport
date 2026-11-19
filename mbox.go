package main

import (
	"bufio"
	"fmt"
	"io"
	"time"
)

// appendMboxMessage writes one mboxrd record with Unix line endings.
func appendMboxMessage(w *bufio.Writer, date time.Time, message io.Reader) error {
	// IMAP does not provide the SMTP envelope sender.
	if _, err := fmt.Fprintf(w, "From MAILER-DAEMON %s\n", date.UTC().Format(time.ANSIC)); err != nil {
		return err
	}
	r := bufio.NewReader(message)
	for {
		first, err := r.Peek(1)
		if err == io.EOF {
			return w.WriteByte('\n')
		}
		if err != nil {
			return err
		}

		// Lines matching ^>*From  get one additional '>'. Stream any existing
		// prefix so that even a very long line only needs a small buffer.
		for len(first) != 0 && first[0] == '>' {
			r.Discard(1)
			if err := w.WriteByte('>'); err != nil {
				return err
			}
			first, err = r.Peek(1)
			if err != nil && err != io.EOF {
				return err
			}
		}
		prefix, err := r.Peek(len("From "))
		if err != nil && err != io.EOF {
			return err
		}
		if string(prefix) == "From " {
			if err := w.WriteByte('>'); err != nil {
				return err
			}
		}

		for {
			line, continued, err := r.ReadLine()
			if err != nil && err != io.EOF {
				return err
			}
			if _, err := w.Write(line); err != nil {
				return err
			}
			if !continued {
				if err := w.WriteByte('\n'); err != nil {
					return err
				}
				break
			}
		}
	}
}
