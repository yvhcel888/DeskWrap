//go:build windows

// Probe4: correct-ABI probe. Builds a real COM shim for
// ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler (like
// jchv/go-webview2 does) and compares three threading/COM scenarios:
//   A) main thread (edge pkg init already CoInitialized it)
//   B) plain goroutine, NO CoInitializeEx
//   C) goroutine with LockOSThread + CoInitializeEx(APARTMENTTHREADED)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var ole32 = windows.NewLazySystemDLL("ole32.dll")
var procCoInitializeEx = ole32.NewProc("CoInitializeEx")

type completedHandler struct {
	vtbl *completedHandlerVtbl
	tag  string
}

type completedHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

func shimQI(this *completedHandler, refiid, object uintptr) uintptr {
	fmt.Printf("[%s] QI called refiid=%x\n", this.tag, refiid)
	return 0x80004002 // E_NOINTERFACE
}
func shimAddRef(this *completedHandler) uintptr {
	fmt.Printf("[%s] AddRef called\n", this.tag)
	return 1
}
func shimRelease(this *completedHandler) uintptr {
	fmt.Printf("[%s] Release called\n", this.tag)
	return 1
}
func shimInvoke(this *completedHandler, res uintptr, env uintptr) uintptr {
	fmt.Printf("[%s] Invoke: this=%x res=%#x(%d) env=%x\n", this.tag, uintptr(unsafe.Pointer(this)), res, int32(res), env)
	os.Stdout.Sync()
	return 0
}

var vtbl = &completedHandlerVtbl{
	QueryInterface: windows.NewCallback(shimQI),
	AddRef:         windows.NewCallback(shimAddRef),
	Release:        windows.NewCallback(shimRelease),
	Invoke:         windows.NewCallback(shimInvoke),
}

func run(tag string, userData string) {
	fmt.Printf("==== %s ====\n", tag)
	h := &completedHandler{vtbl: vtbl, tag: tag}
	hr, err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(
		nil,
		windows.StringToUTF16Ptr(userData),
		0,
		uintptr(unsafe.Pointer(h)),
	)
	fmt.Printf("%s initial call returned: hr=%#x err=%v\n", tag, hr, err)
	runtime.KeepAlive(h)
	os.Stdout.Sync()
}

func scenarioA() {
	run("A-mainthread", filepath.Join(os.TempDir(), "wv2probe3-A-data"))
}

func scenarioB() {
	done := make(chan struct{})
	go func() {
		run("B-goroutine-nocoinit", filepath.Join(os.TempDir(), "wv2probe3-B-data"))
		close(done)
	}()
	<-done
}

func scenarioC() {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
		fmt.Printf("C: CoInitializeEx -> %#x\n", hr)
		run("C-goroutine-coinit", filepath.Join(os.TempDir(), "wv2probe3-C-data"))
		close(done)
	}()
	<-done
}

func main() {
	which := "A"
	if len(os.Args) > 1 {
		which = os.Args[1]
	}
	switch which {
	case "A":
		scenarioA()
	case "B":
		scenarioB()
	case "C":
		scenarioC()
	default:
		scenarioA()
		scenarioB()
		scenarioC()
	}
	// Invoke is synchronous; brief pause for any straggler output, then exit.
	time.Sleep(1500 * time.Millisecond)
	_ = syscall.Syscall
}
