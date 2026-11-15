package main

import (
	"fmt"
	"os"

	"github.com/cryptofyre/transarc/internal/cli"
)

func main() {
	if err := cli.New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
