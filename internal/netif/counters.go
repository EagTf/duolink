package netif

import "golang.org/x/sys/windows"

// Octets returns how many bytes the adapter has received and sent since
// Windows brought it up, counted by Windows for every app, not only the
// traffic duolink forwards. ok is false if the adapter is gone.
//
// GetIfEntry2Ex (iphlpapi.dll) fills a MIB_IF_ROW2 for the interface whose
// LUID is set in the row. MibIfEntryNormal asks for the statistics too.
// The counters are 64-bit, and they restart from 0 when the adapter
// reconnects, so callers must handle a count that went down.
func Octets(luid uint64) (in, out uint64, ok bool) {
	row := windows.MibIfRow2{InterfaceLuid: luid}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
		return 0, 0, false
	}
	return row.InOctets, row.OutOctets, true
}
