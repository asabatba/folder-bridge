//go:build windows

package app

import (
	"encoding/binary"
	"syscall"
	"testing"
	"unsafe"

	"folderbridge/internal/syncengine"
)

func TestTrayIconStateSelection(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  int
	}{
		{"Paused", trayPaused}, {"Error — will retry", trayError}, {"Partial sync — will retry", trayError},
		{"Stopped", trayNormal}, {"Synchronizing session", trayNormal}, {"Up to date with reachable members", trayNormal},
		{"Waiting for 1 member(s); reachable members reconciled", trayNormal},
	} {
		if got := trayIconVariant(tc.state); got != tc.want {
			t.Errorf("state %q: got %d, want %d", tc.state, got, tc.want)
		}
	}
}

func TestEmbeddedTrayIconFrames(t *testing.T) {
	for _, ico := range [][]byte{trayNormalICO, trayPausedICO, trayErrorICO} {
		if count := binary.LittleEndian.Uint16(ico[4:6]); count != 6 {
			t.Fatalf("want six DPI frames, got %d", count)
		}
		for _, tc := range []struct{ requested, selected int }{{16, 16}, {18, 20}, {20, 20}, {24, 24}, {32, 32}, {40, 48}, {48, 48}, {64, 64}, {96, 64}} {
			frame, err := trayIconFrame(ico, tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			// A 32-bit ICO DIB has doubled height (color image plus AND mask).
			if len(frame) < 40 || binary.LittleEndian.Uint32(frame[:4]) != 40 || int(binary.LittleEndian.Uint32(frame[4:8])) != tc.selected || int(binary.LittleEndian.Uint32(frame[8:12])) != 2*tc.selected || binary.LittleEndian.Uint16(frame[14:16]) != 32 {
				t.Fatalf("invalid %dpx selected frame for %dpx", tc.selected, tc.requested)
			}
		}
	}
	for _, bad := range [][]byte{nil, {}, make([]byte, 6), trayNormalICO[:10], append([]byte{1}, trayNormalICO[1:]...)} {
		if _, err := trayIconFrame(bad, 16); err == nil {
			t.Error("invalid ICO accepted")
		}
	}
	damaged := append([]byte(nil), trayNormalICO...)
	binary.LittleEndian.PutUint32(damaged[6+12:6+16], ^uint32(0))
	if _, err := trayIconFrame(damaged, 16); err == nil {
		t.Error("out-of-bounds frame accepted")
	}
	if _, err := trayIconFrame(trayNormalICO, 0); err == nil {
		t.Error("zero size accepted")
	}
}

func TestNativeTrayIconsLoadAtScaledSizesAndRelease(t *testing.T) {
	getInfo := trayUser32.NewProc("GetIconInfo")
	gdi := syscall.NewLazyDLL("gdi32.dll")
	getObject := gdi.NewProc("GetObjectW")
	deleteObject := gdi.NewProc("DeleteObject")
	type iconInfo struct {
		IsIcon      uint32
		HotX, HotY  uint32
		Mask, Color uintptr
	}
	type bitmap struct {
		Type, Width, Height, WidthBytes int32
		Planes, BitsPixel               uint16
		Bits                            uintptr
	}
	for _, size := range []int{16, 20, 24, 32, 48, 64} {
		icons, err := loadTrayIcons(size)
		if err != nil {
			t.Fatal(err)
		}
		owned := icons
		func() {
			defer icons.close()
			for variant, handle := range icons {
				var info iconInfo
				if ok, _, err := getInfo.Call(handle, uintptr(unsafe.Pointer(&info))); ok == 0 {
					t.Fatalf("icon %d at %dpx is not a native HICON: %v", variant, size, err)
				}
				func() {
					defer deleteObject.Call(info.Mask)
					defer deleteObject.Call(info.Color)
					var image bitmap
					if ok, _, err := getObject.Call(info.Color, unsafe.Sizeof(image), uintptr(unsafe.Pointer(&image))); ok == 0 {
						t.Fatal(err)
					}
					if int(image.Width) != size || int(image.Height) != size || image.BitsPixel != 32 {
						t.Fatalf("wrong native icon dimensions/format: %+v", image)
					}
				}()
			}
		}()
		if icons != (trayIcons{}) {
			t.Fatal("icon handles retained after cleanup")
		}
		for _, handle := range owned {
			var info iconInfo
			if ok, _, _ := getInfo.Call(handle, uintptr(unsafe.Pointer(&info))); ok != 0 {
				deleteObject.Call(info.Mask)
				deleteObject.Call(info.Color)
				t.Fatal("native HICON still alive after cleanup")
			}
		}
		icons.close() // Cleanup is idempotent; shared stock handles never enter this set.
	}
}

func TestTrayIconDataUsesCachedArtworkAndState(t *testing.T) {
	icons, err := loadTrayIcons(16)
	if err != nil {
		t.Fatal(err)
	}
	defer icons.close()
	engine := syncengine.New(t.TempDir(), nil)
	tray := &windowsTray{app: &application{engine: engine}, icons: icons}
	for i := 0; i < 10; i++ {
		if got := tray.iconData().Icon; got != icons[trayNormal] {
			t.Fatal("refresh did not reuse normal artwork", got)
		}
	}
	if err := engine.Start(syncengine.Options{Mode: "session", Enabled: true, Paused: true}); err != nil {
		t.Fatal(err)
	}
	if got := tray.iconData().Icon; got != icons[trayPaused] {
		t.Fatal("pause badge not selected")
	}
	engine.ReportError("test error")
	if got := tray.iconData().Icon; got != icons[trayError] {
		t.Fatal("error badge not selected")
	}
}
