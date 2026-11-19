package main

import (
	"fmt"
	"path"
	"strings"
)

func mailboxPaths(mailboxes []mailbox) []string {
	type folderKey struct{ delimiter, prefix string }
	directories := make(map[folderKey]string)
	used := make(map[string]bool)
	paths := make([]string, len(mailboxes))
	for i, box := range mailboxes {
		parts := []string{box.Name}
		if box.Delimiter != "" {
			parts = strings.Split(box.Name, box.Delimiter)
		}
		directory := ""
		for n, part := range parts {
			prefix := strings.Join(parts[:n+1], box.Delimiter)
			if n == 0 {
				prefix = mailboxKey(prefix)
				part = mailboxKey(part)
			}
			key := folderKey{box.Delimiter, prefix}
			if directories[key] == "" {
				base := path.Join(directory, portableComponent(part))
				candidate := base
				for suffix := 2; used[strings.ToLower(candidate)]; suffix++ {
					candidate = fmt.Sprintf("%s~%d", base, suffix)
				}
				used[strings.ToLower(candidate)] = true
				directories[key] = candidate
			}
			directory = directories[key]
		}
		paths[i] = directory
	}
	return paths
}

func portableComponent(name string) string {
	if name == "" {
		return "%00"
	}
	upper := strings.ToUpper(name)
	reserved := upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
		(len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) &&
			upper[3] >= '0' && upper[3] <= '9')

	var label strings.Builder
	for i := 0; i < len(name) && label.Len() < 200; i++ {
		b := name[i]
		// Escape dots so a directory can never collide with a .mbox file.
		if !(i == 0 && reserved) && (b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
			b >= '0' && b <= '9' || b == '-' || b == '_' || (b == ' ' && i > 0 && i < len(name)-1)) {
			label.WriteByte(b)
		} else {
			fmt.Fprintf(&label, "%%%02X", b)
		}
	}
	// Truncation can expose an internal space at the end of the component.
	encoded := label.String()
	if base, ok := strings.CutSuffix(encoded, " "); ok {
		return base + "%20"
	}
	return encoded
}
