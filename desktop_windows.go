//go:build windows && desktopapp

package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const webView2RuntimeGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

var (
	errWebView2Missing = errors.New("Tokenizer needs the Microsoft Edge WebView2 Runtime, which is not installed on this PC.\n\nInstall it from:\nhttps://developer.microsoft.com/microsoft-edge/webview2/\n\nThen start Tokenizer again.")
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")

	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaUseImmersiveDarkMode = 20
	dwmwaCaptionColor         = 35
	dwmwaTextColor            = 36
)

// runDesktopApplication opens the native window and serves the UI to it from a
// private loopback listener. It blocks until the window is closed.
func runDesktopApplication(debug, dev bool) error {
	baseDir, err := appDataDir()
	if err != nil {
		return err
	}

	unlock, alreadyRunning, err := acquireInstanceLock(baseDir)
	if err != nil {
		return err
	}
	if alreadyRunning {
		focusExistingWindow()
		return nil
	}
	defer unlock()

	logFile, err := setupDesktopLogging(baseDir)
	if err != nil {
		return err
	}
	defer logFile.Close()

	log.Printf("Tokenizer desktop starting (debug=%t, dev=%t)", debug, dev)

	if !webView2Installed() {
		return errWebView2Missing
	}

	_, handler, err := newApplication(dev)
	if err != nil {
		return err
	}

	srv, appURL, err := startLoopbackServer(handler)
	if err != nil {
		return err
	}
	log.Printf("Internal server listening on %s", appURL)
	defer shutdownServer(srv)

	dataPath := filepath.Join(baseDir, "WebView2")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return fmt.Errorf("create WebView2 data directory: %w", err)
	}

	scale := systemDPIScale()
	screenWidth, _, _ := procGetSystemMetrics.Call(0)
	screenHeight, _, _ := procGetSystemMetrics.Call(1)
	log.Printf("DPI scale=%.2f, screen metrics=%dx%d", scale, screenWidth, screenHeight)
	width, height := initialWindowSize(scale)
	log.Printf("Initial window size=%dx%d", width, height)
	view := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     debug,
		AutoFocus: true,
		DataPath:  dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  "Tokenizer",
			Width:  width,
			Height: height,
			// rsrc assigns the manifest ID 1 and the first icon group ID 2
			// when generated with both -manifest and -ico (see build.ps1).
			IconId: 2,
			Center: true,
		},
	})
	if view == nil {
		return fmt.Errorf("could not create the desktop window. The WebView2 runtime may be damaged.\n\nDetails: %s", filepath.Join(baseDir, "tokenizer.log"))
	}
	defer view.Destroy()

	windowDPI := getWindowDPI(view.Window())
	log.Printf("Window DPI=%d (%.2fx)", windowDPI, float64(windowDPI)/96)
	applyDarkTitleBar(view.Window())

	if err := view.Bind("openExternal", func(rawURL string) {
		if err := openExternalURL(rawURL); err != nil {
			log.Printf("open external URL: %v", err)
		}
	}); err != nil {
		log.Printf("bind openExternal: %v", err)
	}

	view.SetSize(int(860*scale), int(620*scale), webview2.HintMin)
	view.Navigate(appURL)
	view.Run()

	log.Print("Desktop window closed")
	return nil
}

func appDataDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA is not set; cannot store application data")
	}
	dir := filepath.Join(base, "Tokenizer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create application data directory: %w", err)
	}
	return dir, nil
}

func setupDesktopLogging(baseDir string) (*os.File, error) {
	path := filepath.Join(baseDir, "tokenizer.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	log.SetOutput(file)
	return file, nil
}

func webView2Installed() bool {
	checks := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2RuntimeGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2RuntimeGUID},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + webView2RuntimeGUID},
	}
	for _, check := range checks {
		key, err := registry.OpenKey(check.root, check.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		version, _, err := key.GetStringValue("pv")
		_ = key.Close()
		if err == nil && version != "" && version != "0.0.0.0" {
			return true
		}
	}
	return false
}

// acquireInstanceLock keeps a handle open for the life of the process. Windows
// refuses a second exclusive open, which prevents two windows from sharing the
// same WebView2 data directory.
func acquireInstanceLock(baseDir string) (unlock func(), alreadyRunning bool, err error) {
	path := filepath.Join(baseDir, "instance.lock")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}

	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // no sharing
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("open instance lock: %w", err)
	}

	return func() { _ = windows.CloseHandle(handle) }, false, nil
}

func focusExistingWindow() {
	title, err := windows.UTF16PtrFromString("Tokenizer")
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, windows.SW_RESTORE)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

func systemDPIScale() float64 {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

func getWindowDPI(hwnd unsafe.Pointer) uint {
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	return uint(dpi)
}

// applyDarkTitleBar matches the window chrome to the dark UI. Windows 11 also
// honors the caption and text colors; older builds fall back to dark mode.
func applyDarkTitleBar(hwnd unsafe.Pointer) {
	if hwnd == nil {
		return
	}
	dark := int32(1)
	if hr, _, err := procDwmSetWindowAttribute.Call(
		uintptr(hwnd), dwmwaUseImmersiveDarkMode,
		uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark),
	); hr != 0 {
		log.Printf("dark title bar: HRESULT 0x%x (%v)", hr, err)
	}
	caption := uint32(0x00171514) // COLORREF for #141517.
	if hr, _, err := procDwmSetWindowAttribute.Call(
		uintptr(hwnd), dwmwaCaptionColor,
		uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption),
	); hr != 0 {
		log.Printf("title bar color: HRESULT 0x%x (%v)", hr, err)
	}
	text := uint32(0x00F1ECEC) // COLORREF for #ececf1.
	if hr, _, err := procDwmSetWindowAttribute.Call(
		uintptr(hwnd), dwmwaTextColor,
		uintptr(unsafe.Pointer(&text)), unsafe.Sizeof(text),
	); hr != 0 {
		log.Printf("title bar text color: HRESULT 0x%x (%v)", hr, err)
	}
}

func initialWindowSize(scale float64) (uint, uint) {
	if scale <= 0 {
		scale = 1
	}
	width, height := uint(1180*scale), uint(900*scale)
	minWidth, minHeight := uint(860*scale), uint(620*scale)
	marginX, marginY := uint(80*scale), uint(120*scale)

	screenWidthRaw, _, _ := procGetSystemMetrics.Call(0)  // SM_CXSCREEN
	screenHeightRaw, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	screenWidth, screenHeight := uint(screenWidthRaw), uint(screenHeightRaw)
	if screenWidth > 2*marginX {
		if maxWidth := uint(screenWidth) - marginX; width > maxWidth {
			width = maxWidth
		}
	}
	if screenHeight > 2*marginY {
		if maxHeight := uint(screenHeight) - marginY; height > maxHeight {
			height = maxHeight
		}
	}
	if width < minWidth {
		width = minWidth
	}
	if height < minHeight {
		height = minHeight
	}
	return width, height
}

func openExternalURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("refusing to open non-web URL scheme %q", parsed.Scheme)
	}

	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(parsed.String())
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func showErrorDialog(title, message string) {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	messagePtr, _ := windows.UTF16PtrFromString(message)
	_, _ = windows.MessageBox(0, messagePtr, titlePtr, windows.MB_OK|windows.MB_ICONERROR)
}
