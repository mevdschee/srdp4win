//go:build windows

package ui

import (
	"slices"
	"syscall"

	"github.com/mevdschee/srdp4win/internal/icons"
)

var shell32 = syscall.NewLazyDLL("shell32.dll")

// hasIcon reports whether a Fluent icon with this name is embedded.
func hasIcon(name string) bool { return slices.Contains(icons.Names(), name) }

// pickIcon returns the first of the names that is an embedded icon, or the
// last name when none is (give a name that always exists last).
func pickIcon(names ...string) string {
	have := icons.Names()
	for _, n := range names {
		if slices.Contains(have, n) {
			return n
		}
	}
	return names[len(names)-1]
}
