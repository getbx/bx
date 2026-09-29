import Foundation
import Libbox
import Network
import NetworkExtension

// What libbox asks of the platform. Only three things matter on iOS this phase: open the tun
// (turn libbox's options into NEPacketTunnelNetworkSettings and hand back the fd), watch the
// default interface, and list interfaces. Everything else is an explicit "not supported".
// Shape follows the official sing-box Apple client (its ExtensionPlatformInterface).
final class PlatformInterface: NSObject, LibboxPlatformInterfaceProtocol, LibboxCommandServerHandlerProtocol {
    private unowned let tunnel: PacketTunnelProvider
    private var networkSettings: NEPacketTunnelNetworkSettings?
    private var monitor: NWPathMonitor?
    private var lastPath: String?

    init(_ tunnel: PacketTunnelProvider) {
        self.tunnel = tunnel
    }

    func reset() {
        networkSettings = nil
        monitor?.cancel()
        monitor = nil
        lastPath = nil
    }

    // MARK: tun

    func openTun(_ options: LibboxTunOptionsProtocol?, ret0_: UnsafeMutablePointer<Int32>?) throws {
        guard let options, let ret0_ else {
            throw TunnelError("openTun: nil options or return pointer")
        }
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        settings.mtu = NSNumber(value: options.getMTU())

        if let dnsMode = options.getDNSMode(), dnsMode.value != LibboxDNSModeDisabled {
            let it = try options.getDNSServerAddress()
            var servers: [String] = []
            while it.hasNext() { servers.append(it.next()) }
            if !servers.isEmpty {
                let dns = NEDNSSettings(servers: servers)
                dns.matchDomains = [""] // every query goes to the tun, none to the physical resolver
                settings.dnsSettings = dns
            }
        }

        var v4Addr: [String] = [], v4Mask: [String] = []
        if let it = options.getInet4Address() {
            while it.hasNext() { if let p = it.next() { v4Addr.append(p.address()); v4Mask.append(p.mask()) } }
        }
        let v4 = NEIPv4Settings(addresses: v4Addr, subnetMasks: v4Mask)
        var v4Routes: [NEIPv4Route] = []
        if let it = options.getInet4RouteAddress() {
            while it.hasNext() { if let p = it.next() { v4Routes.append(NEIPv4Route(destinationAddress: p.address(), subnetMask: p.mask())) } }
        }
        if v4Routes.isEmpty { v4Routes = [NEIPv4Route.default()] }
        var v4Excluded: [NEIPv4Route] = []
        if let it = options.getInet4RouteExcludeAddress() {
            while it.hasNext() { if let p = it.next() { v4Excluded.append(NEIPv4Route(destinationAddress: p.address(), subnetMask: p.mask())) } }
        }
        v4.includedRoutes = v4Routes
        v4.excludedRoutes = v4Excluded
        settings.ipv4Settings = v4

        var v6Addr: [String] = [], v6Len: [NSNumber] = []
        if let it = options.getInet6Address() {
            while it.hasNext() { if let p = it.next() { v6Addr.append(p.address()); v6Len.append(NSNumber(value: p.prefix())) } }
        }
        if !v6Addr.isEmpty {
            // Claiming the v6 default route matters: without it iOS sends v6 straight out the
            // physical interface. sing-box then rejects v6 (mobileconfig), matching the desktop.
            let v6 = NEIPv6Settings(addresses: v6Addr, networkPrefixLengths: v6Len)
            var v6Routes: [NEIPv6Route] = []
            if let it = options.getInet6RouteAddress() {
                while it.hasNext() { if let p = it.next() { v6Routes.append(NEIPv6Route(destinationAddress: p.address(), networkPrefixLength: NSNumber(value: p.prefix()))) } }
            }
            if v6Routes.isEmpty { v6Routes = [NEIPv6Route.default()] }
            v6.includedRoutes = v6Routes
            settings.ipv6Settings = v6
        }

        networkSettings = settings
        try runBlocking { [tunnel] in try await tunnel.setTunnelNetworkSettings(settings) }

        if let fd = tunnel.packetFlow.value(forKeyPath: "socket.fileDescriptor") as? Int32 {
            ret0_.pointee = fd
            return
        }
        let fd = LibboxGetTunnelFileDescriptor()
        guard fd != -1 else { throw TunnelError("openTun: no tun file descriptor") }
        ret0_.pointee = fd
    }

    // MARK: interfaces

    func startDefaultInterfaceMonitor(_ listener: LibboxInterfaceUpdateListenerProtocol?) throws {
        guard let listener else { return }
        let m = NWPathMonitor()
        monitor = m
        let first = DispatchSemaphore(value: 0)
        var signalled = false
        m.pathUpdateHandler = { [weak self] path in
            self?.update(listener, path)
            if !signalled { signalled = true; first.signal() }
        }
        m.start(queue: DispatchQueue.global())
        first.wait()
    }

    private func update(_ listener: LibboxInterfaceUpdateListenerProtocol, _ path: Network.NWPath) {
        let desc = "\(path.status) " + path.availableInterfaces.map { "\($0.name)#\($0.index)" }.joined(separator: ",")
        listener.updateNetworkPath(desc)
        guard desc != lastPath else { return }
        lastPath = desc
        guard path.status != .unsatisfied, let iface = path.availableInterfaces.first else {
            listener.updateDefaultInterface("", interfaceIndex: -1, isExpensive: false, isConstrained: false)
            return
        }
        listener.updateDefaultInterface(iface.name, interfaceIndex: Int32(iface.index), isExpensive: path.isExpensive, isConstrained: path.isConstrained)
    }

    func closeDefaultInterfaceMonitor(_: LibboxInterfaceUpdateListenerProtocol?) throws {
        monitor?.cancel()
        monitor = nil
        lastPath = nil
    }

    func getInterfaces() throws -> LibboxNetworkInterfaceIteratorProtocol {
        guard let monitor else { throw TunnelError("interface monitor not started") }
        let path = monitor.currentPath
        var list: [LibboxNetworkInterface] = []
        if path.status != .unsatisfied {
            for it in path.availableInterfaces {
                let n = LibboxNetworkInterface()
                n.name = it.name
                n.index = Int32(it.index)
                switch it.type {
                case .wifi: n.type = LibboxInterfaceTypeWIFI
                case .cellular: n.type = LibboxInterfaceTypeCellular
                case .wiredEthernet: n.type = LibboxInterfaceTypeEthernet
                default: n.type = LibboxInterfaceTypeOther
                }
                list.append(n)
            }
        }
        return InterfaceIterator(list)
    }

    final class InterfaceIterator: NSObject, LibboxNetworkInterfaceIteratorProtocol {
        private var it: IndexingIterator<[LibboxNetworkInterface]>
        private var nextValue: LibboxNetworkInterface?
        init(_ list: [LibboxNetworkInterface]) { it = list.makeIterator() }
        func hasNext() -> Bool { nextValue = it.next(); return nextValue != nil }
        func next() -> LibboxNetworkInterface? { nextValue }
    }

    // MARK: facts about this platform

    func underNetworkExtension() -> Bool { true }
    // Must be the truth, not a constant: sing-tun chooses its TCP stack from it. Under
    // includeAllNetworks the default "mixed" stack cannot carry TCP (sing-tun
    // ErrIncludeAllNetworks); told the truth it picks gVisor. Hard-coding false left the
    // kill-switch build with DNS working and every TCP connection dead (2026-09-29, on device).
    func includeAllNetworks() -> Bool {
        (tunnel.protocolConfiguration as? NETunnelProviderProtocol)?.includeAllNetworks ?? false
    }
    func usePlatformAutoDetectControl() -> Bool { false }
    func autoDetectControl(_: Int32) throws {}
    func useProcFS() -> Bool { false }
    func usePlatformShell() -> Bool { false }
    func usePlatformBridge() -> Bool { false }
    func localDNSTransport() -> LibboxLocalDNSTransportProtocol? { nil }
    func readWIFIState() -> LibboxWIFIState? { nil }
    func registerMyInterface(_: String?) {}
    func tailscaleHostname() -> String { "" }

    func clearDNSCache() {
        guard let networkSettings else { return }
        _ = try? runBlocking { [tunnel] in
            try await tunnel.setTunnelNetworkSettings(nil)
            try await tunnel.setTunnelNetworkSettings(networkSettings)
        }
    }

    // MARK: not supported on iOS

    private func unsupported(_ what: String) -> NSError {
        NSError(domain: "bx.PlatformInterface", code: -1, userInfo: [NSLocalizedDescriptionKey: "\(what) is not supported on iOS"])
    }

    func findConnectionOwner(_: Int32, sourceAddress _: String?, sourcePort _: Int32, destinationAddress _: String?, destinationPort _: Int32) throws -> LibboxConnectionOwner {
        throw unsupported("findConnectionOwner")
    }
    func send(_: LibboxNotification?) throws {}
    func cancelNotification(_: String?, typeID _: Int32) throws {}
    func startNeighborMonitor(_: LibboxNeighborUpdateListenerProtocol?) throws {}
    func closeNeighborMonitor(_: LibboxNeighborUpdateListenerProtocol?) throws {}
    func checkPlatformShell() throws { throw unsupported("shell") }
    func openShellSession(_: LibboxPlatformUser?, command _: String?, environ _: LibboxStringIteratorProtocol?, term _: String?, rows _: Int32, cols _: Int32) throws -> LibboxShellSessionProtocol {
        throw unsupported("shell")
    }
    func lookupUser(_: String?) throws -> LibboxPlatformUser { throw unsupported("lookupUser") }
    func lookupSFTPServer(_ error: NSErrorPointer) -> String { error?.pointee = unsupported("sftp"); return "" }
    func readSystemSSHHostKey(_ error: NSErrorPointer) -> String { error?.pointee = unsupported("ssh host key"); return "" }
    func createBridge(_: LibboxBridgeOptions?) throws -> LibboxBridgeSessionProtocol { throw unsupported("bridge") }

    // MARK: command server handler

    func serviceStop() throws { tunnel.stopFromLibbox() }
    func serviceReload() throws {}
    func getSystemProxyStatus() throws -> LibboxSystemProxyStatus { LibboxSystemProxyStatus() }
    func setSystemProxyEnabled(_: Bool) throws {}
    func triggerNativeCrash() throws {}
    func writeDebugMessage(_: String?) {}
    func connectSSHAgent(_: UnsafeMutablePointer<Int32>?) throws { throw unsupported("ssh agent") }
}
