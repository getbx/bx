package bxdeploy

import "os/exec"

func runHook(cmd string) ([]byte, error) { return exec.Command("sh", "-c", cmd).CombinedOutput() }
