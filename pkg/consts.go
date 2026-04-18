package pkg

import (
	"strings"
	"unicode/utf8"
)

var SPECIAL_CHARS = map[rune]bool{
	'@': true,
	'+': true,
	'-': true,
	'*': true,
	'/': true,
}

func IsSpecialChar(char rune) bool {
	return SPECIAL_CHARS[char]
}

func GetLeftRightOfChar(idx int, str string) (string, string) {
	return GetCharsFromStart(idx, str), GetCharsUntilEnd(idx, str)
}

func GetCharsFromStart(idx int, str string) string {
	runes := []rune(str)
	var output strings.Builder

	for i := idx - 1; i >= 0; i-- {
		s := runes[i]
		output.WriteRune(s)

		if IsSpecialChar(s) {
			break
		}
	}

	return Reverse(strings.TrimSpace(output.String()))
}

func GetCharsUntilEnd(idx int, str string) string {
	var output strings.Builder

	for _, s := range str[idx+1:] {
		if IsSpecialChar(s) {
			break
		}

		output.WriteRune(s)
	}

	return strings.TrimSpace(output.String())
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
