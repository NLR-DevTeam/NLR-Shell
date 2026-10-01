package ui

import (
	"fmt"
	"time"
)

// fmtBytes formats a byte count with binary units.
func fmtBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		f /= unit
		if f < unit {
			if f >= 100 {
				return fmt.Sprintf("%.0f %s", f, u)
			}
			if f >= 10 {
				return fmt.Sprintf("%.1f %s", f, u)
			}
			return fmt.Sprintf("%.2f %s", f, u)
		}
	}
	return fmt.Sprintf("%.0f EB", f/unit)
}

// fmtRate formats a transfer rate.
func fmtRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	return fmtBytes(uint64(bps)) + "/s"
}

// fmtUptime formats a duration as days, hours and minutes.
func fmtUptime(d time.Duration) string {
	m := int(d.Minutes())
	days, hours, mins := m/1440, (m%1440)/60, m%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分", hours, mins)
	}
	return fmt.Sprintf("%d 分钟", mins)
}

// fmtAgo formats how long ago a unix timestamp was.
func fmtAgo(unix int64) string {
	if unix == 0 {
		return "从未连接"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

// fmtModTime formats a file modification time compactly.
func fmtModTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// fmtETA formats the remaining time of a transfer.
func fmtETA(remaining int64, rate float64) string {
	if rate <= 0 || remaining <= 0 {
		return ""
	}
	s := int(float64(remaining) / rate)
	switch {
	case s < 60:
		return fmt.Sprintf("剩余 %d 秒", s)
	case s < 3600:
		return fmt.Sprintf("剩余 %d 分 %d 秒", s/60, s%60)
	}
	return fmt.Sprintf("剩余 %d 小时 %d 分", s/3600, (s%3600)/60)
}
