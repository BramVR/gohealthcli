//go:build windows

package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsRawOutputFinalPathIsNetwork(path string) bool {
	return strings.HasPrefix(strings.ToUpper(path), `\\?\UNC\`)
}

func validateWindowsRawOutputLeaf(leaf string) error {
	if leaf == "" || leaf == "." || leaf == ".." || strings.HasSuffix(leaf, ".") || strings.HasSuffix(leaf, " ") {
		return fmt.Errorf("--output filename %q is not a stable Win32 filename", leaf)
	}
	for _, character := range leaf {
		if character < 32 || strings.ContainsRune(`<>:"/\|?*`, character) || character == utf8.RuneError {
			return fmt.Errorf("--output filename %q is not a stable Win32 filename", leaf)
		}
	}
	stem := strings.TrimRight(strings.ToUpper(strings.SplitN(leaf, ".", 2)[0]), " .")
	stemRunes := []rune(stem)
	reservedPort := len(stemRunes) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		((stemRunes[3] >= '1' && stemRunes[3] <= '9') || strings.ContainsRune("¹²³", stemRunes[3]))
	if stem == "CON" || stem == "CONIN$" || stem == "CONOUT$" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		reservedPort {
		return fmt.Errorf("--output filename %q is a reserved Windows device name", leaf)
	}
	return nil
}

type windowsRawOutputRenameInformation struct {
	replaceIfExists uint8
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func renameWindowsRawOutputHandle(handle, parent windows.Handle, leaf string) error {
	buffer, err := windowsRawOutputRenameBuffer(parent, leaf)
	if err != nil {
		return err
	}
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func windowsRawOutputRenameBuffer(parent windows.Handle, leaf string) ([]byte, error) {
	leafUTF16, err := windows.UTF16FromString(leaf)
	if err != nil {
		return nil, err
	}
	nameBytes := (len(leafUTF16) - 1) * 2
	var layout windowsRawOutputRenameInformation
	bufferSize := int(unsafe.Sizeof(layout)) + nameBytes
	buffer := make([]byte, bufferSize)
	info := (*windowsRawOutputRenameInformation)(unsafe.Pointer(&buffer[0]))
	// FileRenameInformation interprets false as no replacement. Go supplies
	// the architecture-specific padding before the HANDLE field.
	info.replaceIfExists = 0
	info.rootDirectory = parent
	info.fileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.fileName[0], nameBytes/2), leafUTF16)
	return buffer, nil
}
