package pkg

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

func ParseCommandName(line string) string {
	line = strings.TrimSpace(line)

	// Cloud command markers: |commandname|
	if len(line) >= 2 && line[0] == '|' {
		if endIdx := strings.Index(line[1:], "|"); endIdx > 0 {
			return strings.TrimSpace(line[1 : endIdx+1])
		}
	}

	inIdx := strings.Index(line, " in ")
	prodIdx := findProducesIdx(line)
	ocIdx := findTopLevelKeyword(line, " onchange ")
	timeoutIdx := findTopLevelKeyword(line, " timeout<")
	contIdx := findTopLevelKeyword(line, " container ")
	endIdx := len(line)
	for _, c := range [3]byte{'(', '<', '{'} {
		if i := strings.IndexByte(line, c); i >= 0 && i < endIdx {
			endIdx = i
		}
	}
	if inIdx >= 0 && inIdx < endIdx {
		endIdx = inIdx
	}
	if prodIdx >= 0 && prodIdx < endIdx {
		endIdx = prodIdx
	}
	if ocIdx >= 0 && ocIdx < endIdx {
		endIdx = ocIdx
	}
	if timeoutIdx >= 0 && timeoutIdx < endIdx {
		endIdx = timeoutIdx
	}
	if contIdx >= 0 && contIdx < endIdx {
		endIdx = contIdx
	}
	return strings.TrimSpace(line[:endIdx])
}

// findTopLevelKeyword finds a keyword outside quotes and parens at depth zero.
func findTopLevelKeyword(line, kw string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		}
		if depth == 0 && !inQuote && strings.HasPrefix(line[i:], kw) {
			return i
		}
	}
	return -1
}

func findProducesIdx(line string) int {
	return findTopLevelKeyword(line, " produces ")
}

// ltIndex returns the first '<' that is not the modifier bracket of
// ` timeout<...>`; headers also use '<' for the prerequisite list.
func ltIndex(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		if i >= 7 && s[i-7:i] == "timeout" && (i == 7 || s[i-8] == ' ' || s[i-8] == '	') {
			continue
		}
		return i
	}
	return -1
}

func headerSegmentAfter(line, kw string) string {
	idx := findTopLevelKeyword(line, kw)
	if idx < 0 {
		return ""
	}
	segment := line[idx+len(kw):]
	if lt := ltIndex(segment); lt >= 0 {
		segment = segment[:lt]
	}
	if brace := strings.IndexByte(segment, '{'); brace >= 0 {
		segment = segment[:brace]
	}
	for _, other := range []string{" in ", " produces ", " onchange ", " timeout<", " container "} {
		if other == kw {
			continue
		}
		if cut := findTopLevelKeyword(segment, other); cut >= 0 {
			segment = segment[:cut]
		}
	}
	return segment
}

func splitListSegment(segment string) []string {
	var out []string
	for _, p := range strings.Split(segment, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func extractProduces(line string) []string {
	return splitListSegment(headerSegmentAfter(line, " produces "))
}

func extractOnChange(line string) []string {
	return splitListSegment(headerSegmentAfter(line, " onchange "))
}

func extractContainer(line string) string {
	return trimQuoted(strings.TrimSpace(headerSegmentAfter(line, " container ")))
}

func extractTimeout(line string) (string, error) {
	idx := findTopLevelKeyword(line, " timeout<")
	if idx < 0 {
		return "", nil
	}
	rest := strings.TrimSpace(line[idx+len(" timeout"):])
	_, mod, ok, err := peelModifier(rest)
	if err != nil {
		return "", fmt.Errorf("timeout modifier: %v", err)
	}
	if !ok {
		return "", nil
	}
	if _, derr := time.ParseDuration(mod); derr != nil {
		return "", fmt.Errorf("invalid timeout duration %q (expected e.g. 30s, 5m)", mod)
	}
	return mod, nil
}

func oldHeaderTimeout(line string) (string, bool) {
	ti := findTopLevelKeyword(line, " timeout ")
	if ti < 0 {
		return "", false
	}
	seg := strings.TrimSpace(line[ti+len(" timeout "):])
	if seg == "" || strings.HasPrefix(seg, "{") {
		return "", false
	}
	if sp := strings.IndexAny(seg, " 	<{"); sp > 0 {
		seg = seg[:sp]
	}
	return seg, seg != ""
}

func extractArgumentString(line string) string {
	start := strings.Index(line, "(")
	if start == -1 {
		return ""
	}

	end := strings.Index(line[start:], ")")
	if end == -1 {
		return ""
	}

	return strings.TrimSpace(line[start+1 : start+end])
}

func extractPrerequisites(line string) ([]string, map[string]string, error) {
	start := ltIndex(line)
	if start == -1 {
		return nil, nil, nil
	}

	end := strings.Index(line[start:], "{")
	if end == -1 {
		return nil, nil, nil
	}

	segment := line[start+1 : start+end]
	if oc := findTopLevelKeyword(segment, " onchange "); oc >= 0 {
		segment = segment[:oc]
	}

	dirs := make(map[string]string)
	var result []string
	for part := range strings.SplitSeq(segment, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "in" {
			continue
		}
		if strings.HasPrefix(part, "in ") {
			continue
		}
		dir := ""
		if inIdx := strings.Index(part, " in "); inIdx >= 0 {
			dir = strings.TrimSpace(part[inIdx+len(" in "):])
			part = strings.TrimSpace(part[:inIdx])
		}
		if part == "" {
			continue
		}
		result = append(result, part)
		if dir != "" {
			dirs[part] = dir
		}
	}

	if len(dirs) == 0 {
		dirs = nil
	}
	return result, dirs, nil
}

func extractWorkDir(line string) string {
	before, _, ok := strings.Cut(line, "{")
	if !ok {
		return ""
	}

	if lt := ltIndex(before); lt >= 0 {
		before = before[:lt]
	}

	idx := strings.LastIndex(before, " in ")
	if idx == -1 {
		return ""
	}

	dir := strings.TrimSpace(before[idx+len(" in "):])
	if prod := findTopLevelKeyword(dir, " produces "); prod >= 0 {
		dir = dir[:prod]
	}
	if oc := findTopLevelKeyword(dir, " onchange "); oc >= 0 {
		dir = dir[:oc]
	}
	if comma := strings.IndexByte(dir, ','); comma >= 0 {
		dir = strings.TrimSpace(dir[:comma])
	}
	return dir
}

func parseArgumentName(argStr string) (string, bool, string) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return "", false, ""
	}

	parts := strings.Fields(argStr)
	if len(parts) == 0 {
		return "", false, ""
	}

	argName := parts[len(parts)-1]
	isOptional := slices.Contains(parts[:len(parts)-1], "opt")

	defaultVal := ""
	if eq := strings.IndexByte(argName, '='); eq >= 0 {
		defaultVal = argName[eq+1:]
		argName = argName[:eq]
		isOptional = true
	}

	return argName, isOptional, defaultVal
}

func parseArgumentList(argStr string) ([]*Argument, error) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return nil, nil
	}

	args := []*Argument{}
	seen := make(map[string]bool)
	parts := strings.SplitSeq(argStr, ",")

	for part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		argName, isOptional, defaultVal := parseArgumentName(part)
		if argName == "" {
			return nil, fmt.Errorf("invalid argument syntax: '%s'", part)
		}
		if seen[argName] {
			return nil, fmt.Errorf("duplicate argument '%s'", argName)
		}
		seen[argName] = true

		args = append(args, &Argument{
			Name:       argName,
			IsOptional: isOptional,
			Default:    defaultVal,
		})
	}

	return args, nil
}
