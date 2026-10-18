package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/eagtf/duolink/internal/netif"
	"golang.org/x/sys/windows"
)

func isAdmin() bool {
	// The process token's TokenElevation flag: true only under "Run as
	// administrator" (UAC), which is what changing routes needs.
	return windows.GetCurrentProcessToken().IsElevated()
}

// checkWintunDLL makes sure wintun.dll sits next to duolink.exe. The wintun
// package loads it with LoadLibraryEx(LOAD_LIBRARY_SEARCH_APPLICATION_DIR|
// LOAD_LIBRARY_SEARCH_SYSTEM32), so a DLL dropped in the current directory or
// on PATH is never picked up. That also means a missing DLL fails late and
// cryptically, so we check up front.
func checkWintunDLL() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("wintun.dll not found at %s\n"+
			"  download the official zip from https://www.wintun.net/ and copy\n"+
			"  wintun\\bin\\amd64\\wintun.dll next to duolink.exe", p)
	}
	return nil
}

// printIPv6Warning says whether IPv6 bypasses duolink on this PC: duolink
// routes only IPv4 into the TUN.
func printIPv6Warning(all []*netif.Adapter) {
	v6 := netif.WithGlobalIPv6(all)
	if len(v6) == 0 {
		fmt.Println("IPv6: no adapter has a global IPv6 address. All traffic goes through duolink.")
		return
	}
	fmt.Println("WARNING: IPv6 is active and bypasses duolink (it balances IPv4 only):")
	for _, a := range v6 {
		fmt.Printf("  %-12s %s\n", a.Name, a.GlobalV6[0])
	}
	fmt.Println("  Apps that prefer IPv6 (Steam's CDN may) will use only that line.")
	fmt.Println("  To turn IPv6 off on an adapter (reversible with Enable-NetAdapterBinding), in admin PowerShell:")
	for _, a := range v6 {
		fmt.Printf("    Disable-NetAdapterBinding -Name '%s' -ComponentID ms_tcpip6\n", a.Name)
	}
}
