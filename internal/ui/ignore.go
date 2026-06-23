package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/n1lordduck/syncpad/internal/config"
)

func ShowIgnoreForm(w fyne.Window, store *config.Store, c *config.Container, onSave func()) {
	scopeOptions := []string{"Global", "This container only"}
	scopeSelect := widget.NewSelect(scopeOptions, nil)
	scopeSelect.SetSelected("Global")

	globalPatterns := store.GetGlobalIgnore()
	if len(globalPatterns) == 0 {
		globalPatterns = append([]string{}, config.DefaultIgnorePatterns...)
	}
	containerPatterns := make([]string, len(c.IgnorePatterns))
	copy(containerPatterns, c.IgnorePatterns)

	currentPatterns := func() *[]string {
		if scopeSelect.Selected == "Global" {
			return &globalPatterns
		}
		return &containerPatterns
	}

	listBox := container.NewVBox()
	newPatternEntry := widget.NewEntry()
	newPatternEntry.SetPlaceHolder("e.g. *.log or .env")

	var refreshList func()
	refreshList = func() {
		listBox.Objects = nil
		patterns := *currentPatterns()
		for i, p := range patterns {
			idx := i
			pat := p
			lbl := widget.NewLabel(pat)
			lbl.Truncation = fyne.TextTruncateEllipsis
			row := container.NewBorder(nil, nil, nil,
				widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					pts := currentPatterns()
					*pts = append((*pts)[:idx], (*pts)[idx+1:]...)
					refreshList()
				}),
				lbl,
			)
			listBox.Add(row)
		}
		listBox.Refresh()
	}

	scopeSelect.OnChanged = func(_ string) { refreshList() }
	refreshList()

	addBtn := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), func() {
		text := strings.TrimSpace(newPatternEntry.Text)
		if text == "" {
			return
		}
		pts := currentPatterns()
		*pts = append(*pts, text)
		newPatternEntry.SetText("")
		refreshList()
	})

	resetBtn := widget.NewButton("Reset to defaults", func() {
		pts := currentPatterns()
		*pts = append([]string{}, config.DefaultIgnorePatterns...)
		refreshList()
	})

	scroll := container.NewVScroll(listBox)
	scroll.SetMinSize(fyne.NewSize(400, 240))

	addRow := container.NewBorder(nil, nil, nil, addBtn, newPatternEntry)

	content := container.NewVBox(
		widget.NewLabelWithStyle("Scope", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		scopeSelect,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Ignore Patterns", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		scroll,
		addRow,
		resetBtn,
	)

	d := dialog.NewCustomConfirm("Ignore List", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		store.SetGlobalIgnore(globalPatterns)
		c.IgnorePatterns = containerPatterns
		onSave()
	}, w)
	d.Resize(fyne.NewSize(480, 460))
	d.Show()
}
