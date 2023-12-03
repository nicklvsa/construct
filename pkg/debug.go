package pkg

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
)

func HashKey(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func DebugToMermaid(data *ParsedData) string {
	contents := "```mermaid\nflowchart TD\n\t"

	for _, cmd := range data.Commands {
		if len(cmd.Prereqs) > 1 {
			for _, prereq := range cmd.Prereqs {
				prereq = strings.TrimSpace(prereq)
				contents += fmt.Sprintf("%s[%s]-->%s[%s]\n\t", HashKey(prereq), prereq, HashKey(cmd.Name), cmd.Name)
			}

			continue
		}

		contents += fmt.Sprintf("%s[%s]\n\t", HashKey(cmd.Name), cmd.Name)
	}

	return contents + "\n```"
}
