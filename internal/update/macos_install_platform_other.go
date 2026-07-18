//go:build !darwin

package update

func newPlatformPreparedInstall(InstallOptions, MacOSPayload) (platformPreparedInstall, error) {
	return nil, nil
}
