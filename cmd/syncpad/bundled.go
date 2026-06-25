package main

import (
	_ "embed"
	_ "image/png"

	"fyne.io/fyne/v2"
)

//go:embed tray.png
var trayIconBytes []byte

var trayIconResource = &fyne.StaticResource{
	StaticName:    "tray.png",
	StaticContent: trayIconBytes,
}
