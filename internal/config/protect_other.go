//go:build !windows

package config

import (
	"bytes"
	"errors"
)

// There is no DPAPI outside Windows. The app only runs on Windows; this
// stand-in keeps the package and its tests working elsewhere. It hides the
// content from a casual look and protects nothing.

var standInMagic = []byte("srdp4win-not-encrypted\n")

const standInKey = 0x5a

func protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("nothing to encrypt")
	}
	out := append([]byte(nil), standInMagic...)
	for _, b := range plain {
		out = append(out, b^standInKey)
	}
	return out, nil
}

func unprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data file")
	}
	if !bytes.HasPrefix(data, standInMagic) {
		return nil, errors.New("not a connections file")
	}
	data = data[len(standInMagic):]
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ standInKey
	}
	return out, nil
}
