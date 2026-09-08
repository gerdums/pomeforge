//go:build !linux

package main

import "os"

func quickstartIsTerminal(*os.File) bool { return false }
