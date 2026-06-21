package ui

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/n1lordduck/syncpad/internal/config"
	"github.com/n1lordduck/syncpad/internal/watcher"
)

type App struct {
	fyne  fyne.App
	win   fyne.Window
	store *config.Store

	mu       sync.Mutex
	sessions map[string]*watcher.Session

	sidebar   *widget.List
	logLabel  *widget.Label
	logScroll *container.Scroll
	logLines  []string

	selected *config.Container

	pendingLabel *widget.Label
	sendBtn      *widget.Button
	watchBtn     *widget.Button
}

func NewApp(a fyne.App, store *config.Store) *App {
	return &App{
		fyne:     a,
		store:    store,
		sessions: make(map[string]*watcher.Session),
		logLabel: widget.NewLabel(""),
	}
}

func (app *App) BuildWindow() fyne.Window {
	app.win = app.fyne.NewWindow("SyncPad")
	app.win.Resize(fyne.NewSize(900, 580))

	app.logLabel.Wrapping = fyne.TextWrapWord
	app.logScroll = container.NewVScroll(app.logLabel)
	app.logScroll.SetMinSize(fyne.NewSize(0, 200))

	app.sidebar = app.buildSidebar()

	rightPanel := container.NewBorder(
		nil,
		container.NewVBox(widget.NewSeparator(), widget.NewLabel("Event log"), app.logScroll),
		nil, nil,
		app.buildDetailPanel(),
	)

	addBtn := widget.NewButtonWithIcon("New container", theme.ContentAddIcon(), func() {
		ShowContainerForm(app.win, nil, func(c *config.Container) {
			app.store.Add(c)
			app.store.Save()
			app.sidebar.Refresh()
		})
	})

	left := container.NewBorder(addBtn, nil, nil, nil, app.sidebar)
	split := container.NewHSplit(left, rightPanel)
	split.SetOffset(0.28)

	app.win.SetContent(split)
	return app.win
}

func (app *App) buildSidebar() *widget.List {
	list := widget.NewList(
		func() int { return len(app.store.All()) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewIcon(theme.FolderIcon()),
				widget.NewLabel("container"),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			containers := app.store.All()
			if id >= len(containers) {
				return
			}
			c := containers[id]
			box := obj.(*fyne.Container)
			label := box.Objects[1].(*widget.Label)
			label.SetText(c.Name)

			app.mu.Lock()
			_, active := app.sessions[c.ID]
			app.mu.Unlock()

			icon := box.Objects[0].(*widget.Icon)
			if active {
				icon.SetResource(theme.MediaPlayIcon())
			} else {
				icon.SetResource(theme.FolderIcon())
			}
		},
	)

	list.OnSelected = func(id widget.ListItemID) {
		containers := app.store.All()
		if id >= len(containers) {
			return
		}
		app.selected = containers[id]
	}

	return list
}

func (app *App) buildDetailPanel() fyne.CanvasObject {
	nameLabel := widget.NewLabelWithStyle("Select a container", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pathLabel := widget.NewLabel("")
	hostLabel := widget.NewLabel("")
	modeLabel := widget.NewLabel("")

	app.pendingLabel = widget.NewLabel("")
	app.pendingLabel.Hide()

	app.watchBtn = widget.NewButtonWithIcon("Watch", theme.MediaPlayIcon(), func() {
		if app.selected == nil {
			return
		}
		app.toggleWatch(app.selected, app.watchBtn, app.sendBtn, app.pendingLabel)
	})

	app.sendBtn = widget.NewButtonWithIcon("Send updates", theme.MailSendIcon(), func() {
		if app.selected == nil {
			return
		}
		app.mu.Lock()
		sess, ok := app.sessions[app.selected.ID]
		app.mu.Unlock()
		if !ok {
			return
		}
		go func() {
			sess.Flush()
			app.refreshPending(sess)
		}()
	})
	app.sendBtn.Importance = widget.HighImportance
	app.sendBtn.Disable()

	editBtn := widget.NewButtonWithIcon("Edit", theme.SettingsIcon(), func() {
		if app.selected == nil {
			return
		}
		ShowContainerForm(app.win, app.selected, func(updated *config.Container) {
			updated.ID = app.selected.ID
			app.store.Update(updated)
			app.store.Save()
			app.selected = updated
			pathLabel.SetText("Local: " + updated.LocalPath)
			hostLabel.SetText(fmt.Sprintf("SFTP: %s:%d → %s", updated.SFTP.Host, updated.SFTP.Port, updated.SFTP.RemotePath))
			modeLabel.SetText("Mode: " + string(updated.SyncMode))
			app.sidebar.Refresh()
		})
	})

	deleteBtn := widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), func() {
		if app.selected == nil {
			return
		}
		c := app.selected
		dialog.ShowConfirm("Remove container", "Remover '"+c.Name+"'?", func(ok bool) {
			if !ok {
				return
			}
			app.mu.Lock()
			if s, exists := app.sessions[c.ID]; exists {
				s.Stop()
				delete(app.sessions, c.ID)
			}
			app.mu.Unlock()
			app.store.Remove(c.ID)
			app.store.Save()
			app.selected = nil
			app.sidebar.Refresh()
		}, app.win)
	})
	deleteBtn.Importance = widget.DangerImportance

	app.sidebar.OnSelected = func(id widget.ListItemID) {
		containers := app.store.All()
		if id >= len(containers) {
			return
		}
		app.selected = containers[id]
		c := app.selected

		nameLabel.SetText(c.Name)
		pathLabel.SetText("Local: " + c.LocalPath)
		hostLabel.SetText(fmt.Sprintf("SFTP: %s:%d → %s", c.SFTP.Host, c.SFTP.Port, c.SFTP.RemotePath))

		mode := c.SyncMode
		if mode == "" {
			mode = config.SyncManual
		}
		modeLabel.SetText("Mode: " + string(mode))

		app.mu.Lock()
		sess, active := app.sessions[c.ID]
		app.mu.Unlock()

		if active {
			app.watchBtn.SetText("Stop")
			app.watchBtn.SetIcon(theme.MediaStopIcon())
			if c.SyncMode == config.SyncManual {
				app.sendBtn.Enable()
				n := sess.PendingCount()
				if n > 0 {
					app.pendingLabel.SetText(fmt.Sprintf("%d pending file(s)", n))
					app.pendingLabel.Show()
				} else {
					app.pendingLabel.Hide()
				}
			} else {
				app.sendBtn.Disable()
				app.pendingLabel.Hide()
			}
		} else {
			app.watchBtn.SetText("Watch")
			app.watchBtn.SetIcon(theme.MediaPlayIcon())
			app.sendBtn.Disable()
			app.pendingLabel.Hide()
		}
	}

	return container.NewVBox(
		nameLabel,
		pathLabel,
		hostLabel,
		modeLabel,
		widget.NewSeparator(),
		app.pendingLabel,
		container.NewHBox(app.watchBtn, app.sendBtn, editBtn, deleteBtn),
	)
}

func (app *App) toggleWatch(c *config.Container, watchBtn *widget.Button, sendBtn *widget.Button, pendingLabel *widget.Label) {
	app.mu.Lock()
	s, active := app.sessions[c.ID]
	app.mu.Unlock()

	if active {
		s.Stop()
		app.mu.Lock()
		delete(app.sessions, c.ID)
		app.mu.Unlock()
		app.appendLog("⏹ " + c.Name + " parado.")
		watchBtn.SetText("Watch")
		watchBtn.SetIcon(theme.MediaPlayIcon())
		sendBtn.Disable()
		pendingLabel.Hide()
		app.sidebar.Refresh()
		return
	}

	sess := watcher.NewSession(c)
	if err := sess.Start(); err != nil {
		dialog.ShowError(err, app.win)
		return
	}

	app.mu.Lock()
	app.sessions[c.ID] = sess
	app.mu.Unlock()

	app.appendLog("▶ " + c.Name + " monitorando.")
	watchBtn.SetText("Stop")
	watchBtn.SetIcon(theme.MediaStopIcon())

	if c.SyncMode == config.SyncManual || c.SyncMode == "" {
		sendBtn.Enable()
	}

	app.sidebar.Refresh()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case ev, ok := <-sess.Events:
				if !ok {
					return
				}
				prefix := ""
				if ev.Err {
					prefix = "⚠ "
				}
				app.appendLog(fmt.Sprintf("[%s] %s%s", ev.Time.Format("15:04:05"), prefix, ev.Message))

			case <-ticker.C:
				if c.SyncMode == config.SyncManual || c.SyncMode == "" {
					app.refreshPending(sess)
				}
			}
		}
	}()
}

func (app *App) refreshPending(sess *watcher.Session) {
	n := sess.PendingCount()
	if n > 0 {
		app.pendingLabel.SetText(fmt.Sprintf("%d pending file(s)", n))
		app.pendingLabel.Show()
	} else {
		app.pendingLabel.Hide()
	}
}

func (app *App) appendLog(line string) {
	app.logLines = append(app.logLines, line)
	if len(app.logLines) > 200 {
		app.logLines = app.logLines[len(app.logLines)-200:]
	}
	text := ""
	for i := len(app.logLines) - 1; i >= 0; i-- {
		text += app.logLines[i] + "\n"
	}
	app.logLabel.SetText(text)
	_ = time.AfterFunc(50*time.Millisecond, func() {
		app.logScroll.ScrollToTop()
	})
}
