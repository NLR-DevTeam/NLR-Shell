package ui

import (
	"os"
	"path/filepath"

	"gioui.org/widget"

	"nlrshell/internal/droid"
)

// knownPlaces are the special folders the file picker names and pins by
// default: the shared storage and its standard folders.
func knownPlaces() []place {
	root := droid.StorageRoot()
	out := []place{{path: root, name: "内部存储", icon: icHome}}
	for _, k := range []struct {
		dir, name string
		ic        *widget.Icon
	}{
		{"Download", "下载", icDownload},
		{"Documents", "文档", icDocument},
		{"Pictures", "图片", icImage},
		{"DCIM", "相机", icImage},
	} {
		p := filepath.Join(root, k.dir)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, place{path: p, name: k.name, icon: k.ic})
		}
	}
	return out
}

// listDrives returns the shared storage and the SD cards and USB drives
// Android mounts under /storage.
func listDrives() []place {
	out := []place{{path: droid.StorageRoot(), name: "内部存储", icon: icDrive}}
	ents, _ := os.ReadDir("/storage")
	for _, e := range ents {
		switch n := e.Name(); n {
		case "emulated", "self":
		default:
			out = append(out, place{path: filepath.Join("/storage", n), name: n, icon: icUSB})
		}
	}
	return out
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
