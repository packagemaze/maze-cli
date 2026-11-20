package login

import (
	"os/exec"
	"runtime"
)

// OpenBrowser asks the desktop to open url; maze prints the url too, so a failure here is not fatal.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
