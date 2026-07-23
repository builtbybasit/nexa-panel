package main

import (
	"fmt"
	"os"
	"os/exec"
)

var uninstallScriptPath = "/usr/lib/nexa-panel/uninstall.sh"

// runUninstall delegates to the packaged lifecycle script. The child may
// remove the currently running binary safely: this process already has its
// executable mapped and waits only to relay the script's terminal result.
func runUninstall(args []string) error {
	info, err := os.Stat(uninstallScriptPath)
	if err != nil {
		return fmt.Errorf("find packaged uninstaller: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("packaged uninstaller is not an executable regular file: %s", uninstallScriptPath)
	}
	command := exec.Command(uninstallScriptPath, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("uninstall Nexa Panel: %w", err)
	}
	return nil
}
