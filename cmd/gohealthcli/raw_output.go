package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type rawOutputDestination interface {
	io.Writer
	Chmod(os.FileMode) error
	Close() error
	Commit() error
	Abort() error
}

type openRawOutputDestinationFunc func(string) (rawOutputDestination, error)

type preparedRawOutput interface {
	Complete([]byte) (int, error)
	Abort(error) error
}

type preparedRawOutputFile struct {
	path        string
	destination rawOutputDestination
}

type rawOutputValidationError struct {
	err error
}

func (validationError *rawOutputValidationError) Error() string { return validationError.err.Error() }
func (validationError *rawOutputValidationError) Unwrap() error { return validationError.err }

func prepareRawOutputFile(path string) (preparedRawOutput, error) {
	destination, err := openStagedRawOutput(path)
	if err != nil {
		return nil, err
	}
	return &preparedRawOutputFile{path: path, destination: destination}, nil
}

func (output *preparedRawOutputFile) Complete(payload []byte) (int, error) {
	return writePreparedRawOutput(output.path, payload, output.destination)
}

func (output *preparedRawOutputFile) Abort(primary error) error {
	return abortRawOutput(output.destination, primary)
}

func writeRawOutputFile(path string, payload []byte) (int, error) {
	output, err := prepareRawOutputFile(path)
	if err != nil {
		return 0, err
	}
	return output.Complete(payload)
}

func writeRawOutputFileWithOpen(path string, payload []byte, openDestination openRawOutputDestinationFunc) (int, error) {
	destination, err := openDestination(path)
	if err != nil {
		return 0, err
	}
	return writePreparedRawOutput(path, payload, destination)
}

func writePreparedRawOutput(path string, payload []byte, destination rawOutputDestination) (int, error) {
	written, writeErr := destination.Write(payload)
	if writeErr == nil && written != len(payload) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return written, abortRawOutput(destination, fmt.Errorf("write --output %q: %w", path, writeErr))
	}
	if usesPOSIXPermissions() {
		if err := destination.Chmod(0o600); err != nil {
			return written, abortRawOutput(destination, fmt.Errorf("set --output %q owner-only: %w", path, err))
		}
	}
	if err := destination.Close(); err != nil {
		return written, abortRawOutput(destination, fmt.Errorf("close --output %q: %w", path, err))
	}
	if err := destination.Commit(); err != nil {
		return written, abortRawOutput(destination, fmt.Errorf("install --output %q: %w", path, err))
	}
	return written, nil
}

func validateRawOutputDestination(path string) error {
	if err := rawOutputPlatformSupported(); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect --output parent %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--output parent %q is not a directory", parent)
	}
	if err := validateOutputPathNoFollow(path); err != nil {
		return fmt.Errorf("inspect --output %q: %w", path, err)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("--output %q already exists; refusing to overwrite", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect --output %q: %w", path, err)
	}
	return nil
}

func openStagedRawOutput(path string) (rawOutputDestination, error) {
	if err := validateRawOutputDestination(path); err != nil {
		return nil, &rawOutputValidationError{err: err}
	}
	return openPlatformRawOutput(path)
}

func randomRawOutputLeaf() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".gohealthcli-raw-" + hex.EncodeToString(value[:]), nil
}

func abortRawOutput(destination rawOutputDestination, primary error) error {
	if err := destination.Abort(); err != nil {
		return errors.Join(primary, err)
	}
	return primary
}
