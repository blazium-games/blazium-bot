//go:build !windows
// +build !windows

package main

import (
	"fmt"
	"io"
	"log/syslog"
	"os"

	"github.com/sirupsen/logrus"
	logrus_syslog "github.com/sirupsen/logrus/hooks/syslog"
)

func initSyslog(logger *logrus.Logger) {
	hook, err := logrus_syslog.NewSyslogHook("unix", "/dev/log", syslog.LOG_INFO|syslog.LOG_LOCAL0, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Syslog hook error: %v\n", err)
		os.Exit(1)
	}
	logger.AddHook(hook)
	logger.SetOutput(io.Discard)
}
