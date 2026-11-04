// Command apk builds the Android package of NLR Shell. It does what gogio
// does for Gio programs, but with the manifest, resources and Java classes
// in android/: gogio's fixed manifest cannot declare the foreground service
// that keeps sessions connected or the storage permissions. Run it from the
// repository root:
//
//	go run ./tools/apk [-arch arm64,arm,amd64] [-o NLRShell.apk]
//
// It needs ANDROID_HOME (an SDK with build-tools, a platform and the NDK)
// and a JDK 17 or newer: JAVA_HOME, the PATH, Android Studio's bundled one
// or one in the usual install folders.
package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/template"
)

const (
	minSDK    = 26 // Android 8: foreground services and adaptive icons.
	targetSDK = 35
)

// abis maps GOARCH to the Android ABI directory and the NDK clang prefix.
var abis = map[string]struct{ abi, clang string }{
	"arm64": {"arm64-v8a", "aarch64-linux-android"},
	"arm":   {"armeabi-v7a", "armv7a-linux-androideabi"},
	"amd64": {"x86_64", "x86_64-linux-android"},
	"386":   {"x86", "i686-linux-android"},
}

var (
	archFlag  = flag.String("arch", "arm64", "comma separated GOARCH values to build for (arm64, arm, amd64, 386)")
	outFlag   = flag.String("o", "NLRShell.apk", "output file")
	appID     = flag.String("appid", "com.nlrdevteam.nlrshell", "Android application ID")
	keystore  = flag.String("keystore", "", "keystore to sign with (default: ~/.android/debug.keystore, created when missing)")
	storePass = flag.String("storepass", "android", "keystore password")
	keyAlias  = flag.String("alias", "", "key alias in the keystore (default: the only one)")
	verbose   = flag.Bool("v", false, "print the commands that run")
)

type tools struct {
	sdk, ndk, buildTools, androidJar string
	javac, java, keytool             string
}

func main() {
	flag.Parse()
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, "apk:", err)
		os.Exit(1)
	}
}

func build() error {
	t, err := findTools()
	if err != nil {
		return err
	}
	version, err := appVersion()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "nlrshell-apk-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	archs := strings.Split(*archFlag, ",")
	for _, a := range archs {
		if _, ok := abis[a]; !ok {
			return fmt.Errorf("unknown architecture %q", a)
		}
	}

	// The Go libraries and the Java classes build in parallel.
	var wg sync.WaitGroup
	errs := make([]error, len(archs)+1)
	for i, a := range archs {
		wg.Go(func() { errs[i] = buildGo(t, tmp, a) })
	}
	wg.Go(func() { errs[len(archs)] = buildDex(t, tmp) })
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	linked, err := linkResources(t, tmp, version)
	if err != nil {
		return err
	}
	unsigned := filepath.Join(tmp, "unsigned.apk")
	if err := assemble(unsigned, linked, tmp, archs); err != nil {
		return err
	}
	if err := run(exec.Command(filepath.Join(t.buildTools, "zipalign"+exe()), "-f", "-P", "16", "4", unsigned, *outFlag)); err != nil {
		return err
	}
	if err := sign(t, *outFlag); err != nil {
		return err
	}
	st, err := os.Stat(*outFlag)
	if err != nil {
		return err
	}
	fmt.Printf("Built %s (%s, %.1f MB)\n", *outFlag, version, float64(st.Size())/(1<<20))
	return nil
}

// findTools locates the SDK, NDK and JDK programs.
func findTools() (*tools, error) {
	t := &tools{sdk: os.Getenv("ANDROID_HOME")}
	if t.sdk == "" {
		t.sdk = os.Getenv("ANDROID_SDK_ROOT")
	}
	if t.sdk == "" {
		return nil, errors.New("ANDROID_HOME is not set; point it to the Android SDK")
	}
	var err error
	if t.buildTools, err = latest(filepath.Join(t.sdk, "build-tools", "*")); err != nil {
		return nil, fmt.Errorf("no build-tools in the SDK: %v", err)
	}
	platform, err := latest(filepath.Join(t.sdk, "platforms", "android-*"))
	if err != nil {
		return nil, fmt.Errorf("no platform in the SDK: %v", err)
	}
	t.androidJar = filepath.Join(platform, "android.jar")
	if t.ndk = os.Getenv("ANDROID_NDK_ROOT"); t.ndk == "" {
		if t.ndk, err = latest(filepath.Join(t.sdk, "ndk", "*")); err != nil {
			return nil, fmt.Errorf("no NDK in the SDK (sdkmanager \"ndk;<version>\"): %v", err)
		}
	}
	home, err := findJDK()
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(home, "bin")
	t.javac, t.java, t.keytool = filepath.Join(bin, "javac"+exe()), filepath.Join(bin, "java"+exe()), filepath.Join(bin, "keytool"+exe())
	return t, nil
}

// findJDK returns the home of a JDK that d8 and apksigner run on (17 or
// newer), trying JAVA_HOME, the PATH, Android Studio's bundled runtime and
// the usual install folders in turn.
func findJDK() (string, error) {
	var homes []string
	if h := os.Getenv("JAVA_HOME"); h != "" {
		homes = append(homes, h)
	}
	if p, err := exec.LookPath("javac"); err == nil {
		if p, err := filepath.EvalSymlinks(p); err == nil {
			homes = append(homes, filepath.Dir(filepath.Dir(p)))
		}
	}
	var globs []string
	switch runtime.GOOS {
	case "windows":
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
			globs = append(globs, pf+`\Android\Android Studio\jbr`, pf+`\Java\*`, pf+`\Zulu\*`,
				pf+`\Eclipse Adoptium\*`, pf+`\Microsoft\jdk-*`, pf+`\BellSoft\*`)
		}
	case "darwin":
		globs = []string{"/Applications/Android Studio.app/Contents/jbr/Contents/Home", "/Library/Java/JavaVirtualMachines/*/Contents/Home"}
	default:
		globs = []string{"/opt/android-studio/jbr", "/usr/lib/jvm/*"}
	}
	for _, g := range globs {
		m, _ := filepath.Glob(g)
		// Newest first.
		slices.Reverse(m)
		homes = append(homes, m...)
	}
	for _, h := range homes {
		if jdkVersion(h) >= 17 {
			return h, nil
		}
	}
	return "", errors.New("no JDK 17 or newer found; install one and set JAVA_HOME")
}

// jdkVersion returns the major version of the JDK at home, or 0.
func jdkVersion(home string) int {
	if _, err := os.Stat(filepath.Join(home, "bin", "javac"+exe())); err != nil {
		return 0
	}
	out, err := exec.Command(filepath.Join(home, "bin", "java"+exe()), "-version").CombinedOutput()
	if err != nil {
		return 0
	}
	// java version "21.0.2" or openjdk version "1.8.0_402".
	m := regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`).FindSubmatch(out)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(string(m[1]))
	if v == 1 && len(m[2]) > 0 {
		v, _ = strconv.Atoi(string(m[2]))
	}
	return v
}

// latest returns the match of pattern with the highest version number.
func latest(pattern string) (string, error) {
	m, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(m) == 0 {
		return "", errors.New("none found at " + pattern)
	}
	key := func(p string) []int {
		var v []int
		for _, f := range regexp.MustCompile(`\d+`).FindAllString(filepath.Base(p), -1) {
			n, _ := strconv.Atoi(f)
			v = append(v, n)
		}
		return v
	}
	return slices.MaxFunc(m, func(a, b string) int { return slices.Compare(key(a), key(b)) }), nil
}

// appVersion reads the version from the Version variable of the ui package.
func appVersion() (string, error) {
	b, err := os.ReadFile(filepath.Join("internal", "ui", "app.go"))
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`var Version = "([0-9.]+)"`).FindSubmatch(b)
	if m == nil {
		return "", errors.New("no Version in internal/ui/app.go")
	}
	return string(m[1]), nil
}

// versionCode turns "1.2.3" into 10203.
func versionCode(v string) int {
	code := 0
	parts := strings.Split(v, ".")
	for i := range 3 {
		code *= 100
		if i < len(parts) {
			n, _ := strconv.Atoi(parts[i])
			code += n
		}
	}
	return code
}

func exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func run(cmd *exec.Cmd) error {
	if *verbose {
		fmt.Println(strings.Join(cmd.Args, " "))
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v\n%s", filepath.Base(cmd.Args[0]), err, out.String())
	}
	return nil
}

// buildGo compiles the program as libgio.so, the library GioActivity loads.
func buildGo(t *tools, tmp, arch string) error {
	a := abis[arch]
	bin := filepath.Join(t.ndk, "toolchains", "llvm", "prebuilt", runtime.GOOS+"-x86_64", "bin")
	clang := filepath.Join(bin, a.clang+strconv.Itoa(minSDK)+"-clang")
	if runtime.GOOS == "windows" {
		clang += ".cmd"
	}
	if _, err := os.Stat(clang); err != nil {
		return fmt.Errorf("NDK compiler missing: %v", err)
	}
	out := filepath.Join(tmp, "lib", a.abi, "libgio.so")
	cmd := exec.Command("go", "build", "-trimpath", "-buildmode=c-shared",
		// 16 KB page devices (Android 15+) need segments aligned to at
		// least that.
		`-ldflags=-s -w -extldflags "-Wl,-z,max-page-size=65536"`,
		"-o", out, ".")
	cmd.Env = append(os.Environ(),
		"GOOS=android", "GOARCH="+arch, "GOARM=7", "CGO_ENABLED=1",
		"CC="+clang, "CXX="+strings.Replace(clang, "-clang", "-clang++", 1))
	return run(cmd)
}

// buildDex compiles Gio's Java classes and those in android/java into
// classes.dex.
func buildDex(t *tools, tmp string) error {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", "gioui.org/app").Output()
	if err != nil {
		return fmt.Errorf("locating gioui.org/app: %v", err)
	}
	gioDir := strings.TrimSpace(string(out))
	srcs, _ := filepath.Glob(filepath.Join(gioDir, "*.java"))
	err = filepath.WalkDir(filepath.Join("android", "java"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".java") {
			srcs = append(srcs, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	classes := filepath.Join(tmp, "classes")
	// --release would compile against the JDK's own classes; -source and
	// -target with android.jar as the boot class path compile against
	// Android's.
	args := []string{"-nowarn", "-Xlint:-options", "-encoding", "UTF-8",
		"-source", "8", "-target", "8", "-bootclasspath", t.androidJar, "-d", classes}
	if err := run(exec.Command(t.javac, append(args, srcs...)...)); err != nil {
		return err
	}
	var classFiles []string
	filepath.WalkDir(classes, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".class") {
			classFiles = append(classFiles, p)
		}
		return err
	})
	dexDir := filepath.Join(tmp, "dex")
	if err := os.MkdirAll(dexDir, 0o755); err != nil {
		return err
	}
	d8 := filepath.Join(t.buildTools, "d8")
	if runtime.GOOS == "windows" {
		d8 += ".bat"
	}
	cmd := exec.Command(d8, append([]string{"--release", "--lib", t.androidJar,
		"--min-api", strconv.Itoa(minSDK), "--output", dexDir}, classFiles...)...)
	// d8 runs on the JDK found for javac.
	cmd.Env = append(os.Environ(), "JAVA_HOME="+filepath.Dir(filepath.Dir(t.java)))
	return run(cmd)
}

// linkResources compiles android/res and links it with the manifest into a
// package without code.
func linkResources(t *tools, tmp, version string) (string, error) {
	aapt2 := filepath.Join(t.buildTools, "aapt2"+exe())
	res := filepath.Join(tmp, "res.zip")
	if err := run(exec.Command(aapt2, "compile", "-o", res, "--dir", filepath.Join("android", "res"))); err != nil {
		return "", err
	}
	tmpl, err := template.ParseFiles(filepath.Join("android", "AndroidManifest.xml"))
	if err != nil {
		return "", err
	}
	var manifest bytes.Buffer
	err = tmpl.Execute(&manifest, map[string]any{
		"AppID":       *appID,
		"VersionCode": versionCode(version),
		"VersionName": version,
		"MinSDK":      minSDK,
		"TargetSDK":   targetSDK,
	})
	if err != nil {
		return "", err
	}
	mf := filepath.Join(tmp, "AndroidManifest.xml")
	if err := os.WriteFile(mf, manifest.Bytes(), 0o644); err != nil {
		return "", err
	}
	linked := filepath.Join(tmp, "linked.apk")
	err = run(exec.Command(aapt2, "link", "-I", t.androidJar, "--manifest", mf, "-o", linked, res))
	return linked, err
}

// assemble writes the unsigned package: the linked resources and manifest,
// classes.dex and the native libraries.
func assemble(dst, linked, tmp string, archs []string) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	zr, err := zip.OpenReader(linked)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, e := range zr.File {
		// resources.arsc stays uncompressed, as Android 11+ requires.
		if err := zw.Copy(e); err != nil {
			return err
		}
	}
	add := func(name, src string) error {
		r, err := os.Open(src)
		if err != nil {
			return err
		}
		defer r.Close()
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		return err
	}
	if err := add("classes.dex", filepath.Join(tmp, "dex", "classes.dex")); err != nil {
		return err
	}
	for _, a := range archs {
		abi := abis[a].abi
		if err := add("lib/"+abi+"/libgio.so", filepath.Join(tmp, "lib", abi, "libgio.so")); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sign signs the package with the given keystore, or with the debug key
// that Android Studio also uses, creating it when missing.
func sign(t *tools, apk string) error {
	ks, alias := *keystore, *keyAlias
	if ks == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		ks = filepath.Join(home, ".android", "debug.keystore")
		if alias == "" {
			alias = "androiddebugkey"
		}
		if _, err := os.Stat(ks); errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(ks), 0o755); err != nil {
				return err
			}
			err := run(exec.Command(t.keytool, "-genkeypair", "-keystore", ks,
				"-storepass", "android", "-keypass", "android", "-alias", alias,
				"-keyalg", "RSA", "-keysize", "2048", "-validity", "10000",
				"-dname", "CN=Android Debug,O=Android,C=US", "-noprompt"))
			if err != nil {
				return err
			}
		}
	}
	apksigner := filepath.Join(t.buildTools, "apksigner")
	if runtime.GOOS == "windows" {
		apksigner += ".bat"
	}
	// No v4 signature: it goes into a separate .idsig file that only
	// incremental installs from Android Studio use.
	args := []string{"sign", "--v4-signing-enabled", "false", "--ks", ks, "--ks-pass", "pass:" + *storePass}
	if alias != "" {
		args = append(args, "--ks-key-alias", alias)
	}
	cmd := exec.Command(apksigner, append(args, apk)...)
	cmd.Env = append(os.Environ(), "JAVA_HOME="+filepath.Dir(filepath.Dir(t.java)))
	return run(cmd)
}
