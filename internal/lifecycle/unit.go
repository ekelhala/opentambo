package lifecycle

import (
	"fmt"
)

// unitTemplate is the generated service unit: no personal usernames or
// developer home paths, bounded graceful shutdown with cgroup cleanup, and
// journald logging. Hardening is limited to settings compatible with GPU
// device access, mounted model trees and the native runtime (issue #16).
const unitTemplate = `[Unit]
Description=OpenTambo - llama.cpp process manager and persistent KV-cache proxy
After=network-online.target
Wants=network-online.target
%s[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
ExecStartPre=%s
ExecStart=%s
Restart=on-failure
RestartSec=3
TimeoutStopSec=15
KillMode=control-group
UMask=0027
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=%s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

// renderUnit produces the unit file contents for the layout in c.
func renderUnit(c Config) string {
	mountLines := ""
	guard := ""
	if c.MountGuard != "" {
		mountLines = fmt.Sprintf("RequiresMountsFor=%s\n", c.MountGuard)
		guard = fmt.Sprintf("mountpoint -q %s && ", c.MountGuard)
	}
	// Preflight the operator configuration, runtime, model tree and
	// writable state directory before startup so a missing mount or wrong
	// permissions is a clear unit failure instead of a manager writing
	// beside an absent mount.
	prestart := fmt.Sprintf("/bin/sh -c '%stest -r %s && test -x %s && test -d %s && test -w %s'",
		guard, c.ConfigPath(), c.LlamaServer, c.ModelsDir, c.StateDir)
	// The operator configuration file (issue #17) is the single source of
	// runtime intent: the installer seeds it with this layout, edits to it
	// take effect on restart, and the service never rewrites it. A missing
	// explicitly selected file fails startup loudly on purpose.
	execStart := fmt.Sprintf("%s --config %s", c.BinPath, c.ConfigPath())
	return fmt.Sprintf(unitTemplate, mountLines, c.User, c.Group, c.StateDir, prestart, execStart, c.StateDir)
}
