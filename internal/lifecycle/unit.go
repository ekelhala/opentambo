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
	// Preflight the runtime, model tree and writable state directory
	// before startup so a missing mount or wrong permissions is a clear
	// unit failure instead of a manager writing beside an absent mount.
	prestart := fmt.Sprintf("/bin/sh -c '%stest -x %s && test -d %s && test -w %s'",
		guard, c.LlamaServer, c.ModelsDir, c.StateDir)
	execStart := fmt.Sprintf("%s --models %s --data %s --llama-server %s --listen %s",
		c.BinPath, c.ModelsDir, c.StateDir, c.LlamaServer, c.Listen)
	return fmt.Sprintf(unitTemplate, mountLines, c.User, c.Group, c.StateDir, prestart, execStart, c.StateDir)
}
