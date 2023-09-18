package pkg

import (
	"strings"
	"unicode/utf8"
)

var SPECIAL_CHARS = []rune{
	'&', '@', '+', '-', '*', '/',
}

func IsSpecialChar(char rune) bool {
	for _, special := range SPECIAL_CHARS {
		if special == char {
			return true
		}
	}

	return false
}

func GetLeftRightOfChar(idx int, str string) (string, string) {
	return GetCharsFromStart(idx, str), GetCharsUntilEnd(idx, str)
}

func GetCharsFromStart(idx int, str string) string {
	var output string

	for i := idx - 1; i >= 0; i-- {
		s := rune(str[i])
		output += string(s)

		if IsSpecialChar(s) {
			break
		}
	}

	return Reverse(strings.TrimSpace(output))
}

func GetCharsUntilEnd(idx int, str string) string {
	var output string

	for _, s := range str[idx+1:] {
		if IsSpecialChar(s) {
			break
		}

		output += string(s)
	}

	return strings.TrimSpace(output)
}

func Reverse(s string) string {
	size := len(s)
	buf := make([]byte, size)
	for start := 0; start < size; {
		r, n := utf8.DecodeRuneInString(s[start:])
		start += n
		utf8.EncodeRune(buf[size-start:], r)
	}
	return string(buf)
}
