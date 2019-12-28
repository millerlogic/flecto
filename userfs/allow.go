package userfs

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bazil.org/fuse"
)

type userNotAllowedError fuse.Errno

func (userNotAllowedError) Error() string {
	return "Not allowed by user: permission denied"
}

func (e userNotAllowedError) Errno() fuse.Errno {
	return fuse.Errno(e)
}

// ErrUserNotAllowed is returned if the user denied access or timed out waiting for a response.
var ErrUserNotAllowed = userNotAllowedError(syscall.EACCES)

// UserAllow is how access is allowed or denied.
type UserAllow byte

func (allow UserAllow) Allowed() bool {
	switch allow {
	case UserAllowOnce:
	case UserAllowAll:
		return true
	}
	return false
}

const (
	UserAllowNone     UserAllow = iota
	UserAllowNoneOnce           // transient, only used by SetUserAllowed. good for user timeout.
	UserAllowOnce
	UserAllowAll
	//UserAllow15Min
	//UserAllow1Hr
)

// Note: PID can be 0 if not known or it is the OS.
type UserRequest struct {
	Deadline  time.Time
	Path      string
	Action    string
	UID, GID  uint32
	ThreadPID uint32 // The thread, or 0 if not known.
	PID       uint32 // The process, or 0 if not known.
}

func (req UserRequest) Command() string {
	cmd, _ := getCommand(req.PID)
	return cmd
}

// Returns 0 if not found.
func getProcessFromThreadPID(threadPID uint32) uint32 {
	x, _ := filepath.Glob(fmt.Sprintf("/proc/*/task/%v", threadPID))
	if len(x) == 0 {
		return 0
	}
	a := x[0][6:] // remove "/proc/"
	islash := strings.IndexByte(a, '/')
	b := a[:islash]
	processPID64, _ := strconv.ParseUint(b, 10, 32)
	return uint32(processPID64)
}

func getPID(threadPID uint32) uint32 {
	pid := getProcessFromThreadPID(threadPID)
	if pid != 0 {
		return pid
	}
	return threadPID
}

func getCommand(pid uint32) (string, bool) {
	data, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		bcmd := data
		inul := bytes.IndexByte(bcmd, 0)
		if inul != -1 {
			bcmd = bcmd[:inul]
		}
		return string(bcmd), true
	}
	return fmt.Sprintf("(PID %d)", pid), false
}

type hourTime float32

func (h hourTime) Time() time.Time {
	return time.Unix(int64(float64(h)*60*60), 0)
}

func toHourTime(t time.Time) hourTime {
	return hourTime(float64(t.Unix()) / 60 / 60)
}

type allowing struct {
	pid   uint32
	ts    hourTime
	allow UserAllow
}

type allows []allowing

const allowExpire = 7 * 24 * time.Hour

func (a *allows) ForPID(pid uint32) (UserAllow, bool) {
	now := time.Now()
	expire := now.Add(allowExpire)
	for i, ax := range *a {
		if ax.pid == pid {
			if ax.ts.Time().Before(expire) {
				ax.ts = toHourTime(now) // Update ts!
				(*a)[i] = ax
				return ax.allow, true
			}
			// It's expired, so don't use the allow value.
			// Don't bother removing it now, it's likely to be added again.
			break
		}
	}
	return 0, false
}

const maxAllows = 8

func (a *allows) Set(pid uint32, allow UserAllow) {
	lowestTS := hourTime(math.Inf(1))
	lowestIndex := -1
	for i, ax := range *a {
		if ax.pid == pid {
			ax.ts = toHourTime(time.Now()) // Update ts!
			ax.allow = allow
			(*a)[i] = ax
			return
		}
		if ax.ts < lowestTS {
			lowestTS = ax.ts
			lowestIndex = i
		}
	}
	if len(*a) >= maxAllows {
		// Hit maxAllows, remove the lowest ts.
		*a = append((*a)[:lowestIndex], (*a)[lowestIndex+1:]...)
	}
	*a = append(*a, allowing{pid, toHourTime(time.Now()), allow})
}

func (a *allows) Delete(pid uint32) bool {
	for i, ax := range *a {
		if ax.pid == pid {
			*a = append((*a)[:i], (*a)[i+1:]...)
			return true
		}
	}
	return false
}

func (a *allows) DeleteExpired() {
	now := time.Now()
	expire := now.Add(allowExpire)
	dest := 0
	for _, ax := range *a {
		if ax.ts.Time().Before(expire) {
			(*a)[dest] = ax
			dest++
		}
	}
	*a = (*a)[:dest]
}
