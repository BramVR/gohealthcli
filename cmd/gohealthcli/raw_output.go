package main

import (
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

type stagedRawOutput struct {
	targetPath string
	stagePath  string
	file       *os.File
	closed     bool
}

func writeRawOutputFile(path string, payload []byte) (int, error) {
	return writeRawOutputFileWithOpen(path, payload, openStagedRawOutput)
}

func writeRawOutputFileWithOpen(path string, payload []byte, openDestination openRawOutputDestinationFunc) (int, error) {
	destination, err := openDestination(path)
	if err != nil {
		return 0, err
	}
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
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".gohealthcli-raw-")
	if err != nil {
		return nil, fmt.Errorf("create --output staging file: %w", err)
	}
	stagePath := file.Name()
	if usesPOSIXPermissions() {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			_ = os.Remove(stagePath)
			return nil, fmt.Errorf("set --output staging file owner-only: %w", err)
		}
	}
	return &stagedRawOutput{
		targetPath: path,
		stagePath:  stagePath,
		file:       file,
	}, nil
}

func (output *stagedRawOutput) Write(payload []byte) (int, error) {
	return output.file.Write(payload)
}

func (output *stagedRawOutput) Chmod(mode os.FileMode) error {
	return output.file.Chmod(mode)
}

func (output *stagedRawOutput) Close() error {
	if output.closed {
		return nil
	}
	output.closed = true
	return output.file.Close()
}

func (output *stagedRawOutput) Commit() error {
	if !output.closed {
		return fmt.Errorf("staged output is still open")
	}
	if err := publishStagedRawOutput(output.stagePath, output.targetPath); err != nil {
		return err
	}
	return nil
}

func (output *stagedRawOutput) Abort() error {
	closeErr := output.Close()
	fileErr := os.Remove(output.stagePath)
	var cleanupErr error
	if closeErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close staged --output: %w", closeErr))
	}
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove staged --output: %w", fileErr))
	}
	return cleanupErr
}

func abortRawOutput(destination rawOutputDestination, primary error) error {
	if err := destination.Abort(); err != nil {
		return errors.Join(primary, err)
	}
	return primary
}
