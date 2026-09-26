package main

import (
	"os"

	"github.com/crazyuploader/hostglance/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
