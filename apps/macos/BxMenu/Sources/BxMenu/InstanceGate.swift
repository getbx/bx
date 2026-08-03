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
      <key>StandardOutPath</key>
      <string>\(logDirectory)/menu.log</string>
      <key>StandardErrorPath</key>
      <string>\(logDirectory)/menu.err.log</string>
    </dict>
    </plist>

    """
}
