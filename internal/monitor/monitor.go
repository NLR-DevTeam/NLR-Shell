// Package monitor collects system metrics from a remote Linux host over a
// single long-lived shell process. The script prints one snapshot each time
// it reads a line from stdin, so the client controls the sampling rate and
// the process exits by itself when the channel closes.
package monitor

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Script is fed to "sh" on stdin. Each subsequent input line triggers one
// snapshot; the line may carry PIDs whose working directory should be
// resolved (first readable one wins).
const Script = `export LC_ALL=C LANG=C
PATH="$PATH:/usr/sbin:/sbin:/usr/bin:/bin"
echo "@@NLR static"
echo "pid $$"
echo "uname $(uname -srm 2>/dev/null)"
echo "host $(hostname 2>/dev/null || cat /proc/sys/kernel/hostname 2>/dev/null)"
echo "os $( (. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") )"
echo "cpu $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2-)"
echo "ncpu $(grep -c '^processor' /proc/cpuinfo 2>/dev/null)"
echo "tick $(getconf CLK_TCK 2>/dev/null || echo 100)"
echo "page $(getconf PAGESIZE 2>/dev/null || echo 4096)"
echo "user $(id -un 2>/dev/null)"
echo "@@NLR ready"
while read -r P; do
echo "@@NLR begin"
echo "@stat"; grep '^cpu' /proc/stat 2>/dev/null
echo "@mem"; grep -E '^(MemTotal|MemFree|MemAvailable|Buffers|Cached|SReclaimable|SwapTotal|SwapFree):' /proc/meminfo 2>/dev/null
echo "@load"; cat /proc/loadavg 2>/dev/null
echo "@uptime"; cat /proc/uptime 2>/dev/null
echo "@net"; cat /proc/net/dev 2>/dev/null
echo "@df"; df -kP 2>/dev/null
echo "@proc"; cat /proc/[0-9]*/stat 2>/dev/null | awk '{s=$0;e=0;while((i=index(s,")"))>0){e+=i;s=substr(s,i+1)};o=index($0,"(");if(o==0||e==0)next;n=split(substr($0,e+2),a," ");print substr($0,1,o-2),a[2],a[5],a[6],a[12],a[13],a[22],substr($0,o+1,e-o-1)}' 2>/dev/null
echo "@cwd"; for p in $P; do readlink "/proc/$p/cwd" 2>/dev/null && break; done
echo "@@NLR end"
done
`

// Static is host information that does not change between snapshots.
type Static struct {
	Host     string
	OS       string
	Kernel   string
	CPUModel string
	NCPU     int
	User     string
}

// Iface is the traffic of one network interface.
type Iface struct {
	Name           string
	RxRate, TxRate float64 // bytes per second
	RxTotal        uint64
	TxTotal        uint64
}

// Disk is one mounted filesystem.
type Disk struct {
	FS    string
	Mount string
	Total uint64 // bytes
	Used  uint64
	Avail uint64
}

// Proc is one process.
type Proc struct {
	PID  int
	Name string
	CPU  float64 // percent of one core
	Mem  uint64  // resident bytes
}

// Snapshot is one sample of the host state.
type Snapshot struct {
	Time      time.Time
	Supported bool // false if the host has no /proc (not Linux)

	CPU   float64   // percent, all cores combined
	Cores []float64 // percent per core

	MemTotal, MemUsed, MemCache uint64
	SwapTotal, SwapUsed         uint64

	Load   [3]float64
	Uptime time.Duration

	RxRate, TxRate float64
	Ifaces         []Iface

	Disks []Disk

	Procs     []Proc
	ProcCount int

	// Cwd is the working directory of the interactive shell, if it could be
	// determined.
	Cwd string
}

type cpuTimes struct{ total, idle uint64 }

type procInfo struct {
	pid, ppid, tty, tpgid int
	ticks                 uint64
	rss                   uint64
	name                  string
}

// Parser turns the script output into snapshots. It is not safe for
// concurrent use.
type Parser struct {
	Static Static
	Ready  bool

	selfPID int
	tick    float64
	page    uint64

	section string
	inSnap  bool
	static  bool

	cur       Snapshot
	cpus      []cpuTimes
	prevCPUs  []cpuTimes
	uptime    float64
	prevUp    float64
	wall      time.Time
	prevWall  time.Time
	ifaces    map[string][2]uint64
	prevIf    map[string][2]uint64
	procs     []procInfo
	prevTicks map[int]uint64
	mem       map[string]uint64

	// CwdPIDs are the PIDs to send with the next tick: the foreground
	// process of the interactive shell followed by the shell itself.
	CwdPIDs []int
}

// NewParser returns a parser with sane defaults.
func NewParser() *Parser {
	return &Parser{tick: 100, page: 4096}
}

// Feed processes one line of script output. It returns a snapshot when the
// line completes one.
func (p *Parser) Feed(line string) (*Snapshot, bool) {
	line = strings.TrimRight(line, "\r")
	if strings.HasPrefix(line, "@@NLR ") {
		switch line[6:] {
		case "static":
			p.static = true
		case "ready":
			p.static = false
			p.Ready = true
		case "begin":
			p.begin()
		case "end":
			if p.inSnap {
				p.inSnap = false
				return p.finish(), true
			}
		}
		return nil, false
	}
	if p.static {
		k, v, _ := strings.Cut(line, " ")
		v = strings.TrimSpace(v)
		switch k {
		case "pid":
			p.selfPID, _ = strconv.Atoi(v)
		case "uname":
			p.Static.Kernel = v
		case "host":
			p.Static.Host = v
		case "os":
			p.Static.OS = v
		case "cpu":
			p.Static.CPUModel = strings.Join(strings.Fields(v), " ")
		case "ncpu":
			p.Static.NCPU, _ = strconv.Atoi(v)
		case "tick":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				p.tick = f
			}
		case "page":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
				p.page = n
			}
		case "user":
			p.Static.User = v
		}
		return nil, false
	}
	if !p.inSnap {
		return nil, false
	}
	if strings.HasPrefix(line, "@") {
		p.section = line[1:]
		return nil, false
	}
	p.line(line)
	return nil, false
}

func (p *Parser) begin() {
	p.inSnap = true
	p.section = ""
	p.cur = Snapshot{}
	p.prevCPUs, p.cpus = p.cpus, p.prevCPUs[:0]
	p.prevIf, p.ifaces = p.ifaces, map[string][2]uint64{}
	p.prevUp = p.uptime
	p.prevWall, p.wall = p.wall, time.Now()
	p.procs = p.procs[:0]
	p.mem = map[string]uint64{}
}

func (p *Parser) line(line string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	switch p.section {
	case "stat":
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			return
		}
		var c cpuTimes
		for i := 1; i < len(f) && i <= 8; i++ {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			c.total += v
			if i == 4 || i == 5 {
				c.idle += v
			}
		}
		p.cpus = append(p.cpus, c)
	case "mem":
		if len(f) >= 2 {
			v, _ := strconv.ParseUint(f[1], 10, 64)
			p.mem[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	case "load":
		for i := 0; i < 3 && i < len(f); i++ {
			p.cur.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	case "uptime":
		p.uptime, _ = strconv.ParseFloat(f[0], 64)
		p.cur.Uptime = time.Duration(p.uptime * float64(time.Second))
	case "net":
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		name = strings.TrimSpace(name)
		v := strings.Fields(rest)
		if len(v) < 9 {
			return
		}
		rx, _ := strconv.ParseUint(v[0], 10, 64)
		tx, _ := strconv.ParseUint(v[8], 10, 64)
		p.ifaces[name] = [2]uint64{rx, tx}
	case "df":
		if len(f) < 6 || f[0] == "Filesystem" {
			return
		}
		fs, mount := f[0], strings.Join(f[5:], " ")
		if !diskWanted(fs, mount) {
			return
		}
		total, _ := strconv.ParseUint(f[1], 10, 64)
		used, _ := strconv.ParseUint(f[2], 10, 64)
		avail, _ := strconv.ParseUint(f[3], 10, 64)
		if total == 0 {
			return
		}
		for i := range p.cur.Disks {
			if p.cur.Disks[i].FS == fs {
				if len(mount) < len(p.cur.Disks[i].Mount) {
					p.cur.Disks[i].Mount = mount
				}
				return
			}
		}
		p.cur.Disks = append(p.cur.Disks, Disk{FS: fs, Mount: mount, Total: total * 1024, Used: used * 1024, Avail: avail * 1024})
	case "proc":
		if len(f) < 8 {
			return
		}
		var pi procInfo
		pi.pid, _ = strconv.Atoi(f[0])
		pi.ppid, _ = strconv.Atoi(f[1])
		pi.tty, _ = strconv.Atoi(f[2])
		pi.tpgid, _ = strconv.Atoi(f[3])
		ut, _ := strconv.ParseUint(f[4], 10, 64)
		st, _ := strconv.ParseUint(f[5], 10, 64)
		pi.ticks = ut + st
		rss, _ := strconv.ParseInt(f[6], 10, 64)
		if rss > 0 {
			pi.rss = uint64(rss) * p.page
		}
		pi.name = strings.Join(f[7:], " ")
		p.procs = append(p.procs, pi)
	case "cwd":
		if p.cur.Cwd == "" && strings.HasPrefix(line, "/") {
			p.cur.Cwd = line
		}
	}
}

func diskWanted(fs, mount string) bool {
	if strings.HasPrefix(fs, "/dev/loop") {
		return false
	}
	if strings.HasPrefix(fs, "/") || strings.Contains(fs, ":") {
		return true
	}
	return mount == "/"
}

func ifaceWanted(name string) bool {
	if name == "lo" {
		return false
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "kube"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

func (p *Parser) finish() *Snapshot {
	s := &p.cur
	s.Time = p.wall
	s.Supported = len(p.cpus) > 0

	// Elapsed time, preferring the remote clock.
	dt := p.uptime - p.prevUp
	if p.prevUp == 0 || dt <= 0 {
		dt = p.wall.Sub(p.prevWall).Seconds()
		if p.prevWall.IsZero() {
			dt = 0
		}
	}

	// CPU.
	if len(p.cpus) > 0 && len(p.prevCPUs) == len(p.cpus) {
		pct := func(a, b cpuTimes) float64 {
			dtot := float64(a.total - b.total)
			if dtot <= 0 {
				return 0
			}
			v := (dtot - float64(a.idle-b.idle)) / dtot * 100
			return min(max(v, 0), 100)
		}
		s.CPU = pct(p.cpus[0], p.prevCPUs[0])
		for i := 1; i < len(p.cpus); i++ {
			s.Cores = append(s.Cores, pct(p.cpus[i], p.prevCPUs[i]))
		}
	} else {
		s.Cores = make([]float64, max(len(p.cpus)-1, 0))
	}

	// Memory.
	s.MemTotal = p.mem["MemTotal"]
	s.MemCache = p.mem["Buffers"] + p.mem["Cached"] + p.mem["SReclaimable"]
	if avail, ok := p.mem["MemAvailable"]; ok {
		s.MemUsed = sub(s.MemTotal, avail)
	} else {
		s.MemUsed = sub(s.MemTotal, p.mem["MemFree"]+s.MemCache)
	}
	s.SwapTotal = p.mem["SwapTotal"]
	s.SwapUsed = sub(s.SwapTotal, p.mem["SwapFree"])

	// Network.
	names := make([]string, 0, len(p.ifaces))
	for name := range p.ifaces {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cur := p.ifaces[name]
		it := Iface{Name: name, RxTotal: cur[0], TxTotal: cur[1]}
		if prev, ok := p.prevIf[name]; ok && dt > 0 && cur[0] >= prev[0] && cur[1] >= prev[1] {
			it.RxRate = float64(cur[0]-prev[0]) / dt
			it.TxRate = float64(cur[1]-prev[1]) / dt
		}
		if ifaceWanted(name) {
			s.RxRate += it.RxRate
			s.TxRate += it.TxRate
			s.Ifaces = append(s.Ifaces, it)
		}
	}

	// Processes.
	ticks := make(map[int]uint64, len(p.procs))
	byPID := make(map[int]*procInfo, len(p.procs))
	all := make([]Proc, 0, len(p.procs))
	for i := range p.procs {
		pi := &p.procs[i]
		ticks[pi.pid] = pi.ticks
		byPID[pi.pid] = pi
		pr := Proc{PID: pi.pid, Name: pi.name, Mem: pi.rss}
		if prev, ok := p.prevTicks[pi.pid]; ok && dt > 0 && pi.ticks >= prev {
			pr.CPU = float64(pi.ticks-prev) / p.tick / dt * 100
		}
		all = append(all, pr)
	}
	p.prevTicks = ticks
	s.ProcCount = len(all)
	s.Procs = topProcs(all, 40)

	// Find the interactive shell: a process with a controlling terminal
	// whose parent is one of our ancestors (the per-connection sshd).
	p.CwdPIDs = p.CwdPIDs[:0]
	chain := map[int]bool{p.selfPID: true}
	anc := p.selfPID
	for depth := 0; depth < 4 && p.selfPID != 0; depth++ {
		me, ok := byPID[anc]
		if !ok || me.ppid <= 1 {
			break
		}
		anc = me.ppid
		chain[anc] = true
		shell := 0
		for i := range p.procs {
			pi := &p.procs[i]
			if pi.ppid == anc && pi.tty != 0 && !chain[pi.pid] && (shell == 0 || pi.pid < shell) {
				shell = pi.pid
			}
		}
		if shell != 0 {
			if fg := byPID[shell].tpgid; fg > 0 && fg != shell {
				p.CwdPIDs = append(p.CwdPIDs, fg)
			}
			p.CwdPIDs = append(p.CwdPIDs, shell)
			break
		}
	}

	out := *s
	return &out
}

// topProcs keeps the union of the heaviest processes by CPU and by memory,
// so that either sort order in the UI is accurate.
func topProcs(all []Proc, n int) []Proc {
	if len(all) <= n {
		return all
	}
	keep := map[int]bool{}
	sort.Slice(all, func(i, j int) bool { return all[i].CPU > all[j].CPU })
	for _, p := range all[:n] {
		keep[p.PID] = true
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Mem > all[j].Mem })
	for _, p := range all[:n] {
		keep[p.PID] = true
	}
	out := make([]Proc, 0, len(keep))
	for _, p := range all {
		if keep[p.PID] {
			out = append(out, p)
		}
	}
	return out
}

func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

// TickLine returns the stdin line that requests the next snapshot.
func (p *Parser) TickLine() string {
	var sb strings.Builder
	for i, pid := range p.CwdPIDs {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(strconv.Itoa(pid))
	}
	sb.WriteByte('\n')
	return sb.String()
}
