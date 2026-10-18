package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetExtendedTcpTable / GetExtendedUdpTable (iphlpapi.dll) list every IPv4
// socket with the PID that owns it. x/sys doesn't wrap them, so they're loaded
// here. Signature (both):
//
//	DWORD GetExtended{Tcp,Udp}Table(PVOID table, PDWORD size, BOOL order,
//	                                ULONG af, {TCP,UDP}_TABLE_CLASS class, ULONG reserved)
var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPidAll = 5 // TCP_TABLE_OWNER_PID_ALL: MIB_TCPROW_OWNER_PID rows, all states
	udpTableOwnerPid    = 1 // UDP_TABLE_OWNER_PID: MIB_UDPROW_OWNER_PID rows

	// Row layouts: all fields are DWORDs.
	//   MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr, remotePort, owningPid
	//   MIB_UDPROW_OWNER_PID: localAddr, localPort, owningPid
	tcpRowSize = 24
	udpRowSize = 12

	nameMaxAge = 10 * time.Second // PIDs get reused, so names are cached only briefly
)

// procTable answers "which process owns the app side of this flow?".
//
// Every flow from an app reaches the netstack from a socket the app opened, and
// that socket's local address is the TUN's 198.18.0.1 plus a port. Looking
// that address:port up in the owner table gives the PID, and the PID gives the
// exe name. The TCP handshake with the app is already complete when the flow
// reaches us, so the app's socket is always in the table.
type procTable struct {
	reads atomic.Uint64 // counts table reads; orders snapshots against lookups

	mu    sync.Mutex
	snaps [2]procSnapshot // [0] = TCP, [1] = UDP
	names map[uint32]procName
}

type procSnapshot struct {
	owners map[netip.AddrPort]uint32 // local addr:port → PID
	read   uint64                    // value of reads when this snapshot was taken
}

type procName struct {
	exe string
	at  time.Time
}

func newProcTable() *procTable {
	return &procTable{names: map[uint32]procName{}}
}

// lookup returns the lower-case exe name ("steam.exe") owning the socket with
// local address src, or "" if it can't be found (the socket already closed,
// or a protected process).
func (p *procTable) lookup(udp bool, src netip.AddrPort) string {
	// The app's socket already exists now, so any snapshot taken after this
	// moment contains it. A miss in an older snapshot means "re-read"; a miss
	// in a newer one means the socket is gone. In a burst of new flows, the
	// first lookup re-reads and the others, queued on the lock, reuse it.
	// (A counter, not a timestamp: Windows' clock ticks too coarsely to order
	// events microseconds apart.)
	asked := p.reads.Load()
	p.mu.Lock()
	defer p.mu.Unlock()
	i := 0
	if udp {
		i = 1
	}
	pid, ok := p.find(i, udp, src)
	if !ok && p.snaps[i].read <= asked {
		if owners, err := ownerTable(udp); err == nil {
			p.snaps[i] = procSnapshot{owners: owners, read: p.reads.Add(1)}
			pid, ok = p.find(i, udp, src)
		}
	}
	if !ok || pid == 0 {
		return ""
	}
	if n, ok := p.names[pid]; ok && time.Since(n.at) < nameMaxAge {
		return n.exe
	}
	exe := processName(pid)
	p.names[pid] = procName{exe: exe, at: time.Now()}
	return exe
}

// find looks src up in snapshot i. An unconnected UDP socket is often bound
// to 0.0.0.0:port rather than the TUN address, so UDP also tries that.
func (p *procTable) find(i int, udp bool, src netip.AddrPort) (uint32, bool) {
	owners := p.snaps[i].owners
	if pid, ok := owners[src]; ok {
		return pid, true
	}
	if udp {
		pid, ok := owners[netip.AddrPortFrom(netip.IPv4Unspecified(), src.Port())]
		return pid, ok
	}
	return 0, false
}

// ownerTable reads the TCP or UDP owner table into a local addr:port → PID map.
func ownerTable(udp bool) (map[netip.AddrPort]uint32, error) {
	// Byte offsets of localAddr, localPort and owningPid within a row.
	proc, class, rowSize := procGetExtendedTcpTable, uintptr(tcpTableOwnerPidAll), tcpRowSize
	addrOff, portOff, pidOff := 4, 8, 20
	if udp {
		proc, class, rowSize = procGetExtendedUdpTable, udpTableOwnerPid, udpRowSize
		addrOff, portOff, pidOff = 0, 4, 8
	}
	// The call fails with ERROR_INSUFFICIENT_BUFFER and sets size when the
	// buffer is too small; sockets can appear in between, so retry.
	size := uint32(64 * 1024)
	var buf []byte
	for {
		buf = make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, windows.AF_INET, class, 0)
		if r == 0 {
			break
		}
		if windows.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, fmt.Errorf("%s: %w", proc.Name, windows.Errno(r))
		}
	}
	// Table: DWORD count, then the rows. Addresses are stored in network byte
	// order (memory holds a.b.c.d), ports in the low 16 bits of a DWORD, also
	// in network byte order.
	n := int(binary.LittleEndian.Uint32(buf))
	owners := make(map[netip.AddrPort]uint32, n)
	for i := 0; i < n; i++ {
		row := buf[4+i*rowSize : 4+(i+1)*rowSize]
		addr := netip.AddrFrom4([4]byte(row[addrOff : addrOff+4]))
		port := binary.BigEndian.Uint16(row[portOff:])
		owners[netip.AddrPortFrom(addr, port)] = binary.LittleEndian.Uint32(row[pidOff:])
	}
	return owners, nil
}

// processName returns the lower-case exe file name of pid, or "".
//
// OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION is the smallest access
// right that allows QueryFullProcessImageName; as administrator it works for
// almost every process (not for protected system processes, which never
// matter here).
func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:size])))
}
