package main

import (
	"os"

	"github.com/ZZH1124Prime/ZaH/internal/bootstrap"
)

func main() {
	os.Exit(bootstrap.RunCLI(os.Args[1:], os.Stdout, os.Stderr))
}
