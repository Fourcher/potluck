//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
)

func openUncached(path string, flag int) (*os.File, error) {
	return nil, errors.New("uncached reads are not supported here")
}
