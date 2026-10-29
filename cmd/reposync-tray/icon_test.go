package main

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Set ICON_OUT=<dir> to write the icons out for a look.
func TestIcons(t *testing.T) {
	for name, c := range map[string]color.RGBA{"green": green, "blue": blue, "orange": orange, "grey": grey} {
		data := makeIcon(c)
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dir := os.Getenv("ICON_OUT"); dir != "" {
			os.WriteFile(filepath.Join(dir, "icon-"+name+".png"), data, 0o644)
		}
		if ico := wrapICO(data, 32); len(ico) != len(data)+22 || ico[2] != 1 {
			t.Fatalf("%s: bad ico header", name)
		}
	}
}
