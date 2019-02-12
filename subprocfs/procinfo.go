package subprocfs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	proc "github.com/c9s/goprocinfo/linux"
	"github.com/pkg/errors"
)

type procInfo interface {
	pathMatches(path string) bool
	getContent(path string) string
}

type ProcInfo procInfo

type procInfoPair struct {
	pathPattern, content string
}

func (x procInfoPair) pathMatches(path string) bool {
	matches, _ := filepath.Match(x.pathPattern, path)
	return matches
}

func (x procInfoPair) getContent(path string) string {
	return x.content
}

func NewProcInfo(pathPattern string, content string) ProcInfo {
	return procInfoPair{pathPattern, content}
}

type procMemInfo struct {
	memLimit uint64
	lowAvail uint64
	err      error
}

func (x *procMemInfo) Err() error {
	return x.err
}

func (x *procMemInfo) pathMatches(path string) bool {
	return path == "/proc/meminfo"
}

func (x *procMemInfo) getContent(path string) string {
	meminfo, err := proc.ReadMemInfo("/proc/meminfo") // note: everything in kB
	if err != nil {
		x.err = err
	} else {
	}
	total := meminfo.MemTotal * 1024
	avail := meminfo.MemAvailable * 1024
	if avail < x.lowAvail {
		x.lowAvail = avail
	}
	availLimit := x.memLimit
	if avail-x.lowAvail > total/4 || avail < total/4 {
		a := (float64(avail) / float64(total)) * float64(x.memLimit)
		//b := float64(avail - x.lowAvail) / ...
		availLimit = uint64(a)
	}
	buf := strings.Builder{}
	// See https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/Documentation/filesystems/proc.txt?id=HEAD#n853
	fmt.Fprintf(&buf, "MemTotal:       %8d kB\n", x.memLimit/1024)
	fmt.Fprintf(&buf, "MemFree:        %8d kB\n", availLimit/4/1024)
	fmt.Fprintf(&buf, "MemAvailable:   %8d kB\n", availLimit/1024)
	fmt.Fprintf(&buf, "Buffers:               0 kB\n") // memory usage by block devices
	fmt.Fprintf(&buf, "Cached:         %8d kB\n", availLimit/4*3/1024)
	fmt.Fprintf(&buf, "SwapCache:             0 kB\n")
	fmt.Fprintf(&buf, "Active:         %8d kB\n", x.memLimit/4/1024)
	fmt.Fprintf(&buf, "Inactive:       %8d kB\n", availLimit/2/1024)
	fmt.Fprintf(&buf, "Active(anon):   %8d kB\n", 0)
	fmt.Fprintf(&buf, "Inactive(anon):        0 kB\n")
	fmt.Fprintf(&buf, "Active(file):   %8d kB\n", 0)
	fmt.Fprintf(&buf, "Inactive(file): %8d kB\n", 0)
	fmt.Fprintf(&buf, "Unevictable:           0 kB\n")
	fmt.Fprintf(&buf, "Mlocked:               0 kB\n")
	fmt.Fprintf(&buf, "SwapTotal:             0 kB\n")
	fmt.Fprintf(&buf, "SwapFree:              0 kB\n")
	fmt.Fprintf(&buf, "Dirty:                 0 kB\n")
	fmt.Fprintf(&buf, "Writeback:             0 kB\n")
	fmt.Fprintf(&buf, "AnonPages:      %8d kB\n", 0)
	fmt.Fprintf(&buf, "Mapped:         %8d kB\n", 0)
	fmt.Fprintf(&buf, "Shmem:          %8d kB\n", 0) // TODO: shmem
	if x.err != nil {
		fmt.Fprintf(&buf, "_Error:                0 kB %s\n", strings.Replace(err.Error(), "\n", " ", -1))
	}
	return buf.String()
}

// MemInfo limits the memory reported by /proc/meminfo
// Note that this is a very naive implementation and might not reflect actual free or available memory.
func MemInfo(memLimit int) ProcInfo {
	x := &procMemInfo{
		memLimit: uint64(memLimit),
	}
	meminfo, err := proc.ReadMemInfo("/proc/meminfo") // note: everything in kB
	if err != nil {
		x.err = err
		x.memLimit = 0
	} else {
		total := meminfo.MemTotal * 1024
		x.lowAvail = meminfo.MemAvailable * 1024
		if memLimit <= 0 || uint64(memLimit) > total {
			x.memLimit = total
			x.err = errors.New("Invalid memory limit provided")
		}
	}
	return x
}

type procCPUInfo struct {
	maxCPUs int
}

func (x *procCPUInfo) pathMatches(path string) bool {
	return path == "/proc/cpuinfo"
}

func (x *procCPUInfo) getContent(path string) string {
	buf := strings.Builder{}
	f, err := os.Open("/proc/cpuinfo")
	if err == nil {
		defer f.Close()
		scan := bufio.NewScanner(f)
		numCPUs := 0
		newCPU := true
		for scan.Scan() {
			line := scan.Text()
			if line == "" {
				newCPU = true
				continue
			}
			if newCPU {
				newCPU = false
				numCPUs++
				if numCPUs > x.maxCPUs {
					break
				}
				if numCPUs != 1 {
					buf.WriteByte('\n')
				}
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		err = scan.Err()
		if err == nil {
			if numCPUs < x.maxCPUs {
				err = errors.New("Invalid CPUs provided")
			}
			if err == nil {
				err = f.Close()
			}
		}
	}
	if err != nil {
		fmt.Fprintf(&buf, "_error      : %s\n", strings.Replace(err.Error(), "\n", " ", -1))
	}
	buf.WriteByte('\n')
	return buf.String()
}

// CPUInfo limits the number of visible CPUs (cores/threads)
func CPUInfo(maxCPUs int) ProcInfo {
	return &procCPUInfo{maxCPUs: maxCPUs}
}

type procMounts struct {
	rpath, reroot string
}

func (x *procMounts) pathMatches(path string) bool {
	matches, _ := filepath.Match("/proc/[0-9]*/mounts", path)
	return matches
}

func (x *procMounts) getContent(path string) string {
	if x.rpath == "" {
		return ""
	}
	buf := strings.Builder{}
	mounts, err := proc.ReadMounts("/proc/mounts")
	if err == nil {
		for _, mnt := range mounts.Mounts {
			hasPrefix := strings.HasPrefix(mnt.MountPoint, x.rpath)
			if hasPrefix && (len(mnt.MountPoint) == len(x.rpath) || mnt.MountPoint[len(x.rpath)] == '/') {
				mountPoint := mnt.MountPoint
				if x.reroot != "" {
					mountPoint = filepath.Join(x.reroot, mountPoint[len(x.rpath):])
				}
				fmt.Fprintf(&buf, "%s %s %s %s\n", mnt.Device, mountPoint, mnt.FSType, mnt.Options)
			}
		}
	}
	return buf.String()
}

// RestrictMounts excludes mounts not under path from /proc/PID/mounts (and /proc/mounts)
// empty string can be used to exclude all mounts.
// reroot can be used to re-root the mounts under path, or empty string to use actual path.
// For example: RestrictMounts("/foo/bar", "/qux") will exclude "/hello" and convert "/foo/bar/baz" into "/qux/baz"
func RestrictMounts(path string, reroot string) ProcInfo {
	return &procMounts{rpath: path, reroot: reroot}
}

// EmptyMountInfo returns empty contents for /proc/PID/mountinfo files.
// This can be prudent when employing RestrictMounts.
func EmptyMountInfo() ProcInfo {
	return procInfoPair{"/proc/[0-9]*/mountinfo", ""}
}

// EmptyMountStats returns empty contents for /proc/PID/mountstats files.
func EmptyMountStats() ProcInfo {
	return procInfoPair{"/proc/[0-9]*/mountstats", ""}
}
