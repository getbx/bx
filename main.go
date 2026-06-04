package main

import (
	"log"
	"os"

	"github.com/getbx/bx/internal/cli"
)

func main() {
	if err := cli.New().Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
