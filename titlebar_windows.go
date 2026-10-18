package main

import (
	"runtime"
	"slices"
	"unsafe"

	"github.com/go-gui-org/go-gui/gui"
	"golang.org/x/sys/windows"
)

const (
	dwmUseImmersiveDarkMode = 20
	dwmCaptionColor         = 35
	dwmTextColor            = 36
)

type titlebarSearch struct {
	title  []uint16
	buffer []uint16
	handle uintptr
}

var (
	titlebarUser32        = windows.NewLazySystemDLL("user32.dll")
	enumThreadWindows     = titlebarUser32.NewProc("EnumThreadWindows")
	getWindowText         = titlebarUser32.NewProc("GetWindowTextW")
	dwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	titlebarCallback      = windows.NewCallback(findTitlebarWindow)
)

func styleTitlebar(window *gui.Window) {
	title, err := windows.UTF16FromString(window.Config.Title)
	if err != nil {
		return
	}

	// An extra UTF-16 slot prevents longer titles from matching after truncation.
	search := titlebarSearch{
		title:  title[:len(title)-1],
		buffer: make([]uint16, len(title)+1),
	}

	// OnInit runs on the backend's locked UI thread. Match its window by title
	// because go-gui does not expose the HWND; this also works without focus.
	enumThreadWindows.Call(uintptr(windows.GetCurrentThreadId()), titlebarCallback, uintptr(unsafe.Pointer(&search)))

	runtime.KeepAlive(&search)

	if search.handle == 0 {
		return
	}

	// Older Windows versions ignore unsupported attributes and keep their
	// native defaults. Windows 11 supports the explicit caption/text colors.
	setTitlebarAttribute(search.handle, dwmUseImmersiveDarkMode, 1)
	setTitlebarAttribute(search.handle, dwmCaptionColor, titlebarColor(colorBackground))
	setTitlebarAttribute(search.handle, dwmTextColor, titlebarColor(colorText))
}

func findTitlebarWindow(handle uintptr, search *titlebarSearch) uintptr {
	length, _, _ := getWindowText.Call(handle, uintptr(unsafe.Pointer(&search.buffer[0])), uintptr(len(search.buffer)))
	if int(length) != len(search.title) || !slices.Equal(search.buffer[:length], search.title) {
		return 1
	}

	search.handle = handle

	return 0
}

func setTitlebarAttribute(handle, attribute uintptr, value uint32) {
	dwmSetWindowAttribute.Call(handle, attribute, uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value))
}

func titlebarColor(color gui.Color) uint32 {
	// DWM expects a COLORREF (0x00BBGGRR), not an RGB integer.
	return uint32(color.R) | uint32(color.G)<<8 | uint32(color.B)<<16
}
