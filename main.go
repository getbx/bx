package main

import (
	"log"
	"os"

	"github.com/getbx/bx/internal/cli"
	"github.com/getbx/bx/internal/sshpass"
)

func main() {
	// ssh runs bx as its SSH_ASKPASS program during `bx server deploy --password-stdin`:
	// answer from the deploy process and exit before any command parsing (internal/sshpass).
	if sshpass.IsAskpass() {
		os.Exit(sshpass.RunAskpass(os.Args))
	}
	if err := cli.New().Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
