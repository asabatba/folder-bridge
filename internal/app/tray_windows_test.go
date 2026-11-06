//go:build windows

package app

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestWin32TrayStructLayout(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("x64 ABI layout check")
	}
	if got := unsafe.Sizeof(trayWindowClass{}); got != 80 {
		t.Fatalf("WNDCLASSEXW layout = %d, want 80", got)
	}
	if got := unsafe.Sizeof(trayMsg{}); got != 48 {
		t.Fatalf("MSG layout = %d, want 48", got)
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != 976 {
		t.Fatalf("NOTIFYICONDATAW layout = %d, want 976", got)
	}
}

func TestAutostartUsesBackgroundAndQuotedExecutable(t *testing.T) {
	command, err := launchAtLoginCommand()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, `"`) || !strings.HasSuffix(command, `" --background`) {
		t.Fatalf("unsafe startup command: %q", command)
	}
}
