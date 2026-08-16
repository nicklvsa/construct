package main

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/nicklvsa/construct/pkg"
)

type ui struct {
	doc  *pkg.UIDoc
	st   *pkg.UIState
	err  string
	boot string

	selFile string
	selCmd  string

	cmdClicks []widget.Clickable
	undoBtn   widget.Clickable
	saveBtn   widget.Clickable
	applyBtn  widget.Clickable

	nameEd   widget.Editor
	preEd    widget.Editor
	bodyEd   widget.Editor
	loaded   string // selFile+selCmd the editors were seeded with
	bodyLast string
}

func main() {
	file := "Constfile"
	if len(os.Args) > 1 {
		file = os.Args[1]
	}
	doc, err := pkg.NewUIDoc(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	u := &ui{doc: doc, boot: filepath.Base(doc.Main)}
	u.nameEd.SingleLine = true
	u.preEd.SingleLine = true

	go func() {
		w := new(app.Window)
		w.Option(app.Title("construct — "+u.boot), app.Size(1080, 680))
		if err := loop(w, u); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func loop(w *app.Window, u *ui) error {
	th := material.NewTheme()
	th.Palette = material.Palette{
		Bg:         color.NRGBA{R: 11, G: 14, B: 20, A: 255},
		Fg:         color.NRGBA{R: 215, G: 221, B: 232, A: 255},
		ContrastBg: color.NRGBA{R: 92, G: 173, B: 255, A: 255},
		ContrastFg: color.NRGBA{R: 8, G: 19, B: 31, A: 255},
	}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.frame(gtx, th)
			e.Frame(gtx.Ops)
		}
	}
}

func (u *ui) refresh() {
	u.st = u.doc.State()
	u.err = ""
	for _, f := range u.st.Files {
		if f.ParseError != "" {
			u.err = filepath.Base(f.Path) + ": " + f.ParseError
		}
	}
	if u.selFile == "" && len(u.st.Files) > 0 {
		u.selFile = u.st.Files[0].Path
	}
}

func (u *ui) frame(gtx layout.Context, th *material.Theme) {
	if u.st == nil {
		u.refresh()
	}
	u.handle(gtx)

	inset := layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.header(gtx, th)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.main(gtx, th)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.status(gtx, th)
		}),
	)
}

func (u *ui) handle(gtx layout.Context) {
	if u.undoBtn.Clicked(gtx) {
		u.doc.Undo()
		u.refresh()
	}
	if u.applyBtn.Clicked(gtx) {
		u.commitEditors()
		u.refresh()
	}
	if u.saveBtn.Clicked(gtx) {
		u.commitEditors()
		_, _, err := u.doc.Save()
		if err != nil {
			u.err = err.Error()
		}
		u.refresh()
	}
	for i := range u.cmdClicks {
		if u.cmdClicks[i].Clicked(gtx) {
			f := u.st.Files[fileIdx(u.st, u.selFile)]
			if i < len(f.Commands) {
				c := f.Commands[i]
				u.selCmd = c.Name
			}
			u.refresh()
		}
	}
}

func fileIdx(st *pkg.UIState, path string) int {
	for i, f := range st.Files {
		if f.Path == path {
			return i
		}
	}
	return 0
}

func (u *ui) header(gtx layout.Context, th *material.Theme) layout.Dimensions {
	return layout.Inset{Top: 8, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Body1(th, "construct — "+u.boot)
				l.TextSize = unit.Sp(18)
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return material.Button(th, &u.applyBtn, "Apply").Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return material.Button(th, &u.undoBtn, "Undo").Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						b := material.Button(th, &u.saveBtn, "Save")
						if !u.st.Dirty {
							b.Color = th.Palette.Fg
						}
						return b.Layout(gtx)
					}),
				)
			}),
		)
	})
}

func (u *ui) main(gtx layout.Context, th *material.Theme) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(260))
			return u.sidebar(gtx, th)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(14)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.editor(gtx, th)
		}),
	)
}

func (u *ui) sidebar(gtx layout.Context, th *material.Theme) layout.Dimensions {
	var rows []layout.FlexChild
	for _, f := range u.st.Files {
		if f.Path != u.selFile {
			continue
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			kind := "  (import)"
			if f.Main {
				kind = "  (main)"
			}
			l := material.Body1(th, filepath.Base(f.Path)+kind)
			l.Color = th.Palette.ContrastBg
			return layout.Inset{Top: 4, Bottom: 6}.Layout(gtx, l.Layout)
		}))
		if len(u.cmdClicks) != len(f.Commands) {
			u.cmdClicks = make([]widget.Clickable, len(f.Commands))
		}
		for i, c := range f.Commands {
			i, c := i, c
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				for u.cmdClicks[i].Clicked(gtx) {
					u.selCmd = c.Name
					u.selFile = f.Path
				}
				label := "  " + c.Name
				if c.Header != nil && c.Header.IsDefault {
					label = "  _ (default)"
				}
				if c.Display != c.Name {
					label += "   [" + c.Display + "]"
				}
				l := material.Body1(th, label)
				if c.Name == u.selCmd && f.Path == u.selFile {
					l.Color = th.Palette.ContrastBg
				}
				return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, l.Layout)
			}))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

func (u *ui) editor(gtx layout.Context, th *material.Theme) layout.Dimensions {
	key := u.selFile + "#" + u.selCmd
	if u.loaded != key {
		u.seedEditors()
		u.loaded = key
	}

	label := func(txt string) layout.Widget {
		return material.Body1(th, txt).Layout
	}
	ed := func(e *widget.Editor, hint string) layout.Widget {
		return material.Editor(th, e, hint).Layout
	}

	return layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Max.X = gtx.Dp(unit.Dp(220))
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label("name")),
							layout.Rigid(ed(&u.nameEd, "name")),
						)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label("prerequisites (comma separated)")),
							layout.Rigid(ed(&u.preEd, "build, test, src/*.go")),
						)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			layout.Rigid(label("body")),
			layout.Flexed(1, ed(&u.bodyEd, "body")),
		)
	})
}

func (u *ui) seedEditors() {
	u.nameEd.SetText("")
	u.preEd.SetText("")
	u.bodyEd.SetText("")
	for _, f := range u.st.Files {
		if f.Path != u.selFile {
			continue
		}
		for _, c := range f.Commands {
			if c.Name != u.selCmd {
				continue
			}
			name := c.Name
			u.nameEd.SetText(name)
			if c.Header != nil {
				u.preEd.SetText(strings.Join(c.Header.Prereqs, ", "))
				if len(c.Header.FileDeps) > 0 {
					if u.preEd.Text() != "" {
						u.preEd.Insert(", ")
					}
					u.preEd.Insert(strings.Join(c.Header.FileDeps, ", "))
				}
			}
			u.bodyEd.SetText(c.Body)
		}
	}
	u.bodyLast = u.bodyEd.Text()
}

func (u *ui) commitEditors() {
	if u.selCmd == "" {
		return
	}
	name := strings.TrimSpace(u.nameEd.Text())
	prereqs := splitCSV(u.preEd.Text())
	body := u.bodyEd.Text()

	ops := []pkg.UIEditOp{}
	if name != "" && name != u.selCmd {
		ops = append(ops, pkg.UIEditOp{File: u.selFile, Kind: "setHeader", Name: u.selCmd,
			Header: &pkg.UIHeaderPatch{Name: &name}})
	}
	target := u.selCmd
	if name != "" && name != u.selCmd {
		target = name
	}
	hdr := &pkg.UIHeaderPatch{Prereqs: &prereqs}
	ops = append(ops, pkg.UIEditOp{File: u.selFile, Kind: "setHeader", Name: target, Header: hdr})
	ops = append(ops, pkg.UIEditOp{File: u.selFile, Kind: "setBody", Name: target, Body: &body})
	if err := u.doc.Apply(ops); err != nil {
		u.err = err.Error()
	} else {
		u.selCmd = target
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (u *ui) status(gtx layout.Context, th *material.Theme) layout.Dimensions {
	msg := "saved"
	if u.st.Dirty {
		msg = "unsaved changes"
	}
	if u.err != "" {
		msg = u.err
	}
	l := material.Body1(th, msg)
	if u.err != "" {
		l.Color = color.NRGBA{R: 248, G: 113, B: 113, A: 255}
	} else if u.st.Dirty {
		l.Color = color.NRGBA{R: 251, G: 191, B: 36, A: 255}
	}
	errs, warns := 0, 0
	for _, f := range u.st.Files {
		for _, is := range f.Lint {
			if is.Severity == pkg.LintError {
				errs++
			} else {
				warns++
			}
		}
	}
	return layout.Inset{Top: 6, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(l.Layout),
			layout.Rigid(material.Body1(th, fmt.Sprintf("%d file(s) · %d error(s) · %d warning(s)",
				len(u.st.Files), errs, warns)).Layout),
		)
	})
}
