package ui

import (
	"os/exec"
	"strings"
)

func pickFolderNative() (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--directory").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func pickFileNative() (string, error) {
	out, err := exec.Command("zenity", "--file-selection").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
