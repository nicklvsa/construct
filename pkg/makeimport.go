package pkg

import (
	"fmt"
	"regexp"
	"strings"
)

type ImportResult struct {
	Constfile string
	Commands  int
	Variables int
	Flagged   int
}

type importItem struct {
	kind  string // "var", "rule", "flag", "import"
	rule  *makeRule
	text  string
	orig  string
	value string
}

type makeRule struct {
	name      string
	prereqs   []string
	orderOnly []string
	recipe    []string
	doc       []string
}

type makeImporter struct {
	items       []importItem
	vars        map[string]bool
	varValues   map[string]string
	phony       map[string]bool
	seenTargets map[string]bool
	defaultGoal string
	firstGoal   string
	flagged     int
	varCount    int
	inDefine    bool
	cur         *makeRule
}

var makeAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.\-]*)\s*(:=|::=|\?=|\+=|=)\s*(.*)$`)

var makeFuncRes = []*regexp.Regexp{
	regexp.MustCompile(`\$\((shell)\s+[^)]*\)`),
	regexp.MustCompile(`\$\((wildcard)\s+([^)]*)\)`),
	regexp.MustCompile(`\$\((foreach|patsubst|subst|addprefix|addsuffix|filter|filter-out|sort|word|words|firstword|lastword|dir|notdir|basename|suffix|strip|findstring|join|abspath|realpath|if|or|and|call|value|origin|flavor|error|warning|info|eval)\s+[^)]*\)`),
}

var makeVarRe = regexp.MustCompile(`[$]\(([A-Za-z_][A-Za-z0-9_]*)\)|[$]\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func ImportMakefile(content string) (ImportResult, error) {
	lines := joinMakeContinuations(strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n"))

	imp := &makeImporter{
		vars:        map[string]bool{},
		varValues:   map[string]string{},
		phony:       map[string]bool{},
		seenTargets: map[string]bool{},
	}

	var doc []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			doc = nil
			continue
		}
		if strings.HasPrefix(line, "#") {
			doc = append(doc, strings.TrimSpace(strings.TrimLeft(line, "# ")))
			continue
		}
		imp.consume(line, doc, strings.HasPrefix(raw, "\t"))
		doc = nil
	}
	imp.finishRule()

	if imp.defaultGoal == "" {
		imp.defaultGoal = imp.firstGoal
	}
	if len(imp.items) == 0 {
		return ImportResult{}, fmt.Errorf("no makefile rules or variables found")
	}
	return imp.emit(), nil
}

func joinMakeContinuations(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		for strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") && i+1 < len(lines) {
			i++
			next := strings.TrimLeft(lines[i], " \t")
			line = strings.TrimRight(line, " \t\\") + " " + next
		}
		out = append(out, line)
	}
	return out
}

func (imp *makeImporter) flag(desc, orig string) {
	imp.flagged++
	imp.items = append(imp.items, importItem{kind: "flag", text: desc, orig: orig})
}

func (imp *makeImporter) consume(line string, doc []string, tabbed bool) {
	if imp.inDefine {
		if line == "endef" {
			imp.inDefine = false
		}
		return
	}

	if tabbed {
		if imp.cur == nil {
			imp.flag("recipe line outside a rule", line)
			return
		}
		imp.cur.recipe = append(imp.cur.recipe, line)
		return
	}

	fields := strings.Fields(line)
	switch fields[0] {
	case "ifeq", "ifneq", "ifdef", "ifndef", "else", "endif":
		imp.flag("skipped conditional directive (put conditions inside commands)", line)
		return
	case "define":
		imp.inDefine = true
		imp.flag("skipped define block", line)
		return
	case "export", "unexport", "vpath", "override":
		imp.flag("skipped "+fields[0]+" directive", line)
		return
	case "include", "-include", "sinclude":
		for _, f := range fields[1:] {
			imp.items = append(imp.items, importItem{kind: "import", text: strings.Trim(f, "\"")})
		}
		imp.flag("include semantics differ from make; verify import paths", "")
		return
	}

	if strings.HasPrefix(line, ".") && strings.Contains(line, ":") {
		name, rest, _ := strings.Cut(line, ":")
		rest = strings.TrimSpace(rest)
		switch strings.TrimSpace(name) {
		case ".PHONY":
			for f := range strings.FieldsSeq(rest) {
				imp.phony[f] = true
			}
			return
		case ".DEFAULT_GOAL":
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "="))
			if f := strings.Fields(rest); len(f) > 0 {
				imp.defaultGoal = f[0]
			}
			return
		}
		imp.flag("skipped special make target", line)
		return
	}

	if m := makeAssignRe.FindStringSubmatch(line); m != nil {
		imp.varDecl(m[1], m[2], m[3])
		return
	}

	if strings.Contains(line, ":") {
		imp.ruleLine(line, doc)
		return
	}

	imp.flag("unrecognized statement", line)
}

func (imp *makeImporter) varDecl(name, op, value string) {
	emit := func() {
		imp.vars[name] = true
		if e, ok := imp.expandRefs(value); ok {
			imp.varValues[name] = e
		} else {
			imp.varValues[name] = value
		}
		imp.varCount++
		imp.items = append(imp.items, importItem{kind: "var", text: name, value: value})
	}
	switch op {
	case "?=":
		if imp.vars[name] {
			imp.flag("?= after an earlier definition skipped", name+" "+op+" "+value)
			return
		}
		emit()
		imp.flag("?= treated as ordinary assignment", "")
	case "+=":
		if imp.vars[name] {
			imp.flag("+= on an existing variable is not supported", name+" "+op+" "+value)
			return
		}
		emit()
	default:
		if imp.vars[name] {
			imp.flag("duplicate assignment to "+name+" skipped (conditional branches?); keeping the first", name+" "+op+" "+value)
			return
		}
		emit()
	}
}

func (imp *makeImporter) ruleLine(line string, doc []string) {
	imp.finishRule()

	doubleColon := strings.Contains(line, "::")
	before, after, _ := strings.Cut(line, ":")
	if doubleColon {
		after = strings.TrimPrefix(after, ":")
		imp.flag("double-colon rule treated as a normal rule", line)
	}
	targets := strings.Fields(before)
	if len(targets) == 0 {
		imp.flag("rule without a target", line)
		return
	}
	expanded := make([]string, 0, len(targets))
	for _, tgt := range targets {
		e, ok := imp.expandRefs(tgt)
		if !ok {
			imp.flag("unresolved variable in target "+tgt, line)
			return
		}
		expanded = append(expanded, strings.Fields(e)...)
	}
	targets = expanded
	if len(targets) == 0 {
		imp.flag("rule without a target", line)
		return
	}
	if strings.Contains(targets[0], "%") {
		imp.flag("pattern rule skipped (no construct equivalent; write one command per target)", line)
		return
	}
	for _, tgt := range targets {
		if imp.seenTargets[tgt] {
			imp.flag("duplicate rule for "+tgt+" skipped (conditional branches?)", line)
			return
		}
		imp.seenTargets[tgt] = true
	}

	prereqPart, semiRecipe, _ := strings.Cut(after, ";")
	normal, orderOnly := splitOrderOnly(strings.TrimSpace(prereqPart))
	if e, ok := imp.expandRefs(normal); ok {
		normal = e
	} else {
		imp.flag("unresolved variable in prerequisites: "+normal, line)
	}
	if e, ok := imp.expandRefs(orderOnly); ok {
		orderOnly = e
	}
	prereqs := strings.Fields(normal)
	for i, p := range prereqs {
		if strings.Contains(p, "=") {
			imp.flag("target-specific variable skipped", p)
			prereqs = append(prereqs[:i], prereqs[i+1:]...)
			break
		}
	}

	rule := &makeRule{name: targets[0], prereqs: prereqs, orderOnly: strings.Fields(orderOnly), doc: doc}
	if semiRecipe != "" {
		rule.recipe = append(rule.recipe, strings.TrimSpace(semiRecipe))
	}
	imp.cur = rule
	if imp.firstGoal == "" && !strings.HasPrefix(targets[0], ".") {
		imp.firstGoal = targets[0]
	}
	for _, extra := range targets[1:] {
		clone := *rule
		clone.name = extra
		clone.doc = nil
		imp.items = append(imp.items, importItem{kind: "rule", rule: &clone})
	}
}

func (imp *makeImporter) expandRefs(s string) (string, bool) {
	if !strings.Contains(s, "$") {
		return s, true
	}
	ok := true
	out := makeVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m
		name = strings.TrimSuffix(strings.TrimPrefix(name, "$("), ")")
		name = strings.TrimSuffix(strings.TrimPrefix(name, "${"), "}")
		if v, have := imp.varValues[name]; have {
			return v
		}
		ok = false
		return m
	})
	return out, ok
}

func splitOrderOnly(s string) (string, string) {
	if i := strings.IndexByte(s, '|'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func (imp *makeImporter) finishRule() {
	if imp.cur == nil {
		return
	}
	imp.items = append(imp.items, importItem{kind: "rule", rule: imp.cur})
	imp.cur = nil
}

func (imp *makeImporter) emit() ImportResult {
	mapping := map[string]string{}
	taken := map[string]bool{}
	for _, it := range imp.items {
		if it.kind != "rule" {
			continue
		}
		mapping[it.rule.name] = uniqueName(commandName(it.rule.name, imp.phony[it.rule.name]), taken)
	}

	var out strings.Builder
	var flags strings.Builder
	out.WriteString("# Imported from a Makefile by `construct import`. Review construct-import comments.\n\n")
	commands := 0
	for _, it := range imp.items {
		switch it.kind {
		case "var":
			converted, ok := imp.convertText(it.value, nil)
			if !ok {
				imp.flagged++
				flags.WriteString("# construct-import: variable ")
				flags.WriteString(it.text)
				flags.WriteString(" needs manual translation\n")
				flags.WriteString("# ")
				flags.WriteString(it.text)
				flags.WriteString(" = ")
				flags.WriteString(it.value)
				flags.WriteString("\n\n")
				continue
			}
			out.WriteString("var ")
			out.WriteString(it.text)
			out.WriteString(" = ")
			out.WriteString(converted)
			out.WriteString("\n")
		case "import":
			out.WriteString("import \"")
			out.WriteString(it.text)
			out.WriteString("\"\n")
		case "flag":
			flags.WriteString("# construct-import: ")
			flags.WriteString(it.text)
			flags.WriteString("\n")
			if it.orig != "" {
				flags.WriteString("# ")
				flags.WriteString(it.orig)
				flags.WriteString("\n")
			}
			flags.WriteString("\n")
		case "rule":
			imp.emitRule(&out, &flags, it.rule, mapping)
			commands++
		}
	}

	if def, ok := mapping[imp.defaultGoal]; ok && imp.defaultGoal != "" {
		out.WriteString("\n# Default goal\n_ < ")
		out.WriteString(def)
		out.WriteString(" { }\n")
		commands++
	}
	if flags.Len() > 0 {
		out.WriteString("\n# ---- flagged during import ----\n")
		out.WriteString(flags.String())
	}

	res := ImportResult{
		Constfile: FormatConstfile(out.String()),
		Commands:  commands,
		Variables: imp.varCount,
		Flagged:   imp.flagged,
	}
	return res
}

func (imp *makeImporter) emitRule(out, flags *strings.Builder, rule *makeRule, mapping map[string]string) {
	if len(rule.doc) > 0 {
		for _, d := range rule.doc {
			out.WriteString("# ")
			out.WriteString(d)
			out.WriteString("\n")
		}
	}
	name := mapping[rule.name]
	phony := imp.phony[rule.name]
	fileLike := strings.ContainsAny(rule.name, "./") && !phony

	prereqs := make([]string, 0, len(rule.prereqs))
	for _, p := range rule.prereqs {
		if m, ok := mapping[p]; ok {
			prereqs = append(prereqs, m)
		} else {
			prereqs = append(prereqs, p)
		}
	}
	if len(rule.orderOnly) > 0 {
		imp.flagged++
		flags.WriteString("# construct-import: order-only prerequisites skipped: ")
		flags.WriteString(strings.Join(rule.orderOnly, ", "))
		flags.WriteString("\n\n")
	}

	var header string
	switch {
	case fileLike && len(prereqs) > 0:
		header = name + " produces " + rule.name + " < " + strings.Join(prereqs, ", ")
	case fileLike:
		header = name + " produces " + rule.name
	case len(prereqs) > 0:
		header = name + " < " + strings.Join(prereqs, ", ")
	default:
		header = name
	}

	if len(rule.recipe) == 0 {
		out.WriteString(header)
		out.WriteString(" { }\n")
		return
	}
	out.WriteString(header)
	out.WriteString(" {\n")
	for _, r := range rule.recipe {
		tolerant := false
		for len(r) > 0 && (r[0] == '@' || r[0] == '+') {
			r = r[1:]
		}
		if strings.HasPrefix(r, "-") {
			tolerant = true
			r = r[1:]
		}
		r = strings.TrimSpace(r)
		auto := &makeAutoVars{target: rule.name, prereqs: rule.prereqs}
		converted, ok := imp.convertText(r, auto)
		if !ok {
			imp.flagged++
			out.WriteString("    # construct-import: needs manual translation\n")
			out.WriteString("    # $ ")
			out.WriteString(r)
			out.WriteString("\n")
			continue
		}
		prefix := "    $ "
		if tolerant {
			prefix = "    ! $ "
		}
		out.WriteString(prefix)
		out.WriteString(converted)
		out.WriteString("\n")
	}
	out.WriteString("}\n")
}

type makeAutoVars struct {
	target  string
	prereqs []string
}

func (imp *makeImporter) convertText(s string, auto *makeAutoVars) (string, bool) {
	s = strings.ReplaceAll(s, "$$", "\x00")

	s = strings.ReplaceAll(s, "$(MAKE)", "construct")
	s = strings.ReplaceAll(s, "${MAKE}", "construct")

	for _, re := range makeFuncRes {
		if m := re.FindStringSubmatch(s); m != nil {
			if m[1] == "wildcard" {
				s = strings.Replace(s, m[0], m[2], 1)
				continue
			}
			return "", false
		}
	}

	ok := true
	s = makeVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m
		name = strings.TrimSuffix(strings.TrimPrefix(name, "$("), ")")
		name = strings.TrimSuffix(strings.TrimPrefix(name, "${"), "}")
		if imp.vars[name] {
			return "&" + name
		}
		ok = false
		return m
	})
	if !ok {
		return "", false
	}

	if auto != nil {
		all := strings.Join(auto.prereqs, " ")
		first := ""
		if len(auto.prereqs) > 0 {
			first = auto.prereqs[0]
		}
		s = strings.ReplaceAll(s, "$@", auto.target)
		s = strings.ReplaceAll(s, "$<", first)
		s = strings.ReplaceAll(s, "$^", all)
		s = strings.ReplaceAll(s, "$?", all)
		if strings.Contains(s, "$*") {
			return "", false
		}
	}

	if strings.Contains(s, "$(") || strings.Contains(s, "${") {
		return "", false
	}
	return strings.ReplaceAll(s, "\x00", "$"), true
}

func commandName(target string, phony bool) string {
	if !phony && strings.ContainsAny(target, "./") {
		var b strings.Builder
		for _, r := range target {
			if r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else {
				b.WriteRune('-')
			}
		}
		target = b.String()
	}
	if target == "" {
		target = "target"
	}
	return target
}

func uniqueName(base string, taken map[string]bool) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	taken[name] = true
	return name
}
