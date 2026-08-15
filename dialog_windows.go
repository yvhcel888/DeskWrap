//go:build windows

// DeskWrap - native folder picker via IFileOpenDialog (COM, no extra deps).
package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modOle32    = syscall.NewLazyDLL("ole32.dll")
	modOleAut32 = syscall.NewLazyDLL("oleaut32.dll")
	modUser32   = syscall.NewLazyDLL("user32.dll")

	procCoInitializeEx = modOle32.NewProc("CoInitializeEx")
	procCoCreateInst   = modOle32.NewProc("CoCreateInstance")
	procCoTaskMemFree  = modOle32.NewProc("CoTaskMemFree")
	procSysAllocString = modOleAut32.NewProc("SysAllocString")
	procSysFreeString  = modOleAut32.NewProc("SysFreeString")
	procMessageBoxW    = modUser32.NewProc("MessageBoxW")
)

// GUID layout for COM calls.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	// CLSID_FileOpenDialog {DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}
	clsidFileOpenDialog = guid{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	// IID_IFileOpenDialog {D57C7288-D4AD-4768-BE02-9D969532D960}
	iidIFileOpenDialog = guid{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

// comCall invokes a COM interface method through its vtable.
func comCall(ptr unsafe.Pointer, index uintptr, args ...uintptr) (r1, r2 uintptr) {
	vtbl := *(**uintptr)(unsafe.Pointer(ptr))
	fn := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtbl)) + index*unsafe.Sizeof(uintptr(0))))
	all := append([]uintptr{uintptr(ptr)}, args...)
	a, b, _ := syscall.SyscallN(fn, all...)
	return a, b
}

// msgBox shows a native message box. flags: MB_YESNO=0x4, MB_OK=0,
// MB_ICONWARNING=0x30, MB_ICONERROR=0x10, MB_ICONINFORMATION=0x40.
// Returns IDYES=6 / IDNO=7 / IDOK=1.
func msgBox(hwnd uintptr, text, caption string, flags uintptr) int {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(caption)
	r, _, _ := procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
	return int(r)
}

// pickFolderDialog opens the modern folder picker and returns the selected
// path, or "" when cancelled. Failures are pushed to the ring log so they
// show up in the GUI's "日志 & 诊断" panel.
func pickFolderDialog(title string) string {
	// CoInitializeEx(COINIT_APARTMENTTHREADED=2). RPC_E_CHANGED_MODE
	// (0x80010106) means the thread is already initialized - fine.
	hr, _, _ := procCoInitializeEx.Call(0, 2)
	if hr != 0 && hr != 1 && hr != 0x80010106 {
		serviceLog.push(fmt.Sprintf("[pickFolder] CoInitializeEx failed: %#x", hr))
		return ""
	}

	var dlg unsafe.Pointer
	hr, _, _ = procCoCreateInst.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1, /*CLSCTX_INPROC_SERVER*/
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hr != 0 || dlg == nil {
		serviceLog.push(fmt.Sprintf("[pickFolder] CoCreateInstance failed: %#x", hr))
		return ""
	}
	defer comCall(dlg, 2) // Release

	// SetOptions(FOS_PICKFOLDERS=0x20 | FOS_FORCEFILESYSTEM=0x40)
	comCall(dlg, 9, 0x60)
	if title != "" {
		bstr, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(title))))
		if bstr != 0 {
			comCall(dlg, 17, bstr) // SetTitle
			procSysFreeString.Call(bstr)
		}
	}

	// Show(hwndOwner) - HRESULT 0x800704C7 = user cancelled.
	hr, _ = comCall(dlg, 3, 0)
	if hr != 0 {
		serviceLog.push(fmt.Sprintf("[pickFolder] Show failed: %#x", hr))
		return ""
	}

	// GetResult -> IShellItem
	var item unsafe.Pointer
	hr, _ = comCall(dlg, 20, uintptr(unsafe.Pointer(&item)))
	if hr != 0 || item == nil {
		serviceLog.push(fmt.Sprintf("[pickFolder] GetResult failed: %#x", hr))
		return ""
	}
	defer comCall(item, 2) // Release

	// IShellItem.GetDisplayName(SIGDN_FILESYSPATH = 0x80058000).
	// vtable: 0 QI, 1 AddRef, 2 Release, 3 BindToHandler, 4 GetParent,
	// 5 GetDisplayName, 6 GetAttributes, 7 Compare.
	var pName unsafe.Pointer
	hr, _ = comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&pName)))
	if hr != 0 || pName == nil {
		serviceLog.push(fmt.Sprintf("[pickFolder] GetDisplayName failed: %#x", hr))
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(pName))
	// Scan to the NUL terminator instead of a fixed-size read: the CoTaskMem
	// buffer is exactly as long as the string plus its terminator.
	buf := unsafe.Slice((*uint16)(pName), 4096)
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(buf[:n])
}
