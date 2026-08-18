package main

import (
	"fmt"
	"path/filepath"

	"github.com/nicklvsa/construct/pkg"
)

func applySince(inputs *ConstructInput, o *options, data *pkg.ParsedData) (bool, error) {
	baseDir := filepath.Dir(inputs.FileName)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return false, err
	}
	if resolved, rerr := filepath.EvalSymlinks(absBase); rerr == nil {
		absBase = resolved
	}
	changed, err := pkg.GitChangedFiles(baseDir, o.since)
	if err != nil {
		return false, err
	}
	affected := pkg.AffectedCommands(data, changed, absBase)

	targets := inputs.Commands
	if len(targets) == 0 {
		def, derr := data.GetDefaultCommand()
		if derr != nil || def == nil {
			return true, nil // no targets and no default: let Execute produce its error
		}
		targets = []string{def.Name}
		inputs.Commands = targets
	}

	var run []string
	for _, t := range targets {
		if affected[t] {
			run = append(run, t)
			continue
		}
		if _, err := data.GetCommand(t); err != nil {
			run = append(run, t)
			continue
		}
		fmt.Printf("(%s not affected since %s — skipping)\n", t, o.since)
	}
	if len(run) == 0 {
		fmt.Printf("(nothing affected since %s)\n", o.since)
		return false, nil
	}
	inputs.Commands = run
	return true, nil
}
