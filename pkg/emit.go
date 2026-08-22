package pkg

import (
	"strings"
)

func EmitHeader(c *Command) string {
	var b strings.Builder
	if c.Manual {
		b.WriteString("manual ")
	}
	if c.IsService {
		b.WriteString("service ")
	}
	switch {
	case c.IsDefault:
		b.WriteString("_")
	case c.CloudAccessible:
		b.WriteString("|")
		b.WriteString(c.Name)
		b.WriteString("|")
	default:
		b.WriteString(c.Name)
	}
	if len(c.Arguments) > 0 {
		parts := make([]string, 0, len(c.Arguments))
		for _, a := range c.Arguments {
			s := a.Name
			if a.Default != "" {
				s += "=" + a.Default
			}
			if a.IsOptional {
				s = "opt " + s
			}
			parts = append(parts, s)
		}
		b.WriteString(" (")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	if c.Timeout != "" {
		b.WriteString(" timeout<")
		b.WriteString(c.Timeout)
		b.WriteString(">")
	}
	if c.Container != "" {
		b.WriteString(" container \"")
		b.WriteString(c.Container)
		b.WriteString("\"")
	}
	if len(c.Produces) > 0 {
		b.WriteString(" produces ")
		b.WriteString(strings.Join(c.Produces, ", "))
	}
	if c.WorkDir != "" {
		b.WriteString(" in ")
		b.WriteString(c.WorkDir)
	}
	if len(c.OnChange) > 0 {
		b.WriteString(" onchange ")
		b.WriteString(strings.Join(c.OnChange, ", "))
	}
	prereqs := make([]string, 0, len(c.Prereqs)+len(c.FileDeps))
	for _, p := range c.Prereqs {
		if dir := c.PrereqDirs[p]; dir != "" {
			p += " in " + dir
		}
		prereqs = append(prereqs, p)
	}
	prereqs = append(prereqs, c.FileDeps...)
	if len(prereqs) > 0 {
		b.WriteString(" < ")
		b.WriteString(strings.Join(prereqs, ", "))
	}
	b.WriteString(" {")
	return b.String()
}
