package ui

import (
	"fmt"
	"log"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/n1lordduck/syncpad/internal/config"
	sftperrors "github.com/n1lordduck/syncpad/internal/errors"
	sftpclient "github.com/n1lordduck/syncpad/internal/sftp"
)

func ShowContainerForm(w fyne.Window, existing *config.Container, onSave func(*config.Container)) {
	c := &config.Container{}
	if existing != nil {
		cp := *existing
		c = &cp
	}
	if c.SFTP.Port == 0 {
		c.SFTP.Port = 22
	}
	if c.SFTP.Auth == "" {
		c.SFTP.Auth = config.AuthPassword
	}

	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("ex: My cool Server")
	nameEntry.SetText(c.Name)

	hostEntry := widget.NewEntry()
	hostEntry.SetPlaceHolder("sftp.provider.com")
	hostEntry.SetText(c.SFTP.Host)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(c.SFTP.Port))

	userEntry := widget.NewEntry()
	userEntry.SetText(c.SFTP.User)

	passEntry := widget.NewPasswordEntry()
	passEntry.SetText(c.SFTP.Password)

	keyPathEntry := widget.NewEntry()
	keyPathEntry.SetPlaceHolder("/home/user/.ssh/id_rsa")
	keyPathEntry.SetText(c.SFTP.KeyPath)
	keyPathEntry.Disable()

	keyBrowseBtn := widget.NewButtonWithIcon("", theme.FileIcon(), func() {
		path, err := pickFileNative()
		if err == nil && path != "" {
			keyPathEntry.SetText(path)
			return
		}
		d := dialog.NewFileOpen(func(f fyne.URIReadCloser, err error) {
			if err != nil || f == nil {
				return
			}
			keyPathEntry.SetText(f.URI().Path())
			_ = f.Close()
		}, w)
		d.SetFilter(storage.NewExtensionFileFilter([]string{".pem", ".ppk", ""}))
		d.Show()
	})
	keyBrowseBtn.Disable()

	remotePathEntry := widget.NewEntry()
	remotePathEntry.SetPlaceHolder("/home/server/garrysmod")
	remotePathEntry.SetText(c.SFTP.RemotePath)

	validationLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	validationLabel.Hide()

	isAuthKey := c.SFTP.Auth == config.AuthKey

	validate := func() bool {
		var missing []string
		if nameEntry.Text == "" {
			missing = append(missing, "Container name")
		}
		if hostEntry.Text == "" {
			missing = append(missing, "Host")
		}
		if userEntry.Text == "" {
			missing = append(missing, "Username")
		}
		if remotePathEntry.Text == "" {
			missing = append(missing, "Root Path")
		}
		if isAuthKey && keyPathEntry.Text == "" {
			missing = append(missing, "Private key path")
		}
		if !isAuthKey && passEntry.Text == "" {
			missing = append(missing, "Password")
		}
		if len(missing) > 0 {
			msg := "Required: "
			for i, m := range missing {
				if i > 0 {
					msg += ", "
				}
				msg += m
			}
			validationLabel.SetText(msg)
			validationLabel.Show()
			return false
		}
		validationLabel.Hide()
		return true
	}

	nameEntry.OnChanged = func(_ string) { validate() }
	hostEntry.OnChanged = func(_ string) { validate() }
	userEntry.OnChanged = func(_ string) { validate() }
	remotePathEntry.OnChanged = func(_ string) { validate() }
	passEntry.OnChanged = func(_ string) { validate() }
	keyPathEntry.OnChanged = func(_ string) { validate() }

	authSelect := widget.NewSelect([]string{"Password", "Private key"}, func(val string) {
		isAuthKey = val == "Private key"
		if isAuthKey {
			passEntry.Disable()
			keyPathEntry.Enable()
			keyBrowseBtn.Enable()
		} else {
			passEntry.Enable()
			keyPathEntry.Disable()
			keyBrowseBtn.Disable()
		}
		validate()
	})
	if c.SFTP.Auth == config.AuthKey {
		authSelect.SetSelected("Private key")
	} else {
		authSelect.SetSelected("Password")
	}

	syncModeSelect := widget.NewSelect([]string{"Manual", "Automatic"}, func(val string) {
		if val == "Automatic" {
			c.SyncMode = config.SyncAuto
			return
		}
		c.SyncMode = config.SyncManual
	})
	if c.SyncMode == config.SyncAuto {
		syncModeSelect.SetSelected("Automatic")
	} else {
		syncModeSelect.SetSelected("Manual")
	}

	deleteSyncCheck := widget.NewCheck("Delete remote files when deleted locally", func(v bool) {
		c.DeleteSync = v
	})
	deleteSyncCheck.SetChecked(c.DeleteSync)

	foldersListContainer := container.NewVBox()
	var currentFolders []config.FolderItem
	if c.Folders != nil {
		currentFolders = append(currentFolders, c.Folders...)
	}

	var refreshFoldersList func()
	refreshFoldersList = func() {
		foldersListContainer.Objects = nil
		for i, folder := range currentFolders {
			idx := i
			localLbl := widget.NewLabel(folder.LocalPath)
			localLbl.Truncation = fyne.TextTruncateEllipsis
			item := container.NewBorder(
				nil, nil,
				widget.NewLabel("["+folder.Name+"] "),
				widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					currentFolders = append(currentFolders[:idx], currentFolders[idx+1:]...)
					refreshFoldersList()
				}),
				localLbl,
			)
			foldersListContainer.Add(item)
		}
		foldersListContainer.Refresh()
	}
	refreshFoldersList()

	addFolderBtn := widget.NewButtonWithIcon("Add Folder Mapped Entry", theme.ContentAddIcon(), func() {
		nameIn := widget.NewEntry()
		nameIn.SetPlaceHolder("ex: addons")
		pathIn := widget.NewEntry()
		pathIn.SetPlaceHolder("/home/user/gmod/addons")

		browseFolderBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
			path, err := pickFolderNative()
			if err == nil && path != "" {
				pathIn.SetText(path)
				return
			}
			d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
				if err == nil && lu != nil {
					pathIn.SetText(lu.Path())
				}
			}, w)
			d.Show()
		})

		fForm := widget.NewForm(
			widget.NewFormItem("Remote Folder Name", nameIn),
			widget.NewFormItem("Local Target Path", container.NewBorder(nil, nil, nil, browseFolderBtn, pathIn)),
		)

		d := dialog.NewCustomConfirm("New Folder Mapping", "Add", "Cancel",
			container.NewStack(fForm),
			func(ok bool) {
				if !ok || nameIn.Text == "" || pathIn.Text == "" {
					return
				}
				currentFolders = append(currentFolders, config.FolderItem{
					Name:      nameIn.Text,
					LocalPath: pathIn.Text,
				})
				refreshFoldersList()
			}, w)
		d.Resize(fyne.NewSize(560, 240))
		d.Show()
	})

	testBtn := widget.NewButtonWithIcon("Test connection", theme.MediaPlayIcon(), func() {
		port, _ := strconv.Atoi(portEntry.Text)
		if port == 0 {
			port = 22
		}
		auth := config.AuthPassword
		if authSelect.Selected == "Private key" {
			auth = config.AuthKey
		}
		cfg := config.SFTPConfig{
			Host:       hostEntry.Text,
			Port:       port,
			User:       userEntry.Text,
			Auth:       auth,
			Password:   passEntry.Text,
			KeyPath:    keyPathEntry.Text,
			RemotePath: remotePathEntry.Text,
		}
		statusLabel := widget.NewLabel("Connecting...")
		d := dialog.NewCustom("Test connection", "Close", statusLabel, w)
		d.Show()
		go func() {
			client, err := sftpclient.Connect(cfg)
			if err != nil {
				friendlyMsg := sftperrors.Parse(err)
				log.Printf("SFTP Connection failed: %v", err)
				statusLabel.SetText(friendlyMsg.Error())
				return
			}
			client.Close()
			statusLabel.SetText("✔ Connected successfully!")
		}()
	})

	foldersScroll := container.NewVScroll(foldersListContainer)
	foldersScroll.SetMinSize(fyne.NewSize(340, 200))

	leftColumn := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Container Identification", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(widget.NewFormItem("Name", nameEntry)),
			widget.NewSeparator(),
			widget.NewLabelWithStyle("Mapped Folder Entries", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		),
		addFolderBtn,
		nil, nil,
		foldersScroll,
	)

	rightColumn := container.NewVBox(
		widget.NewLabelWithStyle("SFTP Target Settings (Root)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Host", hostEntry),
			widget.NewFormItem("Port", portEntry),
			widget.NewFormItem("Username", userEntry),
			widget.NewFormItem("Authentication", authSelect),
			widget.NewFormItem("Password", passEntry),
			widget.NewFormItem("Private key", container.NewBorder(nil, nil, nil, keyBrowseBtn, keyPathEntry)),
			widget.NewFormItem("Root Path", remotePathEntry),
		),
		testBtn,
		widget.NewSeparator(),
		widget.NewForm(widget.NewFormItem("Sync Mode", syncModeSelect)),
		deleteSyncCheck,
	)

	content := container.NewVBox(
		container.NewGridWithColumns(2, leftColumn, rightColumn),
		validationLabel,
	)

	scrollableContent := container.NewVScroll(content)
	scrollableContent.SetMinSize(fyne.NewSize(760, 440))

	title := "New Container"
	if existing != nil {
		title = "Edit: " + existing.Name
	}

	var d *dialog.CustomDialog

	saveBtn := widget.NewButton("Save", func() {
		if !validate() {
			dialog.ShowError(fmt.Errorf(validationLabel.Text), w)
			return
		}
		port, _ := strconv.Atoi(portEntry.Text)
		if port == 0 {
			port = 22
		}
		auth := config.AuthPassword
		if authSelect.Selected == "Private key" {
			auth = config.AuthKey
		}
		c.Name = nameEntry.Text
		c.Folders = currentFolders
		c.SFTP.Host = hostEntry.Text
		c.SFTP.Port = port
		c.SFTP.User = userEntry.Text
		c.SFTP.Auth = auth
		c.SFTP.Password = passEntry.Text
		c.SFTP.KeyPath = keyPathEntry.Text
		c.SFTP.RemotePath = remotePathEntry.Text
		d.Hide()
		onSave(c)
	})
	saveBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancel", func() {
		d.Hide()
	})

	buttons := container.NewHBox(cancelBtn, saveBtn)
	fullContent := container.NewBorder(nil, buttons, nil, nil, scrollableContent)

	d = dialog.NewCustomWithoutButtons(title, fullContent, w)
	d.Resize(fyne.NewSize(800, 560))
	d.Show()
}
