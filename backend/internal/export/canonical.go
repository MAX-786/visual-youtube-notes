package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf16"
)

var integerSyntax = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

const maxSafeInteger = 1<<53 - 1

// canonical appends v in RFC 8785 (JCS) form, restricted to what the archive
// admits: objects, arrays, strings, booleans, null and integers. A float is
// refused rather than formatted, because RFC 8785's number rule would then
// be load-bearing for every checksum; verify-archive.mjs refuses the same
// way, so the writer and the checker agree on what cannot exist (ADR 0011).
func canonical(buf *bytes.Buffer, v any, path string) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSONString(buf, x)
	case int:
		buf.WriteString(strconv.Itoa(x))
	case int32:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		if x > maxSafeInteger || x < -maxSafeInteger {
			return fmt.Errorf("%s: %d exceeds the safe integer range", path, x)
		}
		buf.WriteString(strconv.FormatInt(x, 10))
	case json.Number:
		s := x.String()
		if !integerSyntax.MatchString(s) {
			return fmt.Errorf("%s: %s is not an integer; the archive admits no floating-point number", path, s)
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > maxSafeInteger || n < -maxSafeInteger {
			return fmt.Errorf("%s: %s exceeds the safe integer range", path, s)
		}
		buf.WriteString(s)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || x != math.Trunc(x) || math.Abs(x) > maxSafeInteger {
			return fmt.Errorf("%s: %v is not an integer; the archive admits no floating-point number", path, x)
		}
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case []string:
		items := make([]any, len(x))
		for i, s := range x {
			items[i] = s
		}
		return canonical(buf, items, path)
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := canonical(buf, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := canonical(buf, x[k], path+"."+k); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("%s: %T has no canonical form", path, v)
	}
	return nil
}

// lessUTF16 orders keys by UTF-16 code unit, as RFC 8785 asks and as
// JavaScript's default sort compares.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeJSONString serializes s the way ECMAScript's JSON.stringify does,
// which RFC 8785 section 3.2.2.2 defers to: only the quote, the backslash
// and control characters are escaped; everything else, U+2028 and U+2029
// included, travels literally.
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}
