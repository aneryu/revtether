//go:build darwin

package main

import _ "embed"

//go:embed menubar.png
var menuBarIcon []byte

func menuBarIconPNG() []byte {
	return menuBarIcon
}
