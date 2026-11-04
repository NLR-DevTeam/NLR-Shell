package org.nlrshell;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;

// KeepAliveService runs in the foreground while SSH sessions are open, so
// Android neither kills the process nor cuts its network when the app is in
// the background. It holds a partial wake lock and a Wi-Fi lock for the same
// reason; the Go side stops it when the last session closes.
public final class KeepAliveService extends Service {
	static final String EXTRA_TEXT = "text";
	private static final String CHANNEL = "sessions";
	private static final int ID = 1;

	private PowerManager.WakeLock wake;
	private WifiManager.WifiLock wifi;

	@Override public IBinder onBind(Intent intent) {
		return null;
	}

	@Override public int onStartCommand(Intent intent, int flags, int startId) {
		String text = intent != null ? intent.getStringExtra(EXTRA_TEXT) : null;
		if (text == null) {
			text = "SSH 会话保持连接中";
		}
		Notification n = notification(text);
		if (Build.VERSION.SDK_INT >= 34) {
			startForeground(ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
		} else {
			startForeground(ID, n);
		}
		acquireLocks();
		// The Go side lives in this process; if Android kills it there is
		// nothing to restart.
		return START_NOT_STICKY;
	}

	@Override public void onDestroy() {
		releaseLocks();
		super.onDestroy();
	}

	// onTaskRemoved runs when the user swipes the app away from the recent
	// apps: the sessions go with it.
	@Override public void onTaskRemoved(Intent rootIntent) {
		stopSelf();
		android.os.Process.killProcess(android.os.Process.myPid());
	}

	private Notification notification(String text) {
		NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
		if (Build.VERSION.SDK_INT >= 26 && nm.getNotificationChannel(CHANNEL) == null) {
			NotificationChannel ch = new NotificationChannel(CHANNEL, "SSH 会话", NotificationManager.IMPORTANCE_LOW);
			ch.setDescription("后台保持 SSH 连接时显示");
			ch.setShowBadge(false);
			nm.createNotificationChannel(ch);
		}
		Intent open = getPackageManager().getLaunchIntentForPackage(getPackageName());
		PendingIntent pi = PendingIntent.getActivity(this, 0, open,
			PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
		Notification.Builder b = Build.VERSION.SDK_INT >= 26
			? new Notification.Builder(this, CHANNEL)
			: new Notification.Builder(this);
		// The resource is looked up by name: the build does not generate an
		// R class.
		int icon = getResources().getIdentifier("ic_notify", "drawable", getPackageName());
		return b.setSmallIcon(icon)
			.setContentTitle("NLR Shell")
			.setContentText(text)
			.setContentIntent(pi)
			.setOngoing(true)
			.setShowWhen(false)
			.build();
	}

	private void acquireLocks() {
		if (wake == null) {
			PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
			wake = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "nlrshell:sessions");
			wake.setReferenceCounted(false);
			wake.acquire();
		}
		if (wifi == null) {
			WifiManager wm = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
			if (wm != null) {
				int mode = Build.VERSION.SDK_INT >= 29
					? WifiManager.WIFI_MODE_FULL_HIGH_PERF
					: WifiManager.WIFI_MODE_FULL;
				wifi = wm.createWifiLock(mode, "nlrshell:sessions");
				wifi.setReferenceCounted(false);
				wifi.acquire();
			}
		}
	}

	private void releaseLocks() {
		if (wake != null) {
			wake.release();
			wake = null;
		}
		if (wifi != null) {
			wifi.release();
			wifi = null;
		}
	}
}
