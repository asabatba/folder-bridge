//go:build windows

package app

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupValueName = "FolderBridge"

var (
	advapi         = syscall.NewLazyDLL("advapi32.dll")
	regCreateKeyEx = advapi.NewProc("RegCreateKeyExW")
	regSetValueEx  = advapi.NewProc("RegSetValueExW")
	regDeleteValue = advapi.NewProc("RegDeleteValueW")
)

func launchAtLoginCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%q --background", exe), nil
}

func openRunKey(access uint32) (syscall.Handle, error) {
	path, _ := syscall.UTF16PtrFromString(runKeyPath)
	var key syscall.Handle
	err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, access, &key)
	return key, err
}

func setAutostart(enabled bool) error {
	key, err := openRunKey(syscall.KEY_SET_VALUE)
	if err == syscall.ERROR_FILE_NOT_FOUND && enabled {
		// Some stripped-down Windows profiles do not have a Run key yet.
		path, _ := syscall.UTF16PtrFromString(runKeyPath)
		code, _, _ := regCreateKeyEx.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(path)), 0, 0, 0, uintptr(syscall.KEY_SET_VALUE), 0, uintptr(unsafe.Pointer(&key)), 0)
		if code != 0 {
			return fmt.Errorf("create HKCU Run key: %w", syscall.Errno(code))
		}
		err = nil
	}
	if err == syscall.ERROR_FILE_NOT_FOUND && !enabled {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open HKCU Run key: %w", err)
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString(startupValueName)
	if !enabled {
		code, _, _ := regDeleteValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)))
		if code != 0 && syscall.Errno(code) != syscall.ERROR_FILE_NOT_FOUND {
			return fmt.Errorf("disable launch at login: %w", syscall.Errno(code))
		}
		return nil
	}
	command, err := launchAtLoginCommand()
	if err != nil {
		return err
	}
	value, err := syscall.UTF16FromString(command)
	if err != nil {
		return err
	}
	code, _, _ := regSetValueEx.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, syscall.REG_SZ,
		uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)*2))
	if code != 0 {
		return fmt.Errorf("enable launch at login: %w", syscall.Errno(code))
	}
	return nil
}

func autostartEnabled() bool {
	key, err := openRunKey(syscall.KEY_QUERY_VALUE)
	if err != nil {
		return false
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString(startupValueName)
	var value [2048]uint16
	size := uint32(len(value) * 2)
	var valueType uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &valueType, (*byte)(unsafe.Pointer(&value[0])), &size); err != nil || valueType != syscall.REG_SZ {
		return false
	}
	expected, err := launchAtLoginCommand()
	return err == nil && syscall.UTF16ToString(value[:size/2]) == expected
}
