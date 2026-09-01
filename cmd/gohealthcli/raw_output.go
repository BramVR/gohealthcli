package main

import (
	"errors"
	"io"
	"os"
)

var errRawOutputNotImplemented = errors.New("raw --output is not implemented")

type rawOutputFile interface {
	io.Writer
	Chmod(os.FileMode) error
	Close() error
}

type openRawOutputFileFunc func(string) (rawOutputFile, error)

func writeRawOutputFile(path string, payload []byte) (int, error) {
	return 0, errRawOutputNotImplemented
}

func writeRawOutputFileWithOpen(path string, payload []byte, openFile openRawOutputFileFunc, removeFile func(string) error) (int, error) {
	return 0, errRawOutputNotImplemented
}
