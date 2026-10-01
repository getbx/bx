package deploy

import "testing"

func TestDeployFailuresBecomePlainCategories(t *testing.T) {
	for text, want := range map[string]string{
		// The iPhone's Go ssh client words things differently from OpenSSH.
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain": "auth_failed",
		"dial tcp 203.0.113.9:22: i/o timeout":                                                          "unreachable",
		"dial tcp: lookup nope.example: no such host":                                                   "unreachable",
		"dial tcp 203.0.113.9:22: connect: connection refused":                                          "unreachable",
		"ssh: connect to host 203.0.113.9 port 22: Operation timed out":                                 "unreachable",
		"ssh: connect to host 203.0.113.9 port 22: Connection refused":                                  "unreachable",
		"ssh: Could not resolve hostname nope.example: nodename nor servname":                           "unreachable",
		"root@203.0.113.9: Permission denied (publickey,password).":                                     "auth_failed",
		"@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @\nHost key verification failed.":           "host_key_changed",
		"WARNING: Your password has expired.\nPassword change required but no TTY available.":           "password_change_required",
		"sudo: a password is required":                                                                  "sudo_password",
		"sudo: a terminal is required to read the password":                                             "sudo_password",
		"the remote architecture was not recognized (uname -m says \"mips\")":                           "unsupported_system",
		"the remote verification failed: bx: checksum mismatch":                                         "checksum",
		"the remote installation failed: exit status 1":                                                 "install_failed",
		"port 443 on the server is already used by nginx; nothing was changed":                          "port_in_use",
		"this server already has bx, but its configuration could not be read (reinstall to start over)": "already_installed",
	} {
		if got := Classify(text); got != want {
			t.Errorf("%q → %q, want %q", text, got, want)
		}
	}
}
