//go:build windows

package app

// Native Win32 notification-area integration with embedded tray artwork. No UI
// framework or WebView is needed. Window messages run on one locked OS thread.
import (
	"log"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmTimer           = 0x0113
	wmContextMenu     = 0x007B
	wmLButtonDblClk   = 0x0203
	wmRButtonUp       = 0x0205
	wmAppTray         = 0x8001
	wmAppNotification = 0x8002
	wmAppExit         = 0x8003
	wmAppRefresh      = 0x8004
	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nifMessage        = 1
	nifIcon           = 2
	nifTip            = 4
	nifInfo           = 16
	niifInfo          = 1
	niifWarning       = 2
	mfSeparator       = 0x800
	mfGray            = 0x001
	tpmReturnCommand  = 0x100
	tpmRightButton    = 0x002
	menuDashboard     = 2001
	menuFolder        = 2002
	menuPause         = 2003
	menuSync          = 2004
	menuAutostart     = 2005
	menuQuit          = 2006
)

var (
	trayUser32                 = syscall.NewLazyDLL("user32.dll")
	trayShell32                = syscall.NewLazyDLL("shell32.dll")
	trayKernel32               = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW       = trayUser32.NewProc("RegisterClassExW")
	procCreateWindowExW        = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProcW         = trayUser32.NewProc("DefWindowProcW")
	procDestroyWindow          = trayUser32.NewProc("DestroyWindow")
	procGetMessageW            = trayUser32.NewProc("GetMessageW")
	procTranslateMessage       = trayUser32.NewProc("TranslateMessage")
	procDispatchMessageW       = trayUser32.NewProc("DispatchMessageW")
	procPostQuitMessage        = trayUser32.NewProc("PostQuitMessage")
	procPostMessageW           = trayUser32.NewProc("PostMessageW")
	procRegisterWindowMessageW = trayUser32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu        = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenuW            = trayUser32.NewProc("AppendMenuW")
	procDestroyMenu            = trayUser32.NewProc("DestroyMenu")
	procSetForegroundWindow    = trayUser32.NewProc("SetForegroundWindow")
	procTrackPopupMenu         = trayUser32.NewProc("TrackPopupMenu")
	procGetCursorPos           = trayUser32.NewProc("GetCursorPos")
	procLoadIconW              = trayUser32.NewProc("LoadIconW")
	procSetTimer               = trayUser32.NewProc("SetTimer")
	procKillTimer              = trayUser32.NewProc("KillTimer")
	procGetModuleHandleW       = trayKernel32.NewProc("GetModuleHandleW")
	procShellNotifyIconW       = trayShell32.NewProc("Shell_NotifyIconW")
	trayCallback               = syscall.NewCallback(trayWindowProc)
	activeTray                 atomic.Pointer[windowsTray]
)

type trayWindowClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ExtraClass, ExtraWindow            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}

type trayPoint struct{ X, Y int32 }
type trayMsg struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             trayPoint
	Private        uint32
}

type notifyIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	TimeoutOrVersion    uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	Guid                [16]byte
	BalloonIcon         uintptr
}

type trayNotification struct {
	title, message string
	warning        bool
}

type windowsTray struct {
	app            *application
	hwnd           atomic.Uintptr
	notifications  chan trayNotification
	done           chan struct{}
	taskbarCreated uint32
	lastState      string
	icons          trayIcons
}

func startTray(app *application) func() {
	t := &windowsTray{app: app, notifications: make(chan trayNotification, 32), done: make(chan struct{})}
	app.setNotifier(func(title, message string, warning bool) {
		select {
		case t.notifications <- trayNotification{title, message, warning}:
		default:
		}
		if hwnd := t.hwnd.Load(); hwnd != 0 {
			procPostMessageW.Call(hwnd, wmAppNotification, 0, 0)
		}
	})
	go t.run()
	return func() {
		app.setNotifier(nil)
		if hwnd := t.hwnd.Load(); hwnd != 0 {
			procPostMessageW.Call(hwnd, wmAppExit, 0, 0)
		}
		<-t.done
	}
}

func (t *windowsTray) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)
	// Load once, not on every three-second state refresh. The system metric
	// follows Windows' small-icon scaling; ICO frames cover common DPI sizes.
	size, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
	if size == 0 {
		size = 16
	}
	var err error
	t.icons, err = loadTrayIcons(int(size))
	if err != nil {
		log.Printf("tray: custom icons unavailable, using Windows fallback: %v", err)
	}
	defer t.icons.close()
	className, _ := syscall.UTF16PtrFromString("FolderBridgeTrayV31")
	hinstance, _, _ := procGetModuleHandleW.Call(0)
	wc := trayWindowClass{WndProc: trayCallback, Instance: hinstance, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		log.Printf("tray: class registration failed: %v", callErr)
		return
	}
	activeTray.Store(t)
	defer activeTray.CompareAndSwap(t, nil)
	hwnd, _, callErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0, 0, hinstance, 0)
	if hwnd == 0 {
		log.Printf("tray: window creation failed: %v", callErr)
		return
	}
	t.hwnd.Store(hwnd)
	defer t.hwnd.Store(0)
	text, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	id, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(text)))
	t.taskbarCreated = uint32(id)
	if !t.updateIcon(nimAdd) {
		log.Print("tray: could not add notification area icon")
	}
	procSetTimer.Call(hwnd, 1, 3000, 0)
	// Catch notifications enqueued before HWND was initialized.
	procPostMessageW.Call(hwnd, wmAppNotification, 0, 0)
	var msg trayMsg
	for {
		rc, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(rc) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func trayWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	t := activeTray.Load()
	if t != nil && hwnd == t.hwnd.Load() {
		if uint32(message) == t.taskbarCreated && t.taskbarCreated != 0 {
			t.updateIcon(nimAdd)
			return 0
		}
		switch message {
		case wmAppTray:
			switch lParam {
			case wmRButtonUp:
				t.showMenu()
			case wmLButtonDblClk:
				openBrowser()
			}
			return 0
		case wmContextMenu:
			t.showMenu()
			return 0
		case wmAppNotification:
			t.drainNotifications()
			return 0
		case wmAppRefresh, wmTimer:
			t.updateIcon(nimModify)
			return 0
		case wmAppExit, wmClose:
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procKillTimer.Call(hwnd, 1)
			t.updateIcon(nimDelete)
			procPostQuitMessage.Call(0)
			return 0
		}
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return result
}

func copyUTF16(dst []uint16, value string) {
	// Ensure Windows NUL termination, truncating on UTF16 code-unit boundary.
	units := syscall.StringToUTF16(value)
	if len(units) > len(dst) {
		units = units[:len(dst)]
		units[len(units)-1] = 0
	}
	copy(dst, units)
}

func (t *windowsTray) iconData() notifyIconData {
	// Obtain the latest synchronization state without a registry query.
	state := t.app.engine.Snapshot().State
	variant := trayIconVariant(state)
	hicon := t.icons[variant]
	if hicon == 0 {
		// Stock shared icons are a last-resort fallback and must not be destroyed.
		iconID := [...]uintptr{32512, 32515, 32513}[variant]
		hicon, _, _ = procLoadIconW.Call(0, iconID)
	}
	data := notifyIconData{Window: t.hwnd.Load(), ID: 1, Flags: nifIcon | nifTip | nifMessage, Callback: wmAppTray, Icon: hicon}
	data.Size = uint32(unsafe.Sizeof(data))
	copyUTF16(data.Tip[:], "FolderBridge — "+state)
	return data
}

func (t *windowsTray) updateIcon(action uintptr) bool {
	data := t.iconData()
	ok, _, _ := procShellNotifyIconW.Call(action, uintptr(unsafe.Pointer(&data)))
	return ok != 0
}

func (t *windowsTray) drainNotifications() {
	for {
		select {
		case n := <-t.notifications:
			data := t.iconData()
			data.Flags = nifInfo
			data.InfoFlags = niifInfo
			if n.warning {
				data.InfoFlags = niifWarning
			}
			copyUTF16(data.InfoTitle[:], n.title)
			copyUTF16(data.Info[:], n.message)
			procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
		default:
			return
		}
	}
}

func (t *windowsTray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	t.app.mu.RLock()
	cfg := t.app.cfg
	t.app.mu.RUnlock()
	appendItem := func(flags uintptr, id uintptr, label string) {
		p, _ := syscall.UTF16PtrFromString(label)
		procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(p)))
	}
	appendItem(0, menuDashboard, "Open dashboard")
	folderFlags := uintptr(0)
	if cfg.Folder == "" {
		folderFlags = mfGray
	}
	appendItem(folderFlags, menuFolder, "Open synced folder")
	appendItem(mfSeparator, 0, "")
	pausedLabel := "Pause synchronization"
	if cfg.Paused {
		pausedLabel = "Resume synchronization"
	}
	pauseFlags := uintptr(0)
	if !cfg.Enabled || cfg.Mode != "session" {
		pauseFlags = mfGray
	}
	appendItem(pauseFlags, menuPause, pausedLabel)
	syncFlags := uintptr(0)
	if cfg.Paused || !cfg.Enabled || cfg.Mode != "session" {
		syncFlags = mfGray
	}
	appendItem(syncFlags, menuSync, "Sync now")
	startupLabel := "Launch at login: off"
	if autostartEnabled() {
		startupLabel = "Launch at login: on"
	}
	appendItem(0, menuAutostart, startupLabel)
	appendItem(mfSeparator, 0, "")
	appendItem(0, menuQuit, "Quit FolderBridge")
	var p trayPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	procSetForegroundWindow.Call(t.hwnd.Load())
	choice, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCommand|tpmRightButton, uintptr(p.X), uintptr(p.Y), 0, t.hwnd.Load(), 0)
	procPostMessageW.Call(t.hwnd.Load(), 0, 0, 0)
	switch choice {
	case menuDashboard:
		openBrowser()
	case menuFolder:
		if cfg.Folder != "" {
			if err := exec.Command("explorer.exe", cfg.Folder).Start(); err != nil {
				t.app.notify("FolderBridge", err.Error(), true)
			}
		}
	case menuPause:
		go func() {
			if err := t.app.setPaused(!cfg.Paused); err != nil {
				t.app.notify("FolderBridge", err.Error(), true)
			}
			procPostMessageW.Call(t.hwnd.Load(), wmAppRefresh, 0, 0)
		}()
	case menuSync:
		t.app.requestSync()
	case menuAutostart:
		go func() {
			if err := setAutostart(!autostartEnabled()); err != nil {
				t.app.notify("FolderBridge", err.Error(), true)
			}
		}()
	case menuQuit:
		t.app.quitOnce.Do(func() { close(t.app.quit) })
	}
}
