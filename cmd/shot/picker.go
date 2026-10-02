package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

// settingsPickerShot clicks 浏览… in the settings dialog: the file picker
// must open above it and hand the chosen folder back to the field.
func (d *driver) settingsPickerShot(want func(string) bool) {
	// The settings dialog is centered and 650dp tall with a fixed layout;
	// 浏览 sits just inside its right edge, in the download-folder row
	// (see settingsDialog.Layout).
	right := (float32(d.size.X) + d.px(520)) / 2
	top := (float32(d.size.Y) - d.px(650)) / 2
	d.click(right-d.px(51), top+d.px(413), pointer.ButtonPrimary)
	d.until("settings picker", func() bool { return d.a.DialogCount() == 2 })
	if d.a.DialogCount() != 2 {
		fmt.Fprintln(os.Stderr, "picker: 浏览 did not open the picker")
		return
	}
	d.run(100 * time.Millisecond)
	if want("settings-picker") {
		d.shot("settings-picker")
	}
	// Confirm the current directory and check that the field took it.
	d.key(key.NameReturn, 0)
	d.until("picker closed", func() bool { return d.a.DialogCount() == 1 })
	if want("settings-picker") {
		d.shot("settings-picked")
	}
}

// pickerShot drives the built-in file picker: it lists the seeded home
// directory, navigates with the keyboard and checks the reported paths.
func (d *driver) pickerShot(want func(string) bool) {
	var chosen []string
	home, err := os.UserHomeDir()
	if err != nil {
		fail("home: %v", err)
	}
	// The picker dialog is centered, 660dp wide and about 530dp tall; its
	// path field sits 62dp below the dialog's top edge.
	top := (float32(d.size.Y) - d.px(530)) / 2
	d.a.OpenPicker("选择文件", false, func(paths []string) { chosen = paths })
	d.until("picker open", func() bool { return d.a.DialogCount() == 1 })
	d.run(100 * time.Millisecond)
	if want("picker") {
		d.shot("picker")
	}
	// Hidden entries are listed: private keys live in ~/.ssh.
	if names := d.a.PickerEntries(); !slices.Contains(names, ".ssh") {
		fail("hidden entries not listed: %v", names)
	}
	// The path field is editable: type a path and press Enter to go there.
	fieldY := top + d.px(62)
	// The path field spans the middle of the dialog, so the window centre
	// is inside it.
	d.click(float32(d.size.X)/2, fieldY, pointer.ButtonPrimary)
	d.clearField()
	d.typeText("~/docs\n")
	d.until("path field navigates", func() bool { return d.a.PickerPath() == filepath.Join(home, "docs") })
	d.key(key.NameDeleteBackward, 0)
	d.until("leave docs", func() bool { return d.a.PickerPath() == home })
	// Enter the docs/ row and come back; rows are found by name so that
	// hidden entries in the fixture cannot shift them.
	selectRow := func(name string) {
		idx := slices.Index(d.a.PickerEntries(), name)
		if idx < 0 {
			fail("%s not listed: %v", name, d.a.PickerEntries())
		}
		// The cursor starts above the first row.
		for range idx + 1 {
			d.key(key.NameDownArrow, 0)
		}
	}
	selectRow("docs")
	d.key(key.NameReturn, 0)
	d.until("enter docs", func() bool { return d.a.PickerPath() == filepath.Join(home, "docs") })
	d.key(key.NameDeleteBackward, 0)
	d.until("leave docs", func() bool { return d.a.PickerPath() == home })
	selectRow("notes.txt")
	d.key(key.NameReturn, 0)
	d.until("picker closed", func() bool { return d.a.DialogCount() == 0 })
	if len(chosen) != 1 || chosen[0] != filepath.Join(home, "notes.txt") {
		fail("file chosen %v", chosen)
	}
	// Folder mode reports the current directory when nothing is selected.
	chosen = nil
	d.a.OpenPicker("选择文件夹", true, func(paths []string) { chosen = paths })
	d.until("folder picker open", func() bool { return d.a.DialogCount() == 1 })
	d.run(100 * time.Millisecond)
	if want("picker-folders") {
		d.shot("picker-folders")
	}
	d.key(key.NameReturn, 0)
	d.until("folder picker closed", func() bool { return d.a.DialogCount() == 0 })
	if len(chosen) != 1 || chosen[0] != home {
		fail("folder chosen %v", chosen)
	}

	// Mouse: double click the docs/ row to walk into it. Row 0 starts
	// 102dp below the dialog's top edge and rows are 30dp tall.
	d.a.OpenPicker("选择文件", false, nil)
	d.until("picker open", func() bool { return d.a.DialogCount() == 1 })
	d.run(100 * time.Millisecond)
	idx := slices.Index(d.a.PickerEntries(), "docs")
	if idx < 0 {
		fail("docs not listed: %v", d.a.PickerEntries())
	}
	x, y := float32(d.size.X)/2, top+d.px(float32(102+30*idx))
	d.click(x, y, pointer.ButtonPrimary)
	d.click(x, y, pointer.ButtonPrimary)
	d.until("double click into docs", func() bool { return d.a.PickerPath() == filepath.Join(home, "docs") })
	d.key(key.NameEscape, 0)
	d.until("picker closed", func() bool { return d.a.DialogCount() == 0 })
}
