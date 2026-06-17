//go:build !linux

package procredact

func RedactArg(pid int, secret string) error {
	return nil
}
