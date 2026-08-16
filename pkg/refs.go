package pkg

// Reference substitution: &name variables, @NAME environment refs, and the
// helpers that scan, escape, and manipulate them. Shared by the parser,
// executor, and linter.

import (
	"os"
	"strings"
	"unicode"
)

func isVarIdentByte(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isVarIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

func isPlainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func envLookupValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):], true
		}
	}
	return "", false
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue // replaced below
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

func escapeShellValue(s string) string {
	if !strings.ContainsAny(s, "`\"\\$") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`', '"', '\\', '$':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// scanRefs walks s and substitutes marker-prefixed references (e.g. &name or
// @NAME, with optional dotted segments) through lookup. A backslash before
// the marker escapes it.
func scanRefs(s string, marker byte, firstSeg, dotSeg func(rune) bool, lookup func(string) (string, bool), fallbackFirst bool) string {
	var result strings.Builder
	result.Grow(len(s) + 16)
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == rune(marker) {
			result.WriteRune(runes[i+1])
			i += 2
			continue
		}
		if runes[i] == rune(marker) {
			j := i + 1
			firstStart := j
			for j < len(runes) && firstSeg(runes[j]) {
				j++
			}
			if j > firstStart {
				firstEnd := j // first segment ends here, before any dots
				if dotSeg != nil {
					for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && dotSeg(runes[j+1]) {
						j++
						for j < len(runes) && dotSeg(runes[j]) {
							j++
						}
					}
				}

				if marker == '@' && j+1 < len(runes) && runes[j] == ':' && runes[j+1] == '-' {
					j += 2
					for j < len(runes) && !isEnvDefaultEnd(runes[j]) {
						j++
					}
				}
				if val, ok := lookup(string(runes[firstStart:j])); ok {
					result.WriteString(val)
					i = j
					continue
				}
				if fallbackFirst && j > firstEnd {
					if val, ok := lookup(string(runes[firstStart:firstEnd])); ok {
						result.WriteString(val)
						i = firstEnd
						continue
					}
				}
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(line, '&') < 0 {
		return line
	}
	return scanRefs(line, '&', isVarIdentRune, isPlainRune, lookup, true)
}

func VarRefNames(s string) []string {
	if strings.IndexByte(s, '&') < 0 {
		return nil
	}
	var names []string
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '&' {
			continue
		}
		j := i + 1
		for j < len(runes) && isVarIdentRune(runes[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && isPlainRune(runes[j+1]) {
			j++
			for j < len(runes) && isPlainRune(runes[j]) {
				j++
			}
		}
		names = append(names, string(runes[i+1:j]))
		i = j - 1
	}
	return names
}

func wildcardRefNames(s string) []string {
	if strings.IndexByte(s, '&') < 0 {
		return nil
	}
	var names []string
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '&' {
			continue
		}
		j := i + 1
		for j < len(runes) && isVarIdentRune(runes[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && isPlainRune(runes[j+1]) {
			j++
			for j < len(runes) && isPlainRune(runes[j]) {
				j++
			}
		}
		if j+1 < len(runes) && runes[j] == '.' && runes[j+1] == '*' {
			names = append(names, string(runes[i+1:j]))
			i = j + 1
		}
	}
	return names
}

func isEnvDefaultEnd(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n', '"', '\'', ',', ';', '&', '@', '$':
		return true
	}
	return false
}

func splitEnvRefToken(token string) (name, def string, hasDefault bool) {
	if before, after, ok := strings.Cut(token, ":-"); ok {
		return before, after, true
	}
	return token, "", false
}

func ResolveEnvRefs(s string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val, ok := os.LookupEnv(name); ok {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", true
	}, false)
}

func resolveEnvRefsWith(s string, lookup func(string) string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val := lookup(name); val != "" {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", true
	}, false)
}

func resolveEnvRefsKeepUnset(s string) string {
	return resolveEnvRefsKeepUnsetWith(s, os.LookupEnv)
}

func resolveEnvRefsKeepUnsetWith(s string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val, ok := lookup(name); ok {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", false
	}, false)
}

func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return nil
}
