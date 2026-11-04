//go:build !android

package droid

const available = false

// SetView records the Gio view; 0 means it is gone.
func SetView(uintptr) {}

// KeepAlive starts (on) or stops the foreground service that keeps the
// sessions connected in the background.
func KeepAlive(on bool, text string) {}

// HasStorage reports whether shared storage may be read and written.
func HasStorage() bool { return true }

// RequestStorage asks the user for access to shared storage.
func RequestStorage() {}

// RequestNotifications asks for permission to show notifications.
func RequestNotifications() {}

// MoveToBack sends the app to the background.
func MoveToBack() {}

// Quit closes the app and ends the process.
func Quit() {}

// NightMode reports whether the system uses its dark theme.
func NightMode() (dark, ok bool) { return false, false }

// StorageRoot returns the primary shared storage directory.
func StorageRoot() string { return "" }
