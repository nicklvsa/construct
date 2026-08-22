package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nicklvsa/construct/pkg"
)

//go:embed init-templates/*
var initTemplates embed.FS

// rejectSubcommandFlags errors when a subcommand's positional args contain
// flags; subcommands only accept global flags before their name.
func rejectSubcommandFlags(args []string, label string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return exitAt(2, "unknown %s option %q", label, a)
		}
	}
	return nil
}

// splitConstfileArgs peels a leading Constfile path off a subcommand's args.
func splitConstfileArgs(args []string) (fileName string, rest []string) {
	if len(args) > 0 && fileExists(args[0]) {
		return args[0], args[1:]
	}
	return defaultConstfileName(), args
}

func runInit(args []string, o *options) error {
	template := o.template
	fileName := o.fileName
	force := o.force
	if template == "" {
		template = "minimal"
	}
	if fileName == "" {
		fileName = "Constfile"
	}
	if err := rejectSubcommandFlags(args, "init"); err != nil {
		return err
	}
	switch len(args) {
	case 0:
	case 1:
		template = args[0]
	default:
		return exitAt(2, "usage: construct init [template]")
	}
	if _, err := os.Stat(fileName); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", fileName)
	}
	content, err := initTemplates.ReadFile("init-templates/" + template + ".constfile")
	if err != nil {
		return fmt.Errorf("unknown template %q (available: minimal, go, python, node, rust, monorepo)", template)
	}
	if err := os.WriteFile(fileName, content, 0644); err != nil {
		return err
	}
	fmt.Printf("created %s from the %q template\n", fileName, template)
	fmt.Println("next: construct --list")
	return nil
}

func runLint(args []string, o *options) error {
	fileName := defaultConstfileName()
	if len(args) > 0 && fileExists(args[0]) {
		fileName = args[0]
	}

	p, err := pkg.NewParser(fileName)
	if err != nil {
		return err
	}
	data, err := p.Parse()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(fileName)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", fileName, err)
	}
	issues := pkg.Lint(strings.Split(string(content), "\n"), data, filepath.Dir(fileName))

	if o.json {
		out, err := json.MarshalIndent(issues, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	errs, warns := 0, 0
	for _, is := range issues {
		fmt.Println(pkg.FormatLintIssue(fileName, is))
		switch is.Severity {
		case pkg.LintError:
			errs++
		case pkg.LintWarning:
			warns++
		}
	}
	summary := fmt.Sprintf("%d issue(s): %d error(s), %d warning(s)", len(issues), errs, warns)
	if len(issues) == 0 {
		fmt.Println("no issues found")
		return nil
	}
	if errs > 0 || (o.strict && warns > 0) {
		return exitAt(1, "%s", summary)
	}
	fmt.Println(summary)
	return nil
}

func runImport(args []string, o *options) error {
	input := "Makefile"
	output := "Constfile"
	if err := rejectSubcommandFlags(args, "import"); err != nil {
		return err
	}

	// `construct import update [specs...]` refreshes remote (git) imports;
	// a Makefile literally named "update" still converts via an explicit path.
	if len(args) > 0 && args[0] == "update" && !fileExists("update") {
		baseDir := "."
		if fileName := defaultConstfileName(); fileExists(fileName) {
			baseDir = filepath.Dir(fileName)
		}
		if _, err := pkg.UpdateGitImports(baseDir, args[1:]); err != nil {
			return exitAt(1, "%v", err)
		}
		return nil
	}

	if len(args) > 0 {
		input = args[0]
	}
	if len(args) > 1 {
		output = args[1]
	}
	if len(args) > 2 {
		return exitAt(2, "usage: construct import [Makefile] [output] | construct import update [specs...]")
	}

	content, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", input, err)
	}
	res, err := pkg.ImportMakefile(string(content))
	if err != nil {
		return fmt.Errorf("%s: %w", input, err)
	}
	if _, err := os.Stat(output); err == nil && !o.force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", output)
	}
	if err := os.WriteFile(output, []byte(res.Constfile), 0644); err != nil {
		return err
	}

	fmt.Printf("imported %s -> %s: %d command(s), %d variable(s)", input, output, res.Commands, res.Variables)
	if res.Flagged > 0 {
		fmt.Printf(", %d line(s) flagged for review (search \"construct-import\")", res.Flagged)
	}
	fmt.Println()
	fmt.Println("next: construct lint && construct --list")
	return nil
}

func runShellCmd(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "shell"); err != nil {
		return err
	}
	fileName, rest := splitConstfileArgs(args)
	if len(rest) > 1 {
		return exitAt(2, "usage: construct shell [Constfile] [command]")
	}
	target := ""
	if len(rest) == 1 {
		target = rest[0]
	}

	data, err := parseConstfileOptional(fileName)
	if err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("no Constfile found (looked for %s)", fileName)
	}

	executor := pkg.NewExecutor(data, false, o.debug)
	executor.SetBaseDir(filepath.Dir(fileName))
	if o.envFile != "" {
		if err := pkg.LoadEnvFile(o.envFile); err != nil {
			return fmt.Errorf("failed to load env file %s: %w", o.envFile, err)
		}
	}

	code, err := executor.InteractiveShell(target, o.containerOverride)
	if err != nil {
		return exitAt(2, "%v", err)
	}
	if code != 0 {
		return exitAt(code, "shell exited with code %d", code)
	}
	return nil
}

func runClean(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "clean"); err != nil {
		return err
	}
	fileName, targets := splitConstfileArgs(args)
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return err
	}
	data, err := p.Parse()
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(fileName)

	removed := 0
	found := 0
	for _, cmd := range data.Commands {
		if pkg.IsLazyName(cmd.Name) {
			continue
		}
		if len(targets) > 0 && !slices.Contains(targets, cmd.Name) {
			continue
		}
		wd := baseDir
		if cmd.WorkDir != "" && !filepath.IsAbs(cmd.WorkDir) {
			wd = filepath.Join(baseDir, cmd.WorkDir)
		}
		for _, pattern := range cmd.Produces {
			for _, path := range expandGlobs(pattern, wd) {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				found++
				if info.IsDir() {
					fmt.Printf("skipped %s (directory)\n", path)
					continue
				}
				abs, err := filepath.Abs(path)
				if err != nil || !withinDir(abs, baseDir) {
					fmt.Printf("skipped %s (outside the Constfile directory)\n", path)
					continue
				}
				if o.dryRun {
					fmt.Printf("would remove %s\n", path)
					continue
				}
				if err := os.Remove(path); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", path, err)
					continue
				}
				fmt.Printf("removed %s\n", path)
				removed++
			}
		}
	}

	if o.cleanCache {
		cacheDir := filepath.Join(baseDir, pkg.CacheDirName())
		if fileExists(cacheDir) {
			if o.dryRun {
				fmt.Printf("would remove %s\n", cacheDir)
			} else {
				if err := os.RemoveAll(cacheDir); err != nil {
					return err
				}
				fmt.Printf("removed %s\n", cacheDir)
			}
		}
	}
	if found == 0 && !o.cleanCache {
		fmt.Println("nothing to clean (no produced files found)")
	}
	return nil
}

func expandGlobs(pattern, dir string) []string {
	full := filepath.Join(dir, pattern)
	matches, err := filepath.Glob(full)
	if err != nil || len(matches) == 0 {
		return []string{full}
	}
	return matches
}

func withinDir(path, dir string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return strings.HasPrefix(path, absDir+string(os.PathSeparator))
}

func runGraph(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "graph"); err != nil {
		return err
	}
	fileName, targets := splitConstfileArgs(args)
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return err
	}
	data, err := p.Parse()
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		targets = graphRoots(data)
	}
	if o.json {
		return graphJSON(data, targets)
	}
	if o.dotGraph {
		return graphDot(data, targets)
	}
	graphASCII(data, targets)
	return nil
}

func graphRoots(data *pkg.ParsedData) []string {
	referenced := map[string]bool{}
	var names []string
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		names = append(names, cmd.Name)
		for _, pre := range cmd.Prereqs {
			referenced[pre] = true
		}
	}
	var roots []string
	for _, n := range names {
		if !referenced[n] {
			roots = append(roots, n)
		}
	}
	if len(roots) == 0 {
		return names
	}
	return roots
}

func graphASCII(data *pkg.ParsedData, targets []string) {
	for _, t := range targets {
		fmt.Println(t)
		printGraphChildren(data, t, "", map[string]bool{t: true})
	}
}

func printGraphChildren(data *pkg.ParsedData, name, prefix string, path map[string]bool) {
	cmd, err := data.GetCommand(name)
	if err != nil || cmd == nil {
		return
	}
	kids := graphChildren(cmd)
	for i, kid := range kids {
		last := i == len(kids)-1
		branch, cont := "├── ", "│   "
		if last {
			branch, cont = "└── ", "    "
		}
		fmt.Printf("%s%s%s\n", prefix, branch, kid.label)
		if !kid.isFile && !path[kid.name] {
			printGraphChildren(data, kid.name, prefix+cont, union(path, kid.name))
		} else if !kid.isFile && path[kid.name] {
			fmt.Printf("%s└── …\n", prefix+cont)
		}
	}
}

func union(m map[string]bool, k string) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	maps.Copy(out, m)
	out[k] = true
	return out
}

type graphKid struct {
	name   string
	label  string
	isFile bool
}

func graphChildren(cmd *pkg.Command) []graphKid {
	var kids []graphKid
	for _, pre := range cmd.Prereqs {
		kids = append(kids, graphKid{name: pre, label: pre})
	}
	for _, dep := range cmd.FileDeps {
		kids = append(kids, graphKid{label: dep + " (file)", isFile: true})
	}
	return kids
}

func graphDot(data *pkg.ParsedData, targets []string) error {
	fmt.Println("digraph construct {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [fontname=\"Helvetica\"];")

	nodes := map[string]bool{}
	edges := map[string]bool{}
	fileNode := func(label string) {
		if !nodes[label] {
			nodes[label] = true
			fmt.Printf("  \"%s\" [shape=ellipse, style=filled, fillcolor=lightgrey];\n", label)
		}
	}

	var walk func(name string, path map[string]bool)
	walk = func(name string, path map[string]bool) {
		cmd, err := data.GetCommand(name)
		if err != nil || cmd == nil || path[name] {
			return
		}
		for _, kid := range graphChildren(cmd) {
			to := kid.label
			if !kid.isFile {
				to = kid.name
			} else {
				fileNode(kid.label)
			}
			if !edges[name+"->"+to] {
				edges[name+"->"+to] = true
				fmt.Printf("  \"%s\" -> \"%s\";\n", name, to)
			}
			if !kid.isFile {
				walk(kid.name, union(path, name))
			}
		}
	}

	for _, t := range targets {
		walk(t, map[string]bool{})
	}

	fmt.Println("}")
	return nil
}

func graphJSON(data *pkg.ParsedData, targets []string) error {
	type node struct {
		Name     string   `json:"name"`
		Prereqs  []string `json:"prereqs,omitempty"`
		FileDeps []string `json:"file_deps,omitempty"`
	}

	seen := map[string]bool{}
	var out []node
	var walk func(name string, path map[string]bool)

	walk = func(name string, path map[string]bool) {
		if seen[name] || path[name] {
			return
		}

		seen[name] = true
		cmd, err := data.GetCommand(name)
		if err != nil || cmd == nil {
			return
		}

		out = append(out, node{Name: name, Prereqs: cmd.Prereqs, FileDeps: cmd.FileDeps})
		path = union(path, name)

		for _, pre := range cmd.Prereqs {
			walk(pre, path)
		}
	}

	for _, t := range targets {
		walk(t, map[string]bool{})
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}

	fmt.Println(string(b))
	return nil
}

func runTargets() {
	fileName := defaultConstfileName()
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return
	}

	data, err := p.Parse()
	if err != nil {
		return
	}

	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}

		fmt.Println(cmd.Name)
	}
}

func runCompletion(args []string) error {
	if len(args) == 0 {
		return exitAt(2, "usage: construct completion <bash|zsh|fish>")
	}

	var script string
	switch args[0] {
	case "bash":
		script = completionBash()
	case "zsh":
		script = completionZsh()
	case "fish":
		script = completionFish()
	default:
		return exitAt(2, "unknown shell %q (bash, zsh, fish)", args[0])
	}

	fmt.Print(script)
	fmt.Fprintf(os.Stderr, "# install: source the script from your shell profile\n")
	return nil
}

func completionBash() string {
	return fmt.Sprintf(`_construct() {
    local cur commands flags
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    commands="$(construct __targets 2>/dev/null)"
    flags="%s"
    if [[ "$cur" == -* ]]; then
        COMPREPLY=( $(compgen -W "$flags" -- "$cur") )
    else
        COMPREPLY=( $(compgen -W "$commands $flags" -- "$cur") )
    fi
}
complete -F _construct construct
`, flagWords())
}

func completionZsh() string {
	return fmt.Sprintf(`#compdef construct
_construct() {
    local -a commands flags
    commands=(${(f)"$(construct __targets 2>/dev/null)"})
    flags=(%s)
    _arguments "${flags[@]}" '1:command:->cmds' '*:target:->cmds'
    case $state in
        cmds) _describe 'command' commands ;;
    esac
}
_construct "$@"
`, zshFlagArgs())
}

func completionFish() string {
	var b strings.Builder
	b.WriteString("complete -c construct -f\n")
	b.WriteString("complete -c construct -a '(construct __targets 2>/dev/null)' -d command\n")
	for _, f := range flagList() {
		name, _, _ := strings.Cut(f[0], "/")
		fmt.Fprintf(&b, "complete -c construct -l %s -d %q\n", strings.TrimPrefix(name, "--"), f[1])
	}
	return b.String()
}

func flagWords() string {
	var words []string
	for _, f := range flagList() {
		for w := range strings.SplitSeq(f[0], "/") {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

func zshFlagArgs() string {
	var parts []string
	for _, f := range flagList() {
		long, _, _ := strings.Cut(f[0], "/")
		parts = append(parts, fmt.Sprintf("%q[%q]", long, f[1]))
	}
	return strings.Join(parts, " ")
}

func runFmt(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "fmt"); err != nil {
		return err
	}
	files := args
	if len(files) == 0 {
		files = []string{defaultConstfileName()}
	}
	unformatted := 0
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		formatted := pkg.FormatConstfile(string(content))
		if formatted == string(content) {
			continue
		}
		if o.checkFormat {
			fmt.Printf("%s is not formatted (run `construct fmt %s`)\n", file, file)
			unformatted++
			continue
		}
		if err := os.WriteFile(file, []byte(formatted), 0644); err != nil {
			return err
		}
		fmt.Printf("formatted %s\n", file)
	}
	if o.checkFormat && unformatted > 0 {
		return exitAt(1, "%d file(s) not formatted", unformatted)
	}
	return nil
}
