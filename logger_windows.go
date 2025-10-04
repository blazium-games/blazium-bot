//go:build windows
// +build windows

package main

import (
	"github.com/sirupsen/logrus"
)

func initSyslog(_ *logrus.Logger) {
	// no-op on Windows
}
