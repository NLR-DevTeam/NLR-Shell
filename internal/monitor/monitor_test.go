package monitor

import (
	"math"
	"strings"
	"testing"
)

const static = `@@NLR static
pid 900
uname Linux 6.1.0-18-amd64 x86_64
host web-1
os Debian GNU/Linux 12 (bookworm)
cpu  Intel(R) Xeon(R)   Platinum 8269CY CPU @ 2.50GHz
ncpu 2
tick 100
page 4096
user root
@@NLR ready
`

func snap(up string, cpu, c0, c1 string, rx, tx string, bashTicks string) string {
	return `@@NLR begin
@stat
cpu  ` + cpu + `
cpu0 ` + c0 + `
cpu1 ` + c1 + `
@mem
MemTotal:        4000000 kB
MemFree:          500000 kB
MemAvailable:    3000000 kB
Buffers:          100000 kB
Cached:          1900000 kB
SwapTotal:       1000000 kB
SwapFree:         750000 kB
SReclaimable:     200000 kB
@load
0.52 0.40 0.31 1/203 4242
@uptime
` + up + ` 99999.00
@net
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: ` + rx + ` 50 0 0 0 0 0 0 ` + tx + ` 40 0 0 0 0 0 0
docker0: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0
@df
Filesystem     1024-blocks     Used Available Capacity Mounted on
udev               2000000        0   2000000       0% /dev
/dev/vda1         41152736 20576368  18463696      53% /
tmpfs               400000      600    399400       1% /run
/dev/vdb1        103081248 51540624  46281200      53% /data disk
/dev/loop0           56000    56000         0     100% /snap/core
@proc
1 0 0 -1 50 50 2500 systemd
700 1 0 -1 5 5 1500 sshd
800 700 0 -1 1 1 1600 sshd
801 800 34816 850 ` + bashTicks + ` 0 1000 bash
850 801 34816 850 10 0 500 my (weird) proc
900 800 0 -1 0 0 200 sh
@cwd
/var/www
@@NLR end
`
}

func TestParser(t *testing.T) {
	p := NewParser()
	var got []*Snapshot
	feed := func(s string) {
		for _, l := range strings.Split(s, "\n") {
			if sn, ok := p.Feed(l); ok {
				got = append(got, sn)
			}
		}
	}
	feed(static)
	if !p.Ready || p.Static.Host != "web-1" || p.Static.NCPU != 2 || p.Static.Kernel != "Linux 6.1.0-18-amd64 x86_64" {
		t.Fatalf("static: %+v", p.Static)
	}
	if p.Static.CPUModel != "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz" || p.Static.OS != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("static: %+v", p.Static)
	}
	feed(snap("1000.00", "100 0 100 800 0 0 0 0 0 0", "50 0 50 400 0 0 0 0", "50 0 50 400 0 0 0 0", "10000", "20000", "100"))
	feed(snap("1002.00", "150 0 150 900 0 0 0 0 0 0", "90 0 90 420 0 0 0 0", "60 0 60 480 0 0 0 0", "12048", "20000", "300"))
	if len(got) != 2 {
		t.Fatalf("snapshots: %d", len(got))
	}
	s := got[1]
	near := func(name string, a, b float64) {
		t.Helper()
		if math.Abs(a-b) > 0.01 {
			t.Fatalf("%s = %v, want %v", name, a, b)
		}
	}
	if !s.Supported {
		t.Fatal("should be supported")
	}
	near("cpu", s.CPU, 50)
	near("core0", s.Cores[0], 80)
	near("core1", s.Cores[1], 20)
	if s.MemTotal != 4000000*1024 || s.MemUsed != 1000000*1024 || s.MemCache != 2200000*1024 {
		t.Fatalf("mem: %+v", s)
	}
	if s.SwapUsed != 250000*1024 {
		t.Fatalf("swap: %d", s.SwapUsed)
	}
	near("load", s.Load[1], 0.40)
	near("rx", s.RxRate, 1024)
	near("tx", s.TxRate, 0)
	if len(s.Ifaces) != 1 || s.Ifaces[0].Name != "eth0" {
		t.Fatalf("ifaces: %+v", s.Ifaces)
	}
	if len(s.Disks) != 2 || s.Disks[0].Mount != "/" || s.Disks[1].Mount != "/data disk" || s.Disks[0].Total != 41152736*1024 {
		t.Fatalf("disks: %+v", s.Disks)
	}
	if s.ProcCount != 6 {
		t.Fatalf("procs: %d", s.ProcCount)
	}
	var bash, weird *Proc
	for i := range s.Procs {
		switch s.Procs[i].PID {
		case 801:
			bash = &s.Procs[i]
		case 850:
			weird = &s.Procs[i]
		}
	}
	near("bash cpu", bash.CPU, 100)
	if bash.Mem != 1000*4096 || weird.Name != "my (weird) proc" {
		t.Fatalf("procs: %+v %+v", bash, weird)
	}
	if s.Cwd != "/var/www" {
		t.Fatalf("cwd: %q", s.Cwd)
	}
	// The shell is 801 (sibling of our sh with a tty); its foreground job is 850.
	if p.TickLine() != "850 801\n" {
		t.Fatalf("tick: %q", p.TickLine())
	}
}

func TestUnsupported(t *testing.T) {
	p := NewParser()
	var last *Snapshot
	for _, l := range strings.Split("@@NLR static\npid 5\n@@NLR ready\n@@NLR begin\n@stat\n@mem\n@load\n@uptime\n@net\n@df\n@proc\n@cwd\n@@NLR end\n", "\n") {
		if s, ok := p.Feed(l); ok {
			last = s
		}
	}
	if last == nil || last.Supported {
		t.Fatalf("got %+v", last)
	}
}
