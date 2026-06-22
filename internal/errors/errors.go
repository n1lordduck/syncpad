package sftperrors

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrConnectionRefused = fmt.Errorf("connection refused")
	ErrAuthentication    = fmt.Errorf("authentication failed")
	ErrTimeout           = fmt.Errorf("connection timeout")
)

var friendlyMessages = map[error]string{
	ErrConnectionRefused: "Connection refused. Check the host and port.",
	ErrAuthentication:    "Authentication failed. Check your password or private key.",
	ErrTimeout:           "Connection timed out. Check your internet or firewall.",
}

var errorMappings = []struct {
	pattern string
	target  error
}{
	{"connection refused", ErrConnectionRefused},
	{"unable to authenticate", ErrAuthentication},
	{"auth cancel", ErrAuthentication},
	{"keyboard-interactive", ErrAuthentication},
	{"timeout", ErrTimeout},
}

var errorRegex *regexp.Regexp

func init() {
	patterns := make([]string, len(errorMappings))
	for i, m := range errorMappings {
		patterns[i] = "(" + regexp.QuoteMeta(m.pattern) + ")"
	}

	combinedPattern := strings.Join(patterns, "|")
	errorRegex = regexp.MustCompile("(?i)" + combinedPattern)
}

func Parse(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	indices := errorRegex.FindStringSubmatchIndex(msg)
	if indices == nil {
		return err
	}

	for i := 1; i < len(indices)/2; i++ {
		if indices[i*2] == -1 {
			continue
		}

		targetErr := errorMappings[i-1].target

		if friendly, found := friendlyMessages[targetErr]; found {
			return fmt.Errorf("%s", friendly)
		}
		return targetErr
	}

	return err
}
