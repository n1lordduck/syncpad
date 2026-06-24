package ui

import (
	"fmt"
	"os"
	"strings"
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
	fyneApp fyne.App
	win     fyne.Window
	store   *config.Store

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

	foldersScroll *container.Scroll
}

func NewApp(a fyne.App, store *config.Store) *App {
	return &App{
		fyneApp:       a,
		store:         store,
		sessions:      make(map[string]*watcher.Session),
		logLabel:      widget.NewLabel(""),
		foldersScroll: container.NewVScroll(container.NewVBox()),
	}
}

func (app *App) BuildWindow() fyne.Window {
	app.win = app.fyneApp.NewWindow("SyncPad")
	app.win.Resize(fyne.NewSize(900, 580))

	app.logLabel.Wrapping = fyne.TextWrapWord
	app.logScroll = container.NewVScroll(app.logLabel)
	app.logScroll.SetMinSize(fyne.NewSize(0, 160))

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
			_ = app.store.Save()
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
				return
			}
			icon.SetResource(theme.FolderIcon())
		},
	)

	return list
}

func (app *App) renderFolders(c *config.Container) {
	vbox := container.NewVBox()
	for _, folder := range c.Folders {
		lbl := widget.NewLabel(fmt.Sprintf("• [%s] %s", folder.Name, folder.LocalPath))
		lbl.Truncation = fyne.TextTruncateEllipsis
		vbox.Add(lbl)
	}
	app.foldersScroll.Content = vbox
	app.foldersScroll.Refresh()
}

// refreshSendBtn enables the Send button only when there are pending files,
// and always updates the pending label to match.
func (app *App) refreshSendBtn(sess *watcher.Session) {
	n := sess.PendingCount()
	if n > 0 {
		app.pendingLabel.SetText(fmt.Sprintf("%d pending file(s)", n))
		app.pendingLabel.Show()
		app.sendBtn.Enable()
		return
	}
	app.pendingLabel.Hide()
	app.sendBtn.Disable()
}

func (app *App) buildDetailPanel() fyne.CanvasObject {
	nameLabel := widget.NewLabelWithStyle("Select a container", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	hostLabel := widget.NewLabel("")
	modeLabel := widget.NewLabel("")

	app.pendingLabel = widget.NewLabel("")
	app.pendingLabel.Hide()

	app.foldersScroll.SetMinSize(fyne.NewSize(0, 260))

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
			app.refreshSendBtn(sess)
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
			_ = app.store.Save()
			app.selected = updated
			hostLabel.SetText(fmt.Sprintf("SFTP: %s:%d → %s", updated.SFTP.Host, updated.SFTP.Port, updated.SFTP.RemotePath))
			modeLabel.SetText("Mode: " + string(updated.SyncMode))
			app.renderFolders(updated)
			app.sidebar.Refresh()
		})
	})

	ignoreBtn := widget.NewButtonWithIcon("Ignore", theme.ContentRemoveIcon(), func() {
		if app.selected == nil {
			return
		}
		ShowIgnoreForm(app.win, app.store, app.selected, func() {
			app.store.Update(app.selected)
			_ = app.store.Save()
		})
	})

	pullBtn := widget.NewButtonWithIcon("Pull", theme.DownloadIcon(), nil)
	pullBtn.OnTapped = func() {
		if app.selected == nil {
			return
		}
		c := app.selected
		app.mu.Lock()
		sess, ok := app.sessions[c.ID]
		app.mu.Unlock()
		if !ok {
			dialog.ShowInformation("Not watching", "Start watching the container before pulling.", app.win)
			return
		}
		pullBtn.SetText("Pulling...")
		pullBtn.Disable()
		go func() {
			app.doPull(sess, c)
			pullBtn.SetText("Pull")
			pullBtn.Enable()
		}()
	}

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
			_ = app.store.Save()
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
		hostLabel.SetText(fmt.Sprintf("SFTP: %s:%d → %s", c.SFTP.Host, c.SFTP.Port, c.SFTP.RemotePath))

		mode := c.SyncMode
		if mode == "" {
			mode = config.SyncManual
		}
		modeLabel.SetText("Mode: " + string(mode))
		app.renderFolders(c)

		app.mu.Lock()
		sess, active := app.sessions[c.ID]
		app.mu.Unlock()

		if !active {
			app.watchBtn.SetText("Watch")
			app.watchBtn.SetIcon(theme.MediaPlayIcon())
			app.sendBtn.Disable()
			app.pendingLabel.Hide()
			return
		}

		// Resuming view of an active session — restore full UI state.
		app.watchBtn.SetText("Stop")
		app.watchBtn.SetIcon(theme.MediaStopIcon())

		n := sess.PendingCount()
		if c.SyncMode == config.SyncManual || c.SyncMode == "" {
			if n > 0 {
				app.appendLog(fmt.Sprintf("↩ Resuming session '%s' — %d pending file(s).", c.Name, n))
			} else {
				app.appendLog(fmt.Sprintf("↩ Resuming session '%s' — nothing pending.", c.Name))
			}
			app.refreshSendBtn(sess)
		} else {
			// Auto mode: send button stays disabled, no pending label.
			app.sendBtn.Disable()
			app.pendingLabel.Hide()
			app.appendLog(fmt.Sprintf("↩ Resuming session '%s' (auto mode).", c.Name))
		}
	}

	topInfo := container.NewVBox(
		nameLabel,
		hostLabel,
		modeLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Mapped Folders:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)

	bottomActions := container.NewVBox(
		app.pendingLabel,
		container.NewHBox(app.watchBtn, app.sendBtn, pullBtn, editBtn, ignoreBtn, deleteBtn),
	)

	return container.NewBorder(
		topInfo,
		bottomActions,
		nil, nil,
		app.foldersScroll,
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
		app.appendLog(c.Name + " stopped.")
		watchBtn.SetText("Watch")
		watchBtn.SetIcon(theme.MediaPlayIcon())
		sendBtn.Disable()
		pendingLabel.Hide()
		app.sidebar.Refresh()
		return
	}

	watchBtn.Disable()
	app.appendLog(c.Name + " initializing...")

	go func() {
		sess := watcher.NewSession(c, app.store.GetGlobalIgnore())
		if err := sess.Start(); err != nil {
			dialog.ShowError(err, app.win)
			watchBtn.Enable()
			return
		}

		app.mu.Lock()
		app.sessions[c.ID] = sess
		app.mu.Unlock()

		app.appendLog(c.Name + " monitoring.")
		watchBtn.SetText("Stop")
		watchBtn.SetIcon(theme.MediaStopIcon())
		watchBtn.Enable()

		// Restore send button state from any pending carried over from last session.
		if c.SyncMode == config.SyncManual || c.SyncMode == "" {
			app.refreshSendBtn(sess)
		}

		app.sidebar.Refresh()

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
					prefix = "! "
				}
				app.appendLog(fmt.Sprintf("[%s] %s%s", ev.Time.Format("15:04:05"), prefix, ev.Message))

			case <-ticker.C:
				if c.SyncMode == config.SyncManual || c.SyncMode == "" {
					app.refreshSendBtn(sess)
				}
			}
		}
	}()
}

/*
func (app *App) refreshPending(sess *watcher.Session) {
	n := sess.PendingCount()
	if n > 0 {
		app.pendingLabel.SetText(fmt.Sprintf("%d pending file(s)", n))
		app.pendingLabel.Show()
		return
	}
	app.pendingLabel.Hide()
    }
*/

func (app *App) doPull(sess *watcher.Session, c *config.Container) {
	app.appendLog("pulling " + c.Name + "...")

	result, err := sess.Pull(func(msg string, isErr bool) {
		prefix := ""
		if isErr {
			prefix = "! "
		}
		app.appendLog("[pull] " + prefix + msg)
	})

	if err != nil {
		app.appendLog("pull failed: " + err.Error())
		return
	}

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			app.appendLog(e)
		}
	}

	if len(result.LocalOnly) == 0 {
		return
	}

	names := make([]string, len(result.LocalOnly))
	for i, p := range result.LocalOnly {
		names[i] = "• " + p
	}
	msg := fmt.Sprintf(
		"%d local file(s) not found on remote:\n\n%s\n\nDelete them locally?",
		len(result.LocalOnly),
		strings.Join(names, "\n"),
	)

	dialog.ShowConfirm("Local-only files", msg, func(ok bool) {
		if !ok {
			return
		}
		for _, p := range result.LocalOnly {
			if err := os.Remove(p); err != nil {
				app.appendLog("delete " + p + ": " + err.Error())
				continue
			}
			app.appendLog("removed local " + p)
		}
	}, app.win)
}

func (app *App) appendLog(line string) {
	app.logLines = append(app.logLines, line)
	if len(app.logLines) > 200 {
		app.logLines = app.logLines[len(app.logLines)-200:]
	}
	var sb strings.Builder
	for i := len(app.logLines) - 1; i >= 0; i-- {
		sb.WriteString(app.logLines[i] + "\n")
	}
	app.logLabel.SetText(sb.String())
	_ = time.AfterFunc(50*time.Millisecond, func() {
		app.logScroll.ScrollToTop()
	})
}
