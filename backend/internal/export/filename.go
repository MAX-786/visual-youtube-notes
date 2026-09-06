package export

import "strings"

// FileName is "<title>.<ext>" with the characters no filesystem keeps
// removed, so the browser saves the download under the Note's own name.
func FileName(title, ext string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f:
			return -1
		case strings.ContainsRune(`\/:*?"<>|`, r):
			return ' '
		}
		return r
	}, title)
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "note"
	}
	return name + "." + ext
}

// Attachment is a Content-Disposition value for name: an ASCII rendering in
// `filename` for every client and, when the name has more than ASCII, the
// UTF-8 original in `filename*` (RFC 6266, RFC 8187).
func Attachment(name string) string {
	ascii := asciiName(name)
	out := `attachment; filename="` + ascii + `"`
	if ascii != name {
		out += "; filename*=UTF-8''" + percentEncode(name)
	}
	return out
}

func asciiName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r > 0x7e || r < 0x20:
			continue
		case r == '"' || r == '\\':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	suffix := extension(name)
	if strings.TrimSpace(strings.TrimSuffix(out, suffix)) == "" {
		return "note" + suffix
	}
	return out
}

func extension(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// percentEncode keeps RFC 8187's attr-char set and encodes every other byte.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
