package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
)

var appLogger *logrus.Logger

type streamSplitter struct {
	stdout io.Writer
	stderr io.Writer
}

func (s streamSplitter) Write(p []byte) (n int, err error) {
	if len(p) >= 5 && (string(p[1:5]) == "warn" || string(p[1:5]) == "erro" || string(p[1:5]) == "fata" || string(p[1:5]) == "pani") {
		return s.stderr.Write(p)
	}
	return s.stdout.Write(p)
}

type multiWriter struct {
	writers []io.Writer
}

func (mw multiWriter) Write(p []byte) (n int, err error) {
	for _, w := range mw.writers {
		n, err = w.Write(p)
		if err != nil {
			return n, err
		}
	}
	return len(p), nil
}

func initLogger() {
	isWindows := runtime.GOOS == "windows"
	appLogger = logrus.StandardLogger()

	output := strings.ToLower(os.Getenv("LOG_OUTPUT"))
	if output == "" {
		if isWindows {
			output = "both"
		} else {
			output = "stream"
		}
	}

	format := strings.ToLower(os.Getenv("LOG_FORMAT"))
	if format == "" {
		format = "text"
	}

	fileName := os.Getenv("LOG_FILE_NAME")
	if fileName == "" {
		fileName = "app.log"
	}

	switch format {
	case "json":
		appLogger.SetFormatter(&logrus.JSONFormatter{})
	default:
		appLogger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	}

	switch output {
	case "stream":
		appLogger.SetOutput(streamSplitter{stdout: os.Stdout, stderr: os.Stderr})
	case "file":
		var dir string
		if isWindows {
			dir = "logs"
		} else {
			dir = "/var/log"
		}
		_ = os.MkdirAll(dir, 0755)
		f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Log file open error: %v\n", err)
			os.Exit(1)
		}
		appLogger.SetOutput(f)
	case "both":
		// Set up both console and file output
		var dir string
		if isWindows {
			dir = "logs"
		} else {
			dir = "/var/log"
		}
		_ = os.MkdirAll(dir, 0755)
		f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Log file open error: %v\n", err)
			os.Exit(1)
		}
		// Create multi-writer for both console and file
		appLogger.SetOutput(multiWriter{
			writers: []io.Writer{
				streamSplitter{stdout: os.Stdout, stderr: os.Stderr},
				f,
			},
		})
	case "syslog":
		if isWindows {
			fmt.Fprintln(os.Stderr, "Syslog not supported on Windows.")
			os.Exit(1)
		} else {
			initSyslog(appLogger)
		}
	default:
		fmt.Fprintf(os.Stderr, "Invalid LOG_OUTPUT: %s\n", output)
		os.Exit(1)
	}
	// Set log level from environment variable, default to info
	logLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch logLevel {
	case "debug":
		appLogger.SetLevel(logrus.DebugLevel)
	case "warn":
		appLogger.SetLevel(logrus.WarnLevel)
	case "error":
		appLogger.SetLevel(logrus.ErrorLevel)
	case "fatal":
		appLogger.SetLevel(logrus.FatalLevel)
	case "panic":
		appLogger.SetLevel(logrus.PanicLevel)
	default:
		appLogger.SetLevel(logrus.InfoLevel)
	}
}
