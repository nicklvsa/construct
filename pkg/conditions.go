package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

func findTopLevelOp(s, op string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], op) {
			return i
		}
	}
	return -1
}

var conditionOps = []string{"==", "!=", ">=", "<=", ">", "<"}

func evaluateCondition(cond string) bool {
	return evaluateConditionWithBase(cond, "")
}

// matchingOuterParens reports whether the "(" opening s is closed by the final
// ")" — i.e. the whole condition is wrapped in one balanced pair.
func matchingOuterParens(s string) bool {
	depth, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
				if depth == 0 {
					return i == len(s)-1
				}
				if depth < 0 {
					return false
				}
			}
		}
	}
	return false
}

func evaluateConditionWithBase(cond, base string) bool {
	cond = strings.TrimSpace(cond)

	if strings.HasPrefix(cond, "(") && matchingOuterParens(cond) {
		return evaluateConditionWithBase(strings.TrimSpace(cond[1:len(cond)-1]), base)
	}

	if idx := findTopLevelOp(cond, "||"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) || evaluateConditionWithBase(cond[idx+2:], base)
	}
	if idx := findTopLevelOp(cond, "&&"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) && evaluateConditionWithBase(cond[idx+2:], base)
	}

	if strings.HasPrefix(cond, "!") {
		rest := strings.TrimSpace(cond[1:])
		return rest != "" && !evaluateConditionWithBase(rest, base)
	}

	if result, ok := evalBuiltinCondition(cond, base); ok {
		return result
	}

	if idx := findTopLevelOp(cond, " contains "); idx > 0 {
		left := strings.TrimSpace(cond[:idx])
		right := strings.TrimSpace(cond[idx+len(" contains "):])
		left = strings.Trim(left, "\"")
		right = strings.Trim(right, "\"")
		return strings.Contains(left, right)
	}

	if idx := findTopLevelOp(cond, " starts_with "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		right := strings.Trim(strings.TrimSpace(cond[idx+len(" starts_with "):]), "\"")
		return strings.HasPrefix(left, right)
	}

	if idx := findTopLevelOp(cond, " ends_with "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		right := strings.Trim(strings.TrimSpace(cond[idx+len(" ends_with "):]), "\"")
		return strings.HasSuffix(left, right)
	}

	if idx := findTopLevelOp(cond, " matches "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		pattern := strings.Trim(strings.TrimSpace(cond[idx+len(" matches "):]), "\"")
		matched, _ := regexp.MatchString(pattern, left)
		return matched
	}

	if idx := findTopLevelOp(cond, " in "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		list := strings.Trim(strings.TrimSpace(cond[idx+len(" in "):]), "\"")
		for item := range strings.SplitSeq(list, ",") {
			if left == strings.TrimSpace(item) {
				return true
			}
		}
		return false
	}

	ops := conditionOps
	for _, op := range ops {
		if idx := findTopLevelOp(cond, op); idx > 0 {
			left := strings.TrimSpace(cond[:idx])
			right := strings.TrimSpace(cond[idx+len(op):])
			left = strings.Trim(left, "\"")
			right = strings.Trim(right, "\"")
			return compareValues(left, right, op)
		}
	}
	return false
}

func evalBuiltinCondition(cond, base string) (bool, bool) {
	open := strings.IndexByte(cond, '(')
	if open <= 0 || !strings.HasSuffix(cond, ")") {
		return false, false
	}

	name := strings.TrimSpace(cond[:open])
	arg := strings.Trim(strings.TrimSpace(cond[open+1:len(cond)-1]), `"`)
	if arg == "" {
		return false, false
	}

	switch name {
	case "exists", "missing", "glob":
		if base != "" && !filepath.IsAbs(arg) {
			arg = filepath.Join(base, arg)
		}
	}

	switch name {
	case "exists":
		_, err := os.Stat(arg)
		return err == nil, true
	case "missing":
		_, err := os.Stat(arg)
		return err != nil, true
	case "glob":
		matches, _ := filepath.Glob(arg)
		return len(matches) > 0, true
	case "require":
		_, err := exec.LookPath(arg)
		return err == nil, true
	case "os":
		if arg == "macos" {
			arg = "darwin"
		}
		return arg == runtime.GOOS, true
	case "arch":
		return arg == runtime.GOARCH, true
	}
	return false, false
}

func compareValues(left, right, op string) bool {
	if li, err := strconv.Atoi(left); err == nil {
		if ri, err := strconv.Atoi(right); err == nil {
			return compare(li, ri, op)
		}
	}
	return compare(left, right, op)
}

func compare[T ~int | ~string](l, r T, op string) bool {
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case ">":
		return l > r
	case "<=":
		return l <= r
	case ">=":
		return l >= r
	}
	return false
}
