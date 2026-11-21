package lifecycle

import (
	"fmt"
	"runtime"
)

func checkLinux() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("automatic installation supports Linux with systemd only; on %s install the binary manually (nothing was changed)", runtime.GOOS)
	}
	return nil
}
