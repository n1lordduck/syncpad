package main

import (
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"github.com/n1lordduck/syncpad/internal/config"
	"github.com/n1lordduck/syncpad/internal/ui"
	"image/color"
)

type forcedDarkTheme struct {
	fyne.Theme
}

func (f *forcedDarkTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return f.Theme.Color(name, theme.VariantDark)
}

func showSplash(a fyne.App) fyne.Window {
	splash := a.NewWindow("")
	splash.SetFixedSize(true)
	splash.Resize(fyne.NewSize(320, 160))
	splash.CenterOnScreen()
	splash.SetPadded(false)

	bg := canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 24, A: 255})

	title := canvas.NewText("SyncPad", color.NRGBA{R: 220, G: 220, B: 255, A: 255})
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter

	sub := canvas.NewText("loading...", color.NRGBA{R: 140, G: 140, B: 160, A: 255})
	sub.TextSize = 13
	sub.Alignment = fyne.TextAlignCenter

	content := container.NewStack(
		bg,
		container.NewCenter(container.NewVBox(title, sub)),
	)

	splash.SetContent(content)
	splash.Show()
	return splash
}

func main() {
	_ = os.Setenv("LC_ALL", "en_US.UTF-8")
	_ = os.Setenv("LANG", "en_US.UTF-8")

	a := app.NewWithID("dev.n1lordduck.syncpad")
	a.Settings().SetTheme(&forcedDarkTheme{Theme: theme.DefaultTheme()})

	res, err := fyne.LoadResourceFromPath("assets/tray.png")
	if err != nil {
		res, _ = fyne.LoadResourceFromPath("../../assets/tray.png")
	}

	if res != nil {
		a.SetIcon(res)
	} else {
		log.Println("WARNING: tray.png icon was not found! The system tray will not initialize.")
	}

	var mainWin fyne.Window

	if desk, ok := a.(desktop.App); ok {
		m := fyne.NewMenu("SyncPad",
			fyne.NewMenuItem("Open", func() {
				if mainWin != nil {
					mainWin.Show()
				}
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", func() {
				a.Quit()
			}),
		)
		desk.SetSystemTrayMenu(m)
		if res != nil {
			desk.SetSystemTrayIcon(res)
		}
	}

	splash := showSplash(a)

	go func() {
		store, err := config.Load()
		if err != nil {
			log.Fatalf("config: %v", err)
		}

		syncApp := ui.NewApp(a, store)
		win := syncApp.BuildWindow()

		mainWin = win
		mainWin.SetCloseIntercept(func() {
			mainWin.Hide()
		})

		time.Sleep(1200 * time.Millisecond)

		splash.Close()
		mainWin.Show()
	}()

	a.Run()
}
