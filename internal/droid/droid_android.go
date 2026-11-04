package droid

/*
#include <jni.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// vm converts the JavaVM pointer Gio hands out as a uintptr.
static JavaVM *vm(uintptr_t p) {
	return (JavaVM *)p;
}

// bridge returns org.nlrshell.Bridge, loaded through the application's
// class loader: FindClass on a thread attached from native code only sees
// the system classes.
static jclass bridge(JNIEnv *env, jobject ctx) {
	static jclass cls;
	if (cls != NULL) {
		return cls;
	}
	jclass ctxCls = (*env)->GetObjectClass(env, ctx);
	jmethodID getLoader = (*env)->GetMethodID(env, ctxCls, "getClassLoader", "()Ljava/lang/ClassLoader;");
	jobject loader = (*env)->CallObjectMethod(env, ctx, getLoader);
	jclass loaderCls = (*env)->GetObjectClass(env, loader);
	jmethodID loadClass = (*env)->GetMethodID(env, loaderCls, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
	jstring name = (*env)->NewStringUTF(env, "org.nlrshell.Bridge");
	jobject c = (*env)->CallObjectMethod(env, loader, loadClass, name);
	(*env)->DeleteLocalRef(env, name);
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		return NULL;
	}
	cls = (jclass)(*env)->NewGlobalRef(env, c);
	return cls;
}

static int attach(JavaVM *vm, JNIEnv **env) {
	jint res = (*vm)->GetEnv(vm, (void **)env, JNI_VERSION_1_6);
	if (res == JNI_OK) {
		return 0;
	}
	if ((*vm)->AttachCurrentThread(vm, env, NULL) != JNI_OK) {
		return -1;
	}
	return 1;
}

static void detach(JavaVM *vm) {
	(*vm)->DetachCurrentThread(vm);
}

static jmethodID method(JNIEnv *env, jclass cls, const char *name, const char *sig) {
	return (*env)->GetStaticMethodID(env, cls, name, sig);
}

static int clearException(JNIEnv *env) {
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		return 1;
	}
	return 0;
}

static void callKeepAlive(JNIEnv *env, jclass cls, jobject ctx, jboolean on, const char *text) {
	jmethodID m = method(env, cls, "keepAlive", "(Landroid/content/Context;ZLjava/lang/String;)V");
	jstring s = (*env)->NewStringUTF(env, text);
	(*env)->CallStaticVoidMethod(env, cls, m, ctx, on, s);
	(*env)->DeleteLocalRef(env, s);
}

static jboolean callBoolCtx(JNIEnv *env, jclass cls, const char *name, jobject ctx) {
	jmethodID m = method(env, cls, name, "(Landroid/content/Context;)Z");
	return (*env)->CallStaticBooleanMethod(env, cls, m, ctx);
}

static void callVoidView(JNIEnv *env, jclass cls, const char *name, jobject view) {
	jmethodID m = method(env, cls, name, "(Landroid/view/View;)V");
	(*env)->CallStaticVoidMethod(env, cls, m, view);
}

// callString calls a static String method without arguments and returns a
// malloc'ed copy of the result, or NULL.
static char *callString(JNIEnv *env, jclass cls, const char *name) {
	jmethodID m = method(env, cls, name, "()Ljava/lang/String;");
	jstring s = (jstring)(*env)->CallStaticObjectMethod(env, cls, m);
	if (s == NULL) {
		return NULL;
	}
	const char *utf = (*env)->GetStringUTFChars(env, s, NULL);
	size_t n = strlen(utf);
	char *out = malloc(n + 1);
	memcpy(out, utf, n + 1);
	(*env)->ReleaseStringUTFChars(env, s, utf);
	(*env)->DeleteLocalRef(env, s);
	return out;
}
*/
import "C"

import (
	"runtime"
	"sync/atomic"
	"unsafe"

	"gioui.org/app"
)

const available = true

// view is the Gio view as a JNI global reference owned by Gio, or 0 while
// the activity is gone.
var view atomic.Uintptr

// SetView records the Gio view from an app.AndroidViewEvent; 0 means it is
// gone.
func SetView(v uintptr) { view.Store(v) }

// call runs f with a JNI environment and the Bridge class. It reports false
// when the class cannot be loaded.
func call(f func(env *C.JNIEnv, cls C.jclass, ctx C.jobject)) bool {
	vm := C.vm(C.uintptr_t(app.JavaVM()))
	ctx := C.jobject(app.AppContext())
	if vm == nil || ctx == 0 {
		return false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var env *C.JNIEnv
	switch C.attach(vm, &env) {
	case -1:
		return false
	case 1:
		defer C.detach(vm)
	}
	cls := C.bridge(env, ctx)
	if cls == 0 {
		return false
	}
	f(env, cls, ctx)
	return C.clearException(env) == 0
}

// callView runs a Bridge method that takes the Gio view.
func callView(name string) {
	v := view.Load()
	if v == 0 {
		return
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	call(func(env *C.JNIEnv, cls C.jclass, _ C.jobject) {
		C.callVoidView(env, cls, cname, C.jobject(v))
	})
}

// KeepAlive starts (on) or stops the foreground service that keeps the
// sessions connected in the background.
func KeepAlive(on bool, text string) {
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	call(func(env *C.JNIEnv, cls C.jclass, ctx C.jobject) {
		var b C.jboolean
		if on {
			b = C.JNI_TRUE
		}
		C.callKeepAlive(env, cls, ctx, b, ctext)
	})
}

func boolCtx(name string) (v, ok bool) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	ok = call(func(env *C.JNIEnv, cls C.jclass, ctx C.jobject) {
		v = C.callBoolCtx(env, cls, cname, ctx) == C.JNI_TRUE
	})
	return v, ok
}

// HasStorage reports whether shared storage may be read and written.
func HasStorage() bool {
	v, _ := boolCtx("hasStorage")
	return v
}

// RequestStorage asks the user for access to shared storage.
func RequestStorage() { callView("requestStorage") }

// RequestNotifications asks for permission to show notifications.
func RequestNotifications() { callView("requestNotifications") }

// MoveToBack sends the app to the background.
func MoveToBack() { callView("moveToBack") }

// Quit closes the app and ends the process.
func Quit() { callView("quit") }

// NightMode reports whether the system uses its dark theme.
func NightMode() (dark, ok bool) { return boolCtx("nightMode") }

// StorageRoot returns the primary shared storage directory.
func StorageRoot() string {
	var s string
	cname := C.CString("storageRoot")
	defer C.free(unsafe.Pointer(cname))
	call(func(env *C.JNIEnv, cls C.jclass, _ C.jobject) {
		if p := C.callString(env, cls, cname); p != nil {
			s = C.GoString(p)
			C.free(unsafe.Pointer(p))
		}
	})
	if s == "" {
		s = "/storage/emulated/0"
	}
	return s
}
