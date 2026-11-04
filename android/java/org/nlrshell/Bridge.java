package org.nlrshell;

import android.Manifest;
import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.res.Configuration;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.provider.Settings;
import android.view.View;

// Bridge holds the Android calls the Go side makes through JNI. Methods take
// the application context or the Gio view (whose context is the activity)
// and are safe to call from any thread.
public final class Bridge {
	private Bridge() {}

	// keepAlive starts or stops the foreground service that keeps the
	// process and its network connections alive in the background. text is
	// shown in the notification.
	public static void keepAlive(Context ctx, boolean on, String text) {
		Intent it = new Intent(ctx, KeepAliveService.class);
		if (!on) {
			ctx.stopService(it);
			return;
		}
		it.putExtra(KeepAliveService.EXTRA_TEXT, text);
		try {
			if (Build.VERSION.SDK_INT >= 26) {
				ctx.startForegroundService(it);
			} else {
				ctx.startService(it);
			}
		} catch (RuntimeException e) {
			// Android 12+ refuses to start a foreground service while the
			// app is in the background; the next call from the foreground
			// will succeed.
		}
	}

	// hasStorage reports whether the app may read and write shared storage.
	public static boolean hasStorage(Context ctx) {
		if (Build.VERSION.SDK_INT >= 30) {
			return Environment.isExternalStorageManager();
		}
		return ctx.checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE) == PackageManager.PERMISSION_GRANTED;
	}

	// requestStorage asks for access to shared storage: the "all files
	// access" settings page on Android 11+, the runtime permission before.
	public static void requestStorage(View view) {
		final Activity act = (Activity) view.getContext();
		act.runOnUiThread(new Runnable() {
			public void run() {
				if (Build.VERSION.SDK_INT >= 30) {
					Intent it = new Intent(Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION,
						Uri.parse("package:" + act.getPackageName()));
					try {
						act.startActivity(it);
					} catch (RuntimeException e) {
						act.startActivity(new Intent(Settings.ACTION_MANAGE_ALL_FILES_ACCESS_PERMISSION));
					}
					return;
				}
				act.requestPermissions(new String[]{
					Manifest.permission.READ_EXTERNAL_STORAGE,
					Manifest.permission.WRITE_EXTERNAL_STORAGE,
				}, 1);
			}
		});
	}

	// requestNotifications asks for the notification permission that
	// Android 13+ needs to show the keep-alive notification.
	public static void requestNotifications(View view) {
		if (Build.VERSION.SDK_INT < 33) {
			return;
		}
		final Activity act = (Activity) view.getContext();
		if (act.checkSelfPermission("android.permission.POST_NOTIFICATIONS") == PackageManager.PERMISSION_GRANTED) {
			return;
		}
		act.runOnUiThread(new Runnable() {
			public void run() {
				act.requestPermissions(new String[]{"android.permission.POST_NOTIFICATIONS"}, 2);
			}
		});
	}

	// moveToBack sends the app to the background without finishing the
	// activity, as the home button does.
	public static void moveToBack(View view) {
		final Activity act = (Activity) view.getContext();
		act.runOnUiThread(new Runnable() {
			public void run() {
				act.moveTaskToBack(true);
			}
		});
	}

	// quit closes the activity, removes the app from the recent apps and
	// ends the process, sessions and all.
	public static void quit(View view) {
		final Activity act = (Activity) view.getContext();
		act.runOnUiThread(new Runnable() {
			public void run() {
				act.finishAndRemoveTask();
				android.os.Process.killProcess(android.os.Process.myPid());
			}
		});
	}

	// nightMode reports whether the system uses the dark theme.
	public static boolean nightMode(Context ctx) {
		int mode = ctx.getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK;
		return mode == Configuration.UI_MODE_NIGHT_YES;
	}

	// storageRoot returns the primary shared storage directory.
	public static String storageRoot() {
		return Environment.getExternalStorageDirectory().getAbsolutePath();
	}
}
