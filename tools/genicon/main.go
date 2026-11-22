// Command genicon renders the application icon, internal/icons/app/icon.svg,
// with Inkscape into the PNG files that are embedded in the program
// (internal/icons/app) and that go-winres puts in the executable and
// release.sh packs into the installer icon (winres).
//
// Run from the repository root (needs inkscape):
//
//	go run ./tools/genicon
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

const svg = "internal/icons/app/icon.svg"

func main() {
	for _, out := range []struct {
		size   int
		winres string
	}{
		{256, "winres/icon.png"},
		{48, "winres/icon48.png"},
		{32, "winres/icon32.png"},
		{16, "winres/icon16.png"},
	} {
		file := fmt.Sprintf("internal/icons/app/icon%d.png", out.size)
		size := fmt.Sprint(out.size)
		cmd := exec.Command("inkscape", svg, "--export-type=png", "--export-filename="+file, "-w", size, "-h", size)
		if b, err := cmd.CombinedOutput(); err != nil {
			log.Fatalf("inkscape: %v\n%s", err, b)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(out.winres, data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
