//go:build !unix

package fileid

import (
	"errors"
	"os"
)

var errNoInodes = errors.New("device and inode numbers are not available on this platform")

func fstatFile(*os.File) (Stat, error) { return Stat{}, errNoInodes }

func statPath(string) (Stat, error) { return Stat{}, errNoInodes }
