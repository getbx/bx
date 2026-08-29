package guardian

// darwin:真的接管系统 DNS(networksetup 那套,经 install 包)。
func newPlatformDNSManager(service string) DNSManager {
	return NewDNSManager(service)
}
