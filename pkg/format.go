package pkg

import "strings"

func NetBraces(line string) int {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "!") {
		return 0
	}
	inStr := false
	depth := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inStr = !inStr
		case '{':
			if !inStr {
				depth++
			}
		case '}':
			if !inStr {
				depth--
			}
		}
	}
	return depth
}

func FormatConstfile(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			out = append(out, "")
			continue
		}
		d := NetBraces(trimmed)
		indent := depth
		if d < 0 || strings.HasPrefix(strings.TrimSpace(trimmed), "}") {
			// A leading `}` closes one level, including on `} else {`.
			indent = depth - 1
		}
		if indent < 0 {
			indent = 0
		}
		out = append(out, strings.Repeat("    ", indent)+strings.TrimLeft(trimmed, " \t"))
		depth += d
		if depth < 0 {
			depth = 0
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}
