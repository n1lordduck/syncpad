package ui

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/n1lordduck/syncpad/internal/config"
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
	nameEntry.SetPlaceHolder("ex: addons")
	nameEntry.SetText(c.Name)

	localPathEntry := widget.NewEntry()
	localPathEntry.SetPlaceHolder("/home/user/gmod/addons")
	localPathEntry.SetText(c.LocalPath)

	browseBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		path, err := pickFolderNative()
		if err != nil || path == "" {
			d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
				if err != nil || lu == nil {
					return
				}
				localPathEntry.SetText(lu.Path())
			}, w)
			d.Show()
			return
		}
		localPathEntry.SetText(path)
	})

	hostEntry := widget.NewEntry()
	hostEntry.SetPlaceHolder("sftp.provedor.com")
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
		if err != nil || path == "" {
			d := dialog.NewFileOpen(func(f fyne.URIReadCloser, err error) {
				if err != nil || f == nil {
					return
				}
				keyPathEntry.SetText(f.URI().Path())
				f.Close()
			}, w)
			d.SetFilter(storage.NewExtensionFileFilter([]string{".pem", ".ppk", ""}))
			d.Show()
			return
		}
		keyPathEntry.SetText(path)
	})
	keyBrowseBtn.Disable()

	authSelect := widget.NewSelect([]string{"Senha", "Chave privada"}, func(val string) {
		if val == "Senha" {
			passEntry.Enable()
			keyPathEntry.Disable()
			keyBrowseBtn.Disable()
		} else {
			passEntry.Disable()
			keyPathEntry.Enable()
			keyBrowseBtn.Enable()
		}
	})
	if c.SFTP.Auth == config.AuthKey {
		authSelect.SetSelected("Chave privada")
	} else {
		authSelect.SetSelected("Senha")
	}

	remotePathEntry := widget.NewEntry()
	remotePathEntry.SetPlaceHolder("/garrysmod/addons")
	remotePathEntry.SetText(c.SFTP.RemotePath)

	syncModeSelect := widget.NewSelect([]string{"Manual", "Automático"}, func(val string) {
		if val == "Automático" {
			c.SyncMode = config.SyncAuto
		} else {
			c.SyncMode = config.SyncManual
		}
	})
	if c.SyncMode == config.SyncAuto {
		syncModeSelect.SetSelected("Automático")
	} else {
		syncModeSelect.SetSelected("Manual")
	}

	deleteSyncCheck := widget.NewCheck("Deletar arquivos remotos ao deletar localmente", func(v bool) {
		c.DeleteSync = v
	})
	deleteSyncCheck.SetChecked(c.DeleteSync)

	form := widget.NewForm(
		widget.NewFormItem("Nome do container", nameEntry),
		widget.NewFormItem("Pasta local", container.NewBorder(nil, nil, nil, browseBtn, localPathEntry)),
	)

	sftpForm := widget.NewForm(
		widget.NewFormItem("Host", hostEntry),
		widget.NewFormItem("Porta", portEntry),
		widget.NewFormItem("Usuário", userEntry),
		widget.NewFormItem("Autenticação", authSelect),
		widget.NewFormItem("Senha", passEntry),
		widget.NewFormItem("Chave privada", container.NewBorder(nil, nil, nil, keyBrowseBtn, keyPathEntry)),
		widget.NewFormItem("Caminho remoto", remotePathEntry),
	)

	content := container.NewVBox(
		widget.NewLabel("Configurações do container"),
		form,
		widget.NewSeparator(),
		widget.NewLabel("Configurações SFTP"),
		sftpForm,
		widget.NewForm(widget.NewFormItem("Modo de sync", syncModeSelect)),
		deleteSyncCheck,
	)

	title := "Novo Container"
	if existing != nil {
		title = "Editar: " + existing.Name
	}

	dialog.ShowCustomConfirm(title, "Salvar", "Cancelar", content, func(ok bool) {
		if !ok {
			return
		}
		port, _ := strconv.Atoi(portEntry.Text)
		if port == 0 {
			port = 22
		}

		auth := config.AuthPassword
		if authSelect.Selected == "Chave privada" {
			auth = config.AuthKey
		}

		c.Name = nameEntry.Text
		c.LocalPath = localPathEntry.Text
		c.SFTP.Host = hostEntry.Text
		c.SFTP.Port = port
		c.SFTP.User = userEntry.Text
		c.SFTP.Auth = auth
		c.SFTP.Password = passEntry.Text
		c.SFTP.KeyPath = keyPathEntry.Text
		c.SFTP.RemotePath = remotePathEntry.Text

		onSave(c)
	}, w)
}
