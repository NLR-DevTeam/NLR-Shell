// Package droid calls into the Android side of NLR Shell (the Java classes
// in android/java). On other platforms every function is a no-op that
// reports the feature as unavailable.
package droid

// Available reports whether the program runs on Android.
const Available = available
