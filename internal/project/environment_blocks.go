package project

import (
	"strconv"
	"strings"
)

type environmentEntry struct {
	key, value, text string
}

// Keep quoted multiline assignments together so continuation lines cannot
// become settings, survive a deleted credential, or be truncated by Restore.
func environmentEntries(data []byte) []environmentEntry {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	var entries []environmentEntry
	for i := 0; i < len(lines); i++ {
		entry := environmentEntry{text: lines[i]}
		trimmed := strings.TrimSpace(entry.text)
		key, raw, found := strings.Cut(trimmed, "=")
		if !found || strings.HasPrefix(trimmed, "#") {
			entries = append(entries, entry)
			continue
		}
		entry.key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		// Preserve whitespace within an unfinished quoted value.
		_, raw, _ = strings.Cut(entry.text, "=")
		raw = strings.TrimLeft(raw, " \t")
		if len(raw) > 0 && (raw[0] == '\'' || raw[0] == '"') {
			quote := raw[0]
			end := environmentQuoteEnd(raw, quote)
			for end < 0 && i+1 < len(lines) {
				i++
				entry.text += "\n" + lines[i]
				raw += "\n" + lines[i]
				end = environmentQuoteEnd(raw, quote)
			}
			entry.value = raw
			if end >= 0 {
				entry.value = raw[1:end]
				if quote == '\'' {
					entry.value = strings.ReplaceAll(entry.value, "\\'", "'")
				} else if decoded, err := strconv.Unquote(strings.ReplaceAll(raw[:end+1], "\n", "\\n")); err == nil {
					entry.value = decoded
				}
			}
		} else {
			entry.value = strings.TrimSpace(raw)
			if prefix, _, ok := strings.Cut(entry.value, " #"); ok {
				entry.value = strings.TrimSpace(prefix)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func environmentQuoteEnd(value string, quote byte) int {
	for i := 1; i < len(value); i++ {
		if value[i] == '\\' {
			i++
			continue
		}
		if value[i] == quote {
			return i
		}
	}
	return -1
}
