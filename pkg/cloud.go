package pkg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type CloudEntry struct {
	Name      string
	BodyStmts int
	Local     bool
}

func (e *Executor) resolveCloudFile() string {
	if v := os.Getenv("CONSTRUCT_CLOUD_FILE"); v != "" {
		return v
	}
	if e.baseDir != "" {
		return filepath.Join(e.baseDir, "construct-cloud.json")
	}
	return "construct-cloud.json"
}

func LoadCloudDefsFile(path string) (map[string]Command, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Command{}, nil
		}
		return nil, err
	}
	var defs map[string]Command
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("failed to parse cloud file %q: %w", path, err)
	}
	if defs == nil {
		defs = map[string]Command{}
	}
	return defs, nil
}

func cloudStmtCount(c Command) int {
	n := len(c.Body)
	for _, s := range c.Body {
		n += len(s.ThenBody) + len(s.ElseBody) + len(s.LoopBody) + len(s.OnFailBody)
	}
	return n
}

func (e *Executor) CloudList() ([]CloudEntry, error) {
	path := e.resolveCloudFile()
	defs, err := LoadCloudDefsFile(path)
	if err != nil {
		return nil, err
	}
	var out []CloudEntry
	for name, c := range defs {
		out = append(out, CloudEntry{Name: name, BodyStmts: cloudStmtCount(c), Local: false})
	}
	slices.SortFunc(out, func(a, b CloudEntry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (e *Executor) CloudPull(names []string, outFile string) (int, error) {
	path := e.resolveCloudFile()
	defs, err := LoadCloudDefsFile(path)
	if err != nil {
		return 0, err
	}
	selected := make(map[string]Command)
	if len(names) == 0 {
		selected = defs
	} else {
		for _, n := range names {
			c, ok := defs[n]
			if !ok {
				return 0, fmt.Errorf("cloud command %q not found", n)
			}
			selected[n] = c
		}
	}
	if outFile == "" {
		if e.baseDir != "" {
			outFile = filepath.Join(e.baseDir, "construct-cloud.json")
		} else {
			outFile = "construct-cloud.json"
		}
	}
	data, err := json.MarshalIndent(selected, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(outFile, data, 0644); err != nil {
		return 0, err
	}
	return len(selected), nil
}

func (e *Executor) CloudPush(names []string, file string) (int, error) {
	var cmds []*Command
	if len(names) > 0 {
		for _, n := range names {
			c, err := e.StructuredParse.GetCommand(n)
			if err != nil {
				return 0, err
			}
			cmds = append(cmds, c)
		}
	} else {
		for _, c := range e.StructuredParse.Commands {
			if c.CloudAccessible {
				cmds = append(cmds, c)
			}
		}
		if len(cmds) == 0 {
			return 0, fmt.Errorf("no cloud-accessible commands (mark one with |name|)")
		}
	}
	if file == "" {
		file = e.resolveCloudFile()
	}
	defs, err := LoadCloudDefsFile(file)
	if err != nil {
		return 0, err
	}
	for _, c := range cmds {
		defs[c.Name] = *c
	}
	data, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		return 0, err
	}
	return len(cmds), nil
}
