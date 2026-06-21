package sftperrors

import (
	"errors"
	"strings"
)

var (
	ErrConnectionRefused = errors.New("the connection was refused. Check if the server is up and the port is correct.")
	ErrAuthentication    = errors.New("authentication failed. Please check your username, password, or private key.")
	ErrTimeout           = errors.New("the connection timed out. The server might be unreachable.")
)

func Parse(err error) error {
	if err == nil {
		return nil
	}

	msg := strings.ToLower(err.Error())

	mappings := []struct {
		pattern string
		target  error
	}{
		{"connection refused", ErrConnectionRefused},
		{"unable to authenticate", ErrAuthentication},
		{"timeout", ErrTimeout},
	}

	for _, m := range mappings {
		if strings.Contains(msg, m.pattern) {
			return m.target
		}
	}
	// TODO: We should probably hide this and log it somewhere else
	return err
}
