//go:build darwin

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const rawOutputDarwinOSearch = 0x40000000 | unix.O_DIRECTORY

const darwinNoACL = ^uint32(0)

// x/sys intentionally deprecates its Darwin syscall constants without
// exposing an fgetattrlist wrapper. Darwin keeps fgetattrlist at syscall 228
// on both supported architectures.
const darwinSysFgetattrlist = 228

type darwinRawOutputAttrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volumeAttr  uint32
	directory   uint32
	file        uint32
	fork        uint32
}

type darwinRawOutputAttrReference struct {
	dataOffset int32
	length     uint32
}

type darwinRawOutputFileSecurityHeader struct {
	magic      uint32
	owner      [16]byte
	group      [16]byte
	entryCount uint32
	flags      uint32
}

func rawOutputParentOpenFlags() int {
	return rawOutputDarwinOSearch | unix.O_CLOEXEC
}

func validateUnixRawOutputParentPlatform(parentFD int) error {
	attributes := darwinRawOutputAttrList{
		bitmapCount: 5,
		commonAttr:  unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	buffer := make([]byte, 8192)
	// FSOPT_ATTR_CMN_EXTENDED is only for GEN_COUNT, DOCUMENT_ID, and
	// fork-field extended attributes. ATTR_CMN_EXTENDED_SECURITY is a normal
	// common attribute and is exercised here by the native ACL tests.
	_, _, errno := unix.Syscall6(
		darwinSysFgetattrlist,
		uintptr(parentFD),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("inspect --output parent ACL: %w", errno)
	}
	if len(buffer) < 4+int(unsafe.Sizeof(darwinRawOutputAttrReference{})) {
		return fmt.Errorf("inspect --output parent ACL: invalid attribute response")
	}
	reference := (*darwinRawOutputAttrReference)(unsafe.Pointer(&buffer[4]))
	if reference.length == 0 {
		return nil
	}
	dataStart := 4 + int(reference.dataOffset)
	headerSize := int(unsafe.Sizeof(darwinRawOutputFileSecurityHeader{}))
	if dataStart < 0 || dataStart+headerSize > len(buffer) || int(reference.length) < headerSize {
		return fmt.Errorf("inspect --output parent ACL: invalid security response")
	}
	header := (*darwinRawOutputFileSecurityHeader)(unsafe.Pointer(&buffer[dataStart]))
	if header.entryCount != darwinNoACL {
		return &rawOutputValidationError{err: fmt.Errorf("--output parent must not have an extended ACL on macOS")}
	}
	return nil
}

func publishStagedRawOutputAt(parentFD int, stagedLeaf, targetLeaf string) error {
	return unix.RenameatxNp(parentFD, stagedLeaf, parentFD, targetLeaf, unix.RENAME_EXCL)
}
