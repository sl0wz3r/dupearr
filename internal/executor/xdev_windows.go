//go:build windows

package executor

import (
	"errors"
	"syscall"
)

// errorNotSameDevice is ERROR_NOT_SAME_DEVICE: MoveFileEx cannot move a file to another volume.
const errorNotSameDevice syscall.Errno = 17

func isNotSameDevice(err error) bool { return errors.Is(err, errorNotSameDevice) }
