// Package testenv provides information about what functionality is available in different testing environments run by the Go team.
//
// It is an internal package because these details are specific to the Go team's test setup (on build.golang.org) and not fundamental to tests in general.
package testenv

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"forkmat/internal/cfg"
	"forkmat/internal/goarch"
	"forkmat/internal/platform"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// newFileMode is the permission of the files that the tests write.
const newFileMode = 0o666

// SpecialBuildTypes are interesting build types that may affect linking.
type SpecialBuildTypes struct {
	Cgo, Asan, Msan, Race bool
}

var (
	// Save the original environment during init for use in checks. A test binary may modify its environment before calling HasExec to change its behavior (such as mimicking a command-line tool), and that modified environment might cause environment checks to behave erratically.
	origEnv = os.Environ()
	tryGoBuild = sync.OnceValue(func() error {
		// To run 'go build', we need to be able to exec a 'go' command. We somewhat arbitrarily choose to exec 'go tool -n compile' because that also confirms that cmd/go can find the compiler. (Before CL 472096, we sometimes ended up with cmd/go installed in the test environment without a cmd/compile it could use to actually build things.)
		goTool, err := goTool()
		if err != nil {
			return err
		}

		cmd := exec.Command(goTool, "tool", "-n", "compile")
		cmd.Env = origEnv

		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%v: %w", cmd, err)
		}

		out = bytes.TrimSpace(out)

		if len(out) == 0 {
			return fmt.Errorf("%v: no tool reported", cmd)
		}

		if _, err := exec.LookPath(string(out)); err != nil {
			return err
		}

		if platform.MustLinkExternal(runtime.GOOS, runtime.GOARCH, false) {
			// We can assume that we always have a complete Go toolchain available. However, this platform requires a C linker to build even pure Go programs, including tests. Do we have one in the test environment? (On Android, for example, the device running the test might not have a C toolchain installed.)
			//
			// If CC is set explicitly, assume that we do. Otherwise, use 'go env CC' to determine which toolchain it would use by default.
			if os.Getenv("CC") == "" {
				cmd := exec.Command(goTool, "env", "CC")
				cmd.Env = origEnv

				out, err := cmd.Output()
				if err != nil {
					return fmt.Errorf("%v: %w", cmd, err)
				}

				out = bytes.TrimSpace(out)

				if len(out) == 0 {
					return fmt.Errorf("%v: no CC reported", cmd)
				}

				_, err = exec.LookPath(string(out))

				return err
			}
		}

		return nil
	})
	findGOROOT = sync.OnceValues(func() (path string, err error) {
		if path := build.Default.GOROOT; path != "" {
			// If runtime.GOROOT() is non-empty, assume that it is valid.
			//
			// (It might not be: for example, the user may have explicitly set GOROOT to the wrong directory. But this case is rare, and if that happens the user can fix what they broke.)
			return path, nil
		}

		// runtime.GOROOT doesn't know where GOROOT is (perhaps because the test binary was built with -trimpath).
		//
		// Since this is internal/testenv, we can cheat and assume that the caller is a test of some package in a subdirectory of GOROOT/src. ('go test' runs the test in the directory containing the packaged under test.) That means that if we start walking up the tree, we should eventually find GOROOT/src/go.mod, and we can report the parent directory of that.
		//
		// Notably, this works even if we can't run 'go env GOROOT' as a subprocess.
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding GOROOT: %w", err)
		}

		dir := cwd

		for {
			parent := filepath.Dir(dir)
			if parent == dir {
				// dir is either "." or only a volume name.
				return "", errors.New("failed to locate GOROOT/src in any parent directory")
			}

			if base := filepath.Base(dir); base != "src" {
				dir = parent
				continue // dir cannot be GOROOT/src if it doesn't end in "src".
			}

			b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err != nil {
				if os.IsNotExist(err) {
					dir = parent
					continue
				}

				return "", fmt.Errorf("finding GOROOT: %w", err)
			}

			goMod := string(b)

			for goMod != "" {
				var line string

				line, goMod, _ = strings.Cut(goMod, "\n")

				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "module" && fields[1] == "std" {
					// Found "module std", which is the module declaration in GOROOT/src!
					return parent, nil
				}
			}
		}
	})
	goTool = sync.OnceValues(func() (string, error) {
		return exec.LookPath("go")
	})
	hasCgo = sync.OnceValue(func() bool {
		goTool, err := goTool()
		if err != nil {
			return false
		}

		cmd := exec.Command(goTool, "env", "CGO_ENABLED")
		cmd.Env = origEnv

		out, err := cmd.Output()
		if err != nil {
			panic(fmt.Sprintf("%v: %v", cmd, out))
		}

		ok, err := strconv.ParseBool(string(bytes.TrimSpace(out)))
		if err != nil {
			panic(fmt.Sprintf("%v: non-boolean output %q", cmd, out))
		}

		return ok
	})

	// NoSpecialBuildTypes indicates a standard, no cgo go build.
	NoSpecialBuildTypes SpecialBuildTypes
	flaky = flag.Bool("flaky", false, "run known-flaky tests too")
)

// Builder reports the name of the builder running this test. For example, "gotip-linux-amd64_avx512-test_only" or "go1.24-windows-arm64" on LUCI, or "linux-amd64" on our old infrastructure. Prefer using runtime.GOOS, runtime.GOARCH, race.Enabled, reading the OS version, checking CPU feature flags with internal/cpu, etc. over parsing builder names when possible. When matching builder names, prefer a fuzzy match instead of a strict comparison. If the test is not running on the build infrastructure, Builder returns the empty string.
func Builder() string {
	return os.Getenv("GO_BUILDER_NAME")
}

// CPUIsSlow reports whether the CPU running the test is suspected to be slow.
func CPUIsSlow() bool {
	switch runtime.GOARCH {
	case "arm", "mips", "mips64", "mips64le", "mipsle", "wasm":
		return true
	}

	return false
}

// CPUProfilingBroken returns true if CPU profiling has known issues on this platform.
func CPUProfilingBroken() bool {
	switch runtime.GOOS {
	case "plan9":
		// Profiling unimplemented.
		return true
	case "aix":
		// See https://golang.org/issue/45170.
		return true
	case "dragonfly", "illumos", "ios", "netbsd", "solaris":
		// See https://golang.org/issue/13841.
		return true
	case "openbsd":
		if runtime.GOARCH == "arm" || runtime.GOARCH == "arm64" {
			// See https://golang.org/issue/13841.
			return true
		}
	}

	return false
}

// CanInternalLink reports whether the current system can link programs with internal linking.
func CanInternalLink(withCgo bool) bool {
	return !platform.MustLinkExternal(runtime.GOOS, runtime.GOARCH, withCgo)
}

// GOROOT reports the path to the directory containing the root of the Go project source tree. This is normally equivalent to runtime.GOROOT, but works even if the test binary was built with -trimpath and cannot exec 'go env GOROOT'.
//
// If GOROOT cannot be found, GOROOT skips t if t is non-nil, or panics otherwise.
func GOROOT(tb testing.TB) string {
	tb.Helper()

	path, err := findGOROOT()
	if err != nil {
		if tb == nil {
			panic(err)
		}

		tb.Helper()
		tb.Skip(err)
	}

	return path
}

// HasCGO reports whether the current system can use cgo.
func HasCGO() bool {
	return hasCgo()
}

// HasExternalNetwork reports whether the current system can use external (non-localhost) networks.
func HasExternalNetwork() bool {
	return !testing.Short() && runtime.GOOS != "js" && runtime.GOOS != "wasip1"
}

// HasGoBuild reports whether the current system can build programs with “go build” and then run them with os.StartProcess or exec.Command.
func HasGoBuild() bool {
	if os.Getenv("GO_GCFLAGS") != "" {
		// It's too much work to require every caller of the go command to pass along "-gcflags="+os.Getenv("GO_GCFLAGS"). For now, if $GO_GCFLAGS is set, report that we simply can't run go build.
		return false
	}

	return tryGoBuild() == nil
}

// GoTool reports the path to the Go tool.
func GoTool() (string, error) {
	if !HasGoBuild() {
		return "", errors.New("platform cannot run go tool")
	}

	return goTool()
}

// HasGoRun reports whether the current system can run programs with “go run”.
func HasGoRun() bool {
	// For now, having go run and having go build are the same.
	return HasGoBuild()
}

// HasLink reports whether the current system can use os.Link.
func HasLink() bool {
	// From Android release M (Marshmallow), hard linking files is blocked and an attempt to call link() on a file will return EACCES.
	// - https://code.google.com/p/android-developer-preview/issues/detail?id=3150
	return runtime.GOOS != "plan9" && runtime.GOOS != "android"
}

// HasParallelism reports whether the current system can execute multiple threads in parallel. There is a copy of this function in cmd/dist/test.go.
func HasParallelism() bool {
	switch runtime.GOOS {
	case "js", "wasip1":
		return false
	}

	return true
}

// HasSymlink reports whether the current system can use os.Symlink.
func HasSymlink() bool {
	ok, _ := hasSymlink()

	return ok
}

// MustHaveBuildMode reports whether the current system can build programs in the given build mode. If not, MustHaveBuildMode calls t.Skip with an explanation.
func MustHaveBuildMode(tb testing.TB, buildmode string) {
	tb.Helper()

	if !platform.BuildModeSupported(runtime.Compiler, buildmode, runtime.GOOS, runtime.GOARCH) {
		tb.Helper()
		tb.Skipf("skipping test: build mode %s on %s/%s is not supported by the %s compiler", buildmode, runtime.GOOS, runtime.GOARCH, runtime.Compiler)
	}
}

// MustHaveCGO calls t.Skip if cgo is not available.
func MustHaveCGO(tb testing.TB) {
	tb.Helper()

	if !HasCGO() {
		tb.Helper()
		tb.Skipf("skipping test: no cgo")
	}
}

// MustHaveExternalNetwork checks that the current system can use external (non-localhost) networks. If not, MustHaveExternalNetwork calls t.Skip with an explanation.
func MustHaveExternalNetwork(tb testing.TB) {
	tb.Helper()

	if runtime.GOOS == "js" || runtime.GOOS == "wasip1" {
		tb.Helper()
		tb.Skipf("skipping test: no external network on %s", runtime.GOOS)
	}

	if testing.Short() {
		tb.Helper()
		tb.Skipf("skipping test: no external network in -short mode")
	}
}

// MustHaveGoBuild checks that the current system can build programs with “go build” and then run them with os.StartProcess or exec.Command. If not, MustHaveGoBuild calls t.Skip with an explanation.
func MustHaveGoBuild(tb testing.TB) {
	tb.Helper()

	if os.Getenv("GO_GCFLAGS") != "" {
		tb.Helper()
		tb.Skipf("skipping test: 'go build' not compatible with setting $GO_GCFLAGS")
	}

	if !HasGoBuild() {
		tb.Helper()
		tb.Skipf("skipping test: 'go build' unavailable: %v", tryGoBuild())
	}
}

// GoToolPath reports the path to the Go tool. It is a convenience wrapper around GoTool. If the tool is unavailable GoToolPath calls t.Skip. If the tool should be available and isn't, GoToolPath calls t.Fatal.
func GoToolPath(tb testing.TB) string {
	tb.Helper()
	MustHaveGoBuild(tb)

	path, err := GoTool()
	if err != nil {
		tb.Fatal(err)
	}

	// Add all environment variables that affect the Go command to test metadata. Cached test results will be invalidate when these variables change. See golang.org/issue/32285.
	for envVar := range strings.FieldsSeq(cfg.KnownEnv) {
		os.Getenv(envVar)
	}

	return path
}

// MustHaveGoRun checks that the current system can run programs with “go run”. If not, MustHaveGoRun calls t.Skip with an explanation.
func MustHaveGoRun(tb testing.TB) {
	tb.Helper()

	if !HasGoRun() {
		tb.Helper()
		tb.Skipf("skipping test: 'go run' not available on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// MustHaveLink reports whether the current system can use os.Link. If not, MustHaveLink calls t.Skip with an explanation.
func MustHaveLink(tb testing.TB) {
	tb.Helper()

	if !HasLink() {
		tb.Helper()
		tb.Skipf("skipping test: hardlinks are not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// MustHaveParallelism checks that the current system can execute multiple threads in parallel. If not, MustHaveParallelism calls t.Skip with an explanation.
func MustHaveParallelism(tb testing.TB) {
	tb.Helper()

	if !HasParallelism() {
		tb.Helper()
		tb.Skipf("skipping test: no parallelism available on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// MustHaveSource checks that the entire source tree is available under GOROOT. If not, it calls t.Skip with an explanation.
//
// This is needed when reading files from GOROOT that are not in ./testdata.
func MustHaveSource(tb testing.TB) {
	tb.Helper()

	if runtime.GOOS == "ios" {
		tb.Skip("skipping test: no source tree on " + runtime.GOOS)
	}

	if Builder() != "" {
		// The builders have the source tree available, and if they don't the tests should error out.
		return
	}

	// If not running on the builders, the test binary might have been copied to a target machine where the source tree isn't available.
	goroot, err := findGOROOT()
	if err != nil {
		tb.Skipf("skipping test: cannot locate GOROOT: %v", err)
	}

	if _, err := os.Stat(filepath.Join(goroot, "src", "go.mod")); err != nil {
		tb.Skipf("skipping test: GOROOT/src not available: %v", err)
	}
}

// MustHaveSymlink reports whether the current system can use os.Symlink. If not, MustHaveSymlink calls t.Skip with an explanation.
func MustHaveSymlink(tb testing.TB) {
	tb.Helper()

	if ok, reason := hasSymlink(); !ok {
		tb.Helper()
		tb.Skipf("skipping test: cannot make symlinks on %s/%s: %s", runtime.GOOS, runtime.GOARCH, reason)
	}
}

// MustInternalLink checks that the current system can link programs with internal linking. If not, MustInternalLink calls t.Skip with an explanation.
func MustInternalLink(tb testing.TB, with SpecialBuildTypes) {
	tb.Helper()

	if with.Asan || with.Msan || with.Race {
		tb.Skipf("skipping test: internal linking with sanitizers is not supported")
	}

	if !CanInternalLink(with.Cgo) {
		tb.Helper()

		if with.Cgo && CanInternalLink(false) {
			tb.Skipf("skipping test: internal linking on %s/%s is not supported with cgo", runtime.GOOS, runtime.GOARCH)
		}

		tb.Skipf("skipping test: internal linking on %s/%s is not supported", runtime.GOOS, runtime.GOARCH)
	}
}

// MustInternalLinkPIE checks whether the current system can link PIE binary using internal linking. If not, MustInternalLinkPIE calls t.Skip with an explanation.
func MustInternalLinkPIE(tb testing.TB) {
	tb.Helper()

	if !platform.InternalLinkPIESupported(runtime.GOOS, runtime.GOARCH) {
		tb.Helper()
		tb.Skipf("skipping test: internal linking for buildmode=pie on %s/%s is not supported", runtime.GOOS, runtime.GOARCH)
	}
}

// ParallelOn64Bit calls t.Parallel() unless there is a case that cannot be parallel. This function should be used when it is necessary to avoid t.Parallel on 32-bit machines, typically because the test uses lots of memory.
func ParallelOn64Bit(t *testing.T) {
	t.Helper()

	if goarch.PtrSize == 4 {
		return
	}

	t.Parallel()
}

// SetGODEBUG appends v to the GODEBUG environment variable for the duration of the test.
func SetGODEBUG(tb testing.TB, v string) {
	tb.Helper()
	tb.Setenv("GODEBUG", os.Getenv("GODEBUG")+","+v)
}

func SkipFlaky(tb testing.TB, issue int) {
	tb.Helper()

	if !*flaky {
		tb.Helper()
		tb.Skipf("skipping known flaky test without the -flaky flag; see golang.org/issue/%d", issue)
	}
}

func SkipFlakyNet(tb testing.TB) {
	tb.Helper()

	if v, _ := strconv.ParseBool(os.Getenv("GO_BUILDER_FLAKY_NET")); v {
		tb.Helper()
		tb.Skip("skipping test on builder known to have frequent network failures")
	}
}

// SkipIfOptimizationOff skips t if optimization is disabled.
func SkipIfOptimizationOff(tb testing.TB) {
	tb.Helper()

	if OptimizationOff() {
		tb.Helper()
		tb.Skip("skipping test with optimization disabled")
	}
}

// SkipIfShortAndSlow skips t if -short is set and the CPU running the test is suspected to be slow.
//
// (This is useful for CPU-intensive tests that otherwise complete quickly.)
func SkipIfShortAndSlow(tb testing.TB) {
	tb.Helper()

	if testing.Short() && CPUIsSlow() {
		tb.Helper()
		tb.Skipf("skipping test in -short mode on %s", runtime.GOARCH)
	}
}

// SyscallIsNotSupported reports whether err may indicate that a system call is not supported by the current platform or execution environment.
func SyscallIsNotSupported(err error) bool {
	return syscallIsNotSupported(err)
}

// WriteImportcfg writes an importcfg file used by the compiler or linker to dstPath containing entries for the file mappings in packageFiles, as well as for the packages transitively imported by the package(s) in pkgs.
//
// pkgs may include any package pattern that is valid to pass to 'go list', so it may also be a list of Go source files all in the same directory.
func WriteImportcfg(tb testing.TB, dstPath string, packageFiles map[string]string, pkgs ...string) {
	tb.Helper()

	icfg := &bytes.Buffer{}

	icfg.WriteString("# import config\n")

	for k, v := range packageFiles {
		fmt.Fprintf(icfg, "packagefile %s=%s\n", k, v)
	}

	if len(pkgs) > 0 {
		// Use 'go list' to resolve any missing packages and rewrite the import map.
		cmd := Command(tb, GoToolPath(tb), "list", "-export", "-deps", "-f", `{{if ne .ImportPath "command-line-arguments"}}{{if .Export}}{{.ImportPath}}={{.Export}}{{end}}{{end}}`)
		cmd.Args = append(cmd.Args, pkgs...)
		cmd.Stderr = &strings.Builder{}

		out, err := cmd.Output()
		if err != nil {
			tb.Fatalf("%v: %v\n%s", cmd, err, cmd.Stderr)
		}

		for line := range strings.SplitSeq(string(out), "\n") {
			if line == "" {
				continue
			}

			importPath, export, ok := strings.Cut(line, "=")
			if !ok {
				tb.Fatalf("invalid line in output from %v:\n%s", cmd, line)
			}

			if packageFiles[importPath] == "" {
				fmt.Fprintf(icfg, "packagefile %s=%s\n", importPath, export)
			}
		}
	}

	if err := os.WriteFile(dstPath, icfg.Bytes(), newFileMode); err != nil {
		tb.Fatal(err)
	}
}
