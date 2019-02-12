package subprocfs

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/millerlogic/loopback"
)

type FS struct {
	fs *loopback.FS

	ilock     sync.Mutex
	infos     []ProcInfo
	limitPids bool
	//allowPids   map[uint32]struct{}
	lastpid       uint32
	lastpidsMap   map[uint32]struct{}
	lastpidExpire time.Time
}

func newFS(srcPath string, infos ...ProcInfo) *FS {
	return &FS{
		fs:    loopback.New(srcPath),
		infos: append([]ProcInfo(nil), infos...),
	}
}

func New(infos ...ProcInfo) *FS {
	return newFS("/proc", infos...)
}

func (f *FS) SetLogger(logger interface {
	Print(v ...interface{})
	Printf(format string, v ...interface{})
}) {
	f.fs.SetLogger(logger)
}

// LimitPIDs will limit pids accessible in the subprocfs (i.e. /proc/PID)
func (f *FS) LimitPIDs(yes bool) {
	f.ilock.Lock()
	f.limitPids = yes
	f.ilock.Unlock()
}

/*
// AllowPID allows or disallows (allow true/false) a PID to be accessed in the subprocfs.
// If the PID is ever not found in the full /proc listing, it is automatically removed.
func (f *FS) AllowPID(pid uint32, allow bool) {
	f.ilock.Lock()
	defer f.ilock.Unlock()
	if allow {
		if f.allowPids == nil {
			f.allowPids = make(map[uint32]struct{})
		}
		f.allowPids[pid] = struct{}{}
	} else {
		if f.allowPids != nil {
			delete(f.allowPids, pid)
		}
	}
}
*/

type overrideReadOnlyErr fuse.Errno

func (overrideReadOnlyErr) Error() string {
	return "Readonly file: Permission denied"
}

func (e overrideReadOnlyErr) Errno() fuse.Errno {
	return fuse.Errno(e)
}

var ErrOverrideReadOnly = overrideReadOnlyErr(syscall.EACCES)

// returns nil if not an override.
func (f *FS) getOverride(path string) (string, bool) {
	f.ilock.Lock()
	defer f.ilock.Unlock()
	for _, info := range f.infos {
		if info.pathMatches(path) {
			return info.getContent(path), true
		}
	}
	return "", false
}

// returns nil, nil if no pid limiting in effect!
func (f *FS) getPidsMap(pid uint32) (map[uint32]struct{}, error) {
	f.ilock.Lock()
	defer f.ilock.Unlock()
	if !f.limitPids {
		return nil, nil
	}
	now := time.Now()
	if pid == f.lastpid && f.lastpidsMap != nil && now.Before(f.lastpidExpire) {
		return f.lastpidsMap, nil
	}
	m, err := GetRelatedPIDsMap("/proc", pid)
	if err != nil {
		return nil, err
	}
	f.lastpidsMap = m
	f.lastpid = pid
	f.lastpidExpire = now.Add(time.Second / 32)
	return m, nil
}

func (f *FS) isPidVisible(fromPid, testPid uint32) (bool, error) {
	m, err := f.getPidsMap(fromPid)
	if err != nil {
		return false, err
	}
	if m == nil {
		return true, nil
	}
	_, ok := m[testPid]
	return ok, nil
}

var pidPathRegexp = regexp.MustCompile(`^/proc/([0-9]+)(/.*|$)`)

// returns 0 if not a pid path, or returns the pid.
func parsePidPath(path string) uint32 {
	x := pidPathRegexp.FindStringSubmatch(path)
	if len(x) >= 2 {
		spid := x[1]
		pid64, err := strconv.ParseUint(spid, 10, 32)
		if err == nil {
			pid := uint32(pid64)
			return pid
		}
	}
	return 0
}

func isPidPath(path string) bool {
	return pidPathRegexp.MatchString(path)
}

// returns false if: not a pid path, or not hidden.
func (f *FS) isPidPathHidden(fromPid uint32, path string) bool {
	pid := parsePidPath(path)
	if pid != 0 {
		visible, err := f.isPidVisible(fromPid, pid)
		if err == nil {
			return !visible
		}
	}
	return false
}

type procPidPathHiddenErr fuse.Errno

func (procPidPathHiddenErr) Error() string {
	return "/proc/PID hidden: No such file or directory"
}

func (e procPidPathHiddenErr) Errno() fuse.Errno {
	return fuse.Errno(e)
}

var ErrProcPidPathHidden = procPidPathHiddenErr(syscall.ENOENT)

func (f *FS) Root() (fs.Node, error) {
	n, err := f.fs.Root()
	if err != nil {
		return nil, err
	}
	return &spNode{f: f, node: n.(*loopback.Node)}, nil
}

func (f *FS) Statfs(ctx context.Context, req *fuse.StatfsRequest, resp *fuse.StatfsResponse) error {
	return f.fs.Statfs(ctx, req, resp)
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

type spNode struct {
	f    *FS
	node *loopback.Node
}

func (n *spNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	err := n.node.Attr(ctx, attr)
	if err != nil {
		return err
	}
	attr.Valid = 0
	/* // procfs shows size of 0 anyway.
	info := n.f.getOverride(n.node.GetRealPath())
	if info != nil {
		data := info.GetContent()
		attr.Size = uint64(len(data))
	}
	*/
	return nil
}

func (n *spNode) Forget() {
	n.node.Forget()
}

func (n *spNode) Access(ctx context.Context, a *fuse.AccessRequest) error {
	threadPID := a.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return ErrProcPidPathHidden
	}
	return n.node.Access(ctx, a)
}

func (n *spNode) Lookup(ctx context.Context, name string) (fs.Node, error) {
	node, err := n.node.Lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	return &spNode{f: n.f, node: node.(*loopback.Node)}, nil
}

func (n *spNode) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return nil, ErrProcPidPathHidden
	}
	data, ok := n.f.getOverride(n.node.GetRealPath())
	if ok {
		resp.Flags |= fuse.OpenDirectIO
		return &ohandle{data}, nil
	}
	handle, err := n.node.Open(ctx, req, resp)
	resp.Flags |= fuse.OpenDirectIO
	return &lhandle{handle.(*loopback.Handle), n.f, n.node.GetRealPath(), processPID}, err
}

func (n *spNode) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	_, ok := n.f.getOverride(req.Name)
	if ok {
		return nil, nil, ErrOverrideReadOnly
	}
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return nil, nil, fuse.Errno(syscall.EACCES)
	}
	node, handle, err := n.node.Create(ctx, req, resp)
	if err != nil {
		return nil, nil, err
	}
	resp.Flags |= fuse.OpenDirectIO
	return &spNode{f: n.f, node: node.(*loopback.Node)},
		&lhandle{handle.(*loopback.Handle), n.f, n.node.GetRealPath(), processPID},
		nil
}

func (n *spNode) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return nil, fuse.Errno(syscall.EACCES)
	}
	node, err := n.node.Mkdir(ctx, req)
	if err != nil {
		return nil, err
	}
	return &spNode{f: n.f, node: node.(*loopback.Node)}, nil
}

func (n *spNode) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return ErrProcPidPathHidden
	}
	return n.node.Remove(ctx, req)
}

func (n *spNode) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return ErrProcPidPathHidden
	}
	return n.node.Setattr(ctx, req, resp)
}

func (n *spNode) Rename(ctx context.Context, req *fuse.RenameRequest, newDir fs.Node) error {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return ErrProcPidPathHidden
	}
	return n.node.Rename(ctx, req, newDir)
}

func (n *spNode) Readlink(ctx context.Context, req *fuse.ReadlinkRequest) (string, error) {
	threadPID := req.Pid
	processPID := getProcessFromThreadPID(threadPID)
	if n.f.isPidPathHidden(processPID, n.node.GetRealPath()) {
		return "", ErrProcPidPathHidden
	}
	if n.node.GetRealPath() == "/proc/self" {
		return strconv.FormatUint(uint64(processPID), 10), nil
	}
	if n.node.GetRealPath() == "/proc/thread-self" {
		return strconv.FormatUint(uint64(processPID), 10) + "/task/" + strconv.FormatUint(uint64(threadPID), 10), nil
	}
	return n.node.Readlink(ctx, req)
}

type lhandle struct {
	*loopback.Handle
	f              *FS
	path           string
	fromProcessPid uint32
}

func (h *lhandle) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	dents, err := h.Handle.ReadDirAll(ctx)
	if h.path == "/proc" {
		var newdents []fuse.Dirent
		for i, dent := range dents {
			if h.f.isPidPathHidden(h.fromProcessPid, "/proc/"+dent.Name) {
				if newdents == nil {
					newdents = append([]fuse.Dirent(nil), dents[:i]...)
				}
			} else {
				if newdents != nil {
					newdents = append(newdents, dent)
				}
			}
		}
		if newdents != nil {
			return newdents, err
		}
	}
	return dents, err
}

type ohandle struct {
	data string
}

func (h *ohandle) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	return nil
}

func (h *ohandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	start := req.Offset
	if start > int64(len(h.data)) {
		start = int64(len(h.data))
	}
	end := start + int64(req.Size)
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	resp.Data = []byte(h.data[start:end])
	return nil
}

/*
func (h *ohandle) ReadAll(ctx context.Context) ([]byte, error) {
	return []byte(h.data), nil
}
*/

func (h *ohandle) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	return nil, fuse.Errno(syscall.ENOTDIR)
}

func (h *ohandle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	return nil
}

func (h *ohandle) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	return ErrOverrideReadOnly
}
