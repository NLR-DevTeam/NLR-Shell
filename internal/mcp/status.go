package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"nlrshell/internal/monitor"
)

func systemStatus(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server string `json:"server"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	static, snap, err := sample(ctx, e)
	if err != nil {
		return "", err
	}
	if !snap.Supported {
		return "", errors.New("the server has no /proc, so it is probably not Linux; use run_command to inspect it")
	}
	return formatStatus(static, snap), nil
}

// sample runs the monitor script used by the app's monitor panel and
// returns its second snapshot, taken a second after the first, so that
// CPU usage and network rates are known.
func sample(ctx context.Context, e *pooled) (monitor.Static, monitor.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sess, err := e.conn.NewSession()
	if err != nil {
		return monitor.Static{}, monitor.Snapshot{}, fmt.Errorf("cannot open a channel: %w", err)
	}
	defer sess.Close()
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.Start("exec sh"); err != nil {
		return monitor.Static{}, monitor.Snapshot{}, fmt.Errorf("cannot start the monitor: %w", err)
	}
	if _, err := io.WriteString(stdin, monitor.Script); err != nil {
		return monitor.Static{}, monitor.Snapshot{}, err
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	p := monitor.NewParser()
	ticked, snaps := false, 0
	for {
		select {
		case <-ctx.Done():
			return monitor.Static{}, monitor.Snapshot{}, errors.New("timed out reading the system status")
		case line, ok := <-lines:
			if !ok {
				return monitor.Static{}, monitor.Snapshot{}, errors.New("the monitor script stopped early; the server may not have a POSIX shell")
			}
			snap, done := p.Feed(line)
			if p.Ready && !ticked {
				ticked = true
				io.WriteString(stdin, p.TickLine())
			}
			if !done {
				continue
			}
			if snaps++; snaps == 2 {
				stdin.Close()
				return p.Static, *snap, nil
			}
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				continue
			}
			io.WriteString(stdin, p.TickLine())
		}
	}
}

func formatStatus(st monitor.Static, s monitor.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %s", st.Host)
	if st.OS != "" {
		fmt.Fprintf(&b, " — %s", st.OS)
	}
	if st.Kernel != "" {
		fmt.Fprintf(&b, " (%s)", st.Kernel)
	}
	if st.User != "" {
		fmt.Fprintf(&b, ", logged in as %s", st.User)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Uptime: %s\n", formatDuration(s.Uptime))

	fmt.Fprintf(&b, "CPU: %.1f%% used", s.CPU)
	if st.CPUModel != "" || st.NCPU > 0 {
		fmt.Fprintf(&b, " — %d × %s", st.NCPU, strings.TrimSpace(st.CPUModel))
	}
	b.WriteByte('\n')
	if len(s.Cores) > 1 {
		parts := make([]string, len(s.Cores))
		for i, c := range s.Cores {
			parts[i] = fmt.Sprintf("%.0f%%", c)
		}
		fmt.Fprintf(&b, "  per core: %s\n", strings.Join(parts, " "))
	}
	fmt.Fprintf(&b, "Load average: %.2f %.2f %.2f\n", s.Load[0], s.Load[1], s.Load[2])

	fmt.Fprintf(&b, "Memory: %s / %s used (%s), %s buff/cache\n",
		formatBytes(s.MemUsed), formatBytes(s.MemTotal), percent(s.MemUsed, s.MemTotal), formatBytes(s.MemCache))
	if s.SwapTotal > 0 {
		fmt.Fprintf(&b, "Swap: %s / %s used (%s)\n", formatBytes(s.SwapUsed), formatBytes(s.SwapTotal), percent(s.SwapUsed, s.SwapTotal))
	} else {
		b.WriteString("Swap: none\n")
	}

	fmt.Fprintf(&b, "Network: ↓ %s/s ↑ %s/s\n", formatBytes(uint64(s.RxRate)), formatBytes(uint64(s.TxRate)))
	for _, it := range s.Ifaces {
		fmt.Fprintf(&b, "  %s: ↓ %s/s ↑ %s/s (total ↓ %s ↑ %s)\n", it.Name,
			formatBytes(uint64(it.RxRate)), formatBytes(uint64(it.TxRate)), formatBytes(it.RxTotal), formatBytes(it.TxTotal))
	}

	if len(s.Disks) > 0 {
		b.WriteString("Disks:\n")
		for _, d := range s.Disks {
			fmt.Fprintf(&b, "  %s (%s): %s / %s used (%s), %s free\n", d.Mount, d.FS,
				formatBytes(d.Used), formatBytes(d.Total), percent(d.Used, d.Total), formatBytes(d.Avail))
		}
	}

	fmt.Fprintf(&b, "Processes: %d", s.ProcCount)
	if procs := busiest(s.Procs, statusProcs); len(procs) > 0 {
		fmt.Fprintf(&b, ", the %d busiest by CPU, then memory:\n", len(procs))
		fmt.Fprintf(&b, "  %7s %6s %9s  %s\n", "PID", "CPU%", "RSS", "NAME")
		for _, p := range procs {
			fmt.Fprintf(&b, "  %7d %6.1f %9s  %s\n", p.PID, p.CPU, formatBytes(p.Mem), p.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// statusProcs is how many processes system_status lists.
const statusProcs = 15

// busiest returns at most n processes, by CPU and then by memory. Kernel
// threads, which have no memory of their own, are left out.
func busiest(all []monitor.Proc, n int) []monitor.Proc {
	var out []monitor.Proc
	for _, p := range all {
		if p.Mem > 0 {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		return out[i].Mem > out[j].Mem
	})
	return out[:min(len(out), n)]
}

func percent(used, total uint64) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(used)/float64(total)*100)
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %ciB", v, "KMGTP"[i])
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
