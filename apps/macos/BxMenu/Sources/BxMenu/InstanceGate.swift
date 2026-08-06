import Foundation

enum InstanceDecision: Equatable {
    case keepSelf(terminatePeer: Bool)
    case yieldToPeer
}

func resolveInstanceConflict(selfPath: String, peerPath: String?, canonicalPath: String) -> InstanceDecision {
    guard let peerPath else { return .keepSelf(terminatePeer: false) }
    if selfPath == canonicalPath { return .keepSelf(terminatePeer: true) }
    if peerPath == canonicalPath { return .yieldToPeer }
    return .yieldToPeer
}

// menuLaunchAgentPlist renders the menu-bar LaunchAgent plist. It is the
// fourth generator of this same file — the others live in
// internal/install/unified_darwin.go (MenuAgentPlistText),
// scripts/install-macos-menu.sh and scripts/package-macos-menu.sh — and it is
// the one that runs most often: ensureLoginItemIfCanonical() compares the
// on-disk plist against this text on every menu launch and rewrites the file
// when they differ. Any key missing here is therefore silently stripped from
// a correctly installed plist, so all four must stay byte-for-byte identical.
// In particular KeepAlive={SuccessfulExit:false} must be present (crash =>
// relaunch, deliberate Quit bx => stays quit); KeepAlive=true would make
// Quit bx silently fail.
func menuLaunchAgentPlist(executablePath: String, logDirectory: String) -> String {
    """
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
      <key>Label</key>
      <string>com.getbx.bx.menu</string>
      <key>ProgramArguments</key>
      <array>
        <string>\(executablePath)</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>KeepAlive</key>
      <dict>
        <key>SuccessfulExit</key>
        <false/>
      </dict>
      <key>StandardOutPath</key>
      <string>\(logDirectory)/menu.log</string>
      <key>StandardErrorPath</key>
      <string>\(logDirectory)/menu.err.log</string>
    </dict>
    </plist>

    """
}
