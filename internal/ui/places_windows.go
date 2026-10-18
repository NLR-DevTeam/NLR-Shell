package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"gioui.org/widget"
	"golang.org/x/sys/windows"
)

// knownPlaces are the special folders the file picker names and pins by
// default, in their default order. Desktop and Documents may be redirected
// (to OneDrive, say), so they are asked of the shell.
func knownPlaces() []place {
	var out []place
	for _, k := range []struct {
		id   *windows.KNOWNFOLDERID
		name string
		ic   *widget.Icon
	}{
		{windows.FOLDERID_Desktop, "桌面", icDesktop},
		{windows.FOLDERID_Downloads, "下载", icDownload},
		{windows.FOLDERID_Documents, "文档", icDocument},
	} {
		if p, err := windows.KnownFolderPath(k.id, 0); err == nil && p != "" {
			out = append(out, place{path: p, name: k.name, icon: k.ic})
		}
	}
	return out
}

// listDrives returns the drives Explorer shows under This PC. It may block
// on a slow network or optical drive, so it runs off the UI goroutine.
func listDrives() []place {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []place
	for i := range 26 {
		if mask&(1<<i) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		kind := windows.GetDriveType(rp)
		var name string
		ic := icDrive
		switch kind {
		case windows.DRIVE_FIXED, windows.DRIVE_RAMDISK:
			name = "本地磁盘"
		case windows.DRIVE_REMOVABLE:
			name, ic = "可移动磁盘", icUSB
		case windows.DRIVE_CDROM:
			name, ic = "光驱", icDisc
		case windows.DRIVE_REMOTE:
			name, ic = "网络驱动器", icNetwork
		default:
			continue
		}
		var label [windows.MAX_PATH + 1]uint16
		if err := windows.GetVolumeInformation(rp, &label[0], uint32(len(label)), nil, nil, nil, nil, 0); err == nil {
			if l := windows.UTF16ToString(label[:]); l != "" {
				name = l
			}
		}
		out = append(out, place{path: root, name: fmt.Sprintf("%s (%s:)", name, letter), icon: ic})
	}
	return out
}

// samePath compares paths the way the file system does: case-insensitively.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
