package sftperrors

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrConnectionRefused = fmt.Errorf("connection refused error")
	ErrAuthentication    = fmt.Errorf("auth error")
	ErrTimeout           = fmt.Errorf("timeout error")
)

var errorMappings = []struct {
	pattern string
	target  error
}{
	{"connection refused", ErrConnectionRefused},
	{"unable to authenticate", ErrAuthentication},
	{"timeout", ErrTimeout},
}

var errorRegex *regexp.Regexp

func init() {
	patterns := make([]string, len(errorMappings))
	for i, m := range errorMappings {
		patterns[i] = "(" + regexp.QuoteMeta(m.pattern) + ")"
	}

	combinedPattern := strings.Join(patterns, "|")
	errorRegex = regexp.MustCompile(combinedPattern)
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
		if indices[i*2] != -1 {
			return errorMappings[i-1].target
		}
	}

	return err
}
