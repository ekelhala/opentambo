package lifecycle

import (
	"fmt"
	"runtime"
)

// hostGOOS is the runtime OS name; the seam lets tests exercise the
// non-Linux refusal on any platform. Production never reassigns it.
var hostGOOS = runtime.GOOS

func checkLinux() error {
	if hostGOOS != "linux" {
		return fmt.Errorf("automatic installation supports Linux with systemd only; on %s install the binary manually (nothing was changed)", hostGOOS)
	}
	return nil
}
