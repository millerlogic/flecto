package userfs

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/millerlogic/loopback"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
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
	ThreadPID uint32
}

// FS for user files, such as their home, documents, other personal files.
type FS struct {
	fs      *loopback.FS
	newPath string

	alock          sync.RWMutex
	allowed        map[string]UserAllow // paths allowed.
	await          map[string]chan struct{}
	allowReqs      chan<- UserRequest
	autoAllowUntil time.Time // auto respond to requests with userAutoAllow until this time is hit.
	autoAllow      UserAllow
}

var _ fs.FS = &FS{}
var _ fs.FSStatfser = &FS{}

// New user FS.
func New(srcPath, newPath string) *FS {
	return &FS{
		fs:      loopback.New(srcPath),
		newPath: newPath,
		allowed: make(map[string]UserAllow),
		await:   make(map[string]chan struct{}),
	}
}

// SetLogger sets the logger.
func (f *FS) SetLogger(logger interface {
	Print(v ...interface{})
	Printf(format string, v ...interface{})
}) {
	f.fs.SetLogger(logger)
}

// AutoUserAllowRequests -
// While the current time is less than until, any user allow reqeusts are suppressed and auto replied with allow.
// To cancel early, call again with a time not in the future (such as Now, or time.Time's zero value)
// Note: any previous and future SetUserAllowed calls still apply! they override the auto requests.
func (f *FS) AutoUserAllowRequests(allow UserAllow, until time.Time) {
	f.alock.Lock()
	defer f.alock.Unlock()
	f.autoAllow = allow
	f.autoAllowUntil = until
	if time.Now().Before(until) {
		// See if outstanding requests, auto reply to them:
		for path, aw := range f.await {
			delete(f.await, path)
			close(aw)
		}
	}
}

// AcceptUserAllowRequests - paths are sent to the channel, use SetUserAllowed to respond.
// If the channel is full, requests are dropped.
// Note that if SetUserAllowed is not called on a path sent to the channel, said path will not be sent again.
// (The previous statement is not true in the case of StopUserAllowRequests and AutoUserAllowRequests)
// If asking the user to accept/deny, consider using UserAllowNoneOnce if they take too long (timeout)
func (f *FS) AcceptUserAllowRequests(ch chan<- UserRequest) {
	f.alock.Lock()
	defer f.alock.Unlock()
	if f.allowReqs != nil {
		panic("Already being handled")
	}
	f.allowReqs = ch
}

// StopUserAllowRequests - Requests will not be sent after this call.
func (f *FS) StopUserAllowRequests() {
	f.alock.Lock()
	defer f.alock.Unlock()
	f.allowReqs = nil
	// No point in keeping everything waiting if there's nothing to wait on:
	for path, aw := range f.await {
		delete(f.await, path)
		close(aw)
	}
}

// SetUserAllowed responds to a path user request.
func (f *FS) SetUserAllowed(path string, allow UserAllow) {
	f.alock.Lock()
	if allow != UserAllowNoneOnce {
		f.allowed[path] = allow
	}
	if aw, ok := f.await[path]; ok {
		delete(f.await, path)
		f.alock.Unlock()
		close(aw)
	} else {
		f.alock.Unlock()
	}
}

// waits until the ctx is done! so make sure some sort of cancelation is in place.
func (f *FS) readUserAllowedWait(ctx context.Context, req UserRequest) UserAllow {
	path := req.Path

	f.alock.RLock()
	allow, ok := f.allowed[path]
	f.alock.RUnlock()
	if ok {
		return allow
	}

	// If the ctx is done right away, don't bother with the await channel.
	select {
	case <-ctx.Done():
		return UserAllowNone
	default:
	}

	// A few things done in a lock...
	f.alock.Lock()
	allow, ok = f.allowed[path] // Check again in case of change.
	if ok {
		f.alock.Unlock()
		return allow
	}
	// Automatic reply?
	if time.Now().Before(f.autoAllowUntil) {
		f.alock.Unlock()
		return f.autoAllow
	}
	// Need to get or init an await channel.
	aw, ok := f.await[path]
	if !ok {
		aw = make(chan struct{})
		// Ask the user, only if it's a new await, and only if accepting allow requests.
		if f.allowReqs != nil {
			f.await[path] = aw
			select {
			case f.allowReqs <- req:
			default:
				// Channel is full, so forget about it and don't wait.
				delete(f.await, path)
				close(aw)
				log.Print("Channel is full (AcceptUserAllowRequests)")
			}
		} else {
			// Nothing allowing, so don't wait.
			close(aw)
		}
	}
	f.alock.Unlock()

	// Await for reply...
	select {
	case <-aw:
	case <-ctx.Done():
	}

	// Get reply.
	f.alock.RLock()
	allow, ok = f.allowed[path] // defaults to UserAllowNone
	f.alock.RUnlock()
	return allow
}

func (f *FS) isUserAllowedWait(ctx context.Context, req UserRequest) UserAllow {
	allow := f.readUserAllowedWait(ctx, req)
	if allow == UserAllowOnce {
		f.alock.Lock()
		if f.allowed[req.Path] == UserAllowOnce { // Check again in case of change.
			delete(f.allowed, req.Path)
		}
		f.alock.Unlock()
	}
	return allow
}

// UserAllowedDefaultTimeout is the default timeout for waiting for a response from the user.
const UserAllowedDefaultTimeout = 10 * time.Second

func newUserReq(newPath, action string, header *fuse.Header) UserRequest {
	return UserRequest{
		Deadline:  time.Now().Add(UserAllowedDefaultTimeout),
		Path:      newPath,
		Action:    action,
		UID:       header.Uid,
		GID:       header.Gid,
		ThreadPID: header.Pid,
	}
}

func newUserReqFromAttr(newPath, action string, attr *fuse.Attr) UserRequest {
	return UserRequest{
		Deadline:  time.Now().Add(UserAllowedDefaultTimeout),
		Path:      newPath,
		Action:    action,
		UID:       attr.Uid,
		GID:       attr.Gid,
		ThreadPID: 0, // not known
	}
}

// IsUserAllowed - is the user allowed? waits for up to UserAllowedDefaultTimeout for the user to respond.
func (f *FS) IsUserAllowed(ctx context.Context, req UserRequest) bool {
	ctx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	return f.isUserAllowedWait(ctx, req).Allowed()
}

// IsUserAllowedFast - quickly checks if the user allowed, without waiting.
func (f *FS) IsUserAllowedFast(ctx context.Context, req UserRequest) bool {
	ctx, cancel := context.WithCancel(ctx)
	cancel() // cancel it now, we want it done immediately.
	return f.isUserAllowedWait(ctx, req).Allowed()
}

// Root node (dir)
func (f *FS) Root() (fs.Node, error) {
	node, err := f.fs.Root()
	if err != nil {
		return nil, err
	}
	return &userNode{f, node.(*loopback.Node), f.newPath}, nil
}

// Statfs is the FS statistics info.
func (f *FS) Statfs(ctx context.Context, req *fuse.StatfsRequest, resp *fuse.StatfsResponse) error {
	return f.fs.Statfs(ctx, req, resp)
}

type userNode struct {
	f       *FS
	node    *loopback.Node
	newPath string
}

func growSize(x uint64) uint64 {
	x |= 0x3F
	for y := uint64(1); y < x; y <<= 1 {
		x |= y
	}
	return x + 1
}

func (n *userNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	err := n.node.Attr(ctx, attr)
	if err != nil {
		return err
	}
	if !n.f.IsUserAllowedFast(ctx, newUserReqFromAttr(n.newPath, "attr", attr)) {
		// Obscure some things if not allowed yet...
		attr.Blocks = 42                // Fake block count.
		attr.Size = growSize(attr.Size) // Fake size.
		attr.Nlink = 1
		//attr.Atime = attr.Mtime
		//attr.Ctime = attr.Mtime
		//attr.Uid = 0
		//attr.Gid = 0
		//attr.Mode = ...
	}
	return nil
}

func (n *userNode) Forget() {
	n.node.Forget()
}

func (n *userNode) Access(ctx context.Context, a *fuse.AccessRequest) error {
	return n.node.Access(ctx, a)
}

func (n *userNode) Lookup(ctx context.Context, name string) (fs.Node, error) {
	node, err := n.node.Lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	return &userNode{n.f, node.(*loopback.Node), filepath.Join(n.newPath, name)}, nil
}

func (n *userNode) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	/*if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "access", &req.Header)) {
		return nil, ErrUserNotAllowed
	}*/
	handle, err := n.node.Open(ctx, req, resp)
	if err != nil {
		return nil, err
	}
	return &uhandle{
		f:         n.f,
		handle:    handle.(*loopback.Handle),
		n:         n,
		uid:       req.Uid,
		gid:       req.Gid,
		threadPID: req.Pid,
	}, nil
}

func (n *userNode) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "create", &req.Header)) {
		return nil, nil, ErrUserNotAllowed
	}
	node, handle, err := n.node.Create(ctx, req, resp)
	if err != nil {
		return nil, nil, err
	}
	ncreate := &userNode{n.f, node.(*loopback.Node), filepath.Join(n.newPath, req.Name)}
	return ncreate, &uhandle{
			f:         n.f,
			handle:    handle.(*loopback.Handle),
			n:         ncreate,
			uid:       req.Uid,
			gid:       req.Gid,
			threadPID: req.Pid,
			allow:     true, // allow it because we already allowed the create.
		},
		nil
}

func (n *userNode) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "mkdir", &req.Header)) {
		return nil, ErrUserNotAllowed
	}
	node, err := n.node.Mkdir(ctx, req)
	if err != nil {
		return nil, err
	}
	return &userNode{n.f, node.(*loopback.Node), filepath.Join(n.newPath, req.Name)}, nil
}

func (n *userNode) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "remove", &req.Header)) {
		return ErrUserNotAllowed
	}
	return n.node.Remove(ctx, req)
}

func (n *userNode) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "setattr", &req.Header)) {
		return ErrUserNotAllowed
	}
	return n.node.Setattr(ctx, req, resp)
}

func (n *userNode) Rename(ctx context.Context, req *fuse.RenameRequest, newDir fs.Node) error {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "rename", &req.Header)) {
		return ErrUserNotAllowed
	}
	return n.node.Rename(ctx, req, newDir)
}

func (n *userNode) Readlink(ctx context.Context, req *fuse.ReadlinkRequest) (string, error) {
	// Always allow readlink.
	return n.node.Readlink(ctx, req)
}

func (n *userNode) Symlink(ctx context.Context, req *fuse.SymlinkRequest) (fs.Node, error) {
	if !n.f.IsUserAllowed(ctx, newUserReq(n.newPath, "symlink", &req.Header)) {
		return nil, ErrUserNotAllowed
	}
	return n.node.Symlink(ctx, req)
}

type uhandle struct {
	f          *FS
	handle     *loopback.Handle
	n          *userNode
	uid, gid   uint32
	threadPID  uint32
	allow      bool // if false, check again at allowUntil; if true, always allow.
	allowUntil time.Time
}

const handleAllowTimeout = UserAllowedDefaultTimeout * 2

func (h *uhandle) userAllowed(ctx context.Context, action string) bool {
	if h.allow {
		return true
	}
	now := time.Now()
	if now.Before(h.allowUntil) {
		return h.allow
	}
	allow := h.f.IsUserAllowed(ctx, UserRequest{
		Deadline:  time.Now().Add(UserAllowedDefaultTimeout),
		Path:      h.n.newPath,
		Action:    action,
		UID:       h.uid,
		GID:       h.gid,
		ThreadPID: h.threadPID,
	})
	h.allow = allow
	h.allowUntil = now.Add(handleAllowTimeout)
	return allow
}

func (h *uhandle) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	return h.handle.Flush(ctx, req)
}

func (h *uhandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	if !h.userAllowed(ctx, "read") {
		return ErrUserNotAllowed
	}
	return h.handle.Read(ctx, req, resp)
}

/*
func (h *uhandle) ReadAll(ctx context.Context) ([]byte, error) {
	if !h.userAllowed(ctx, "read") {
		return nil, ErrUserNotAllowed
	}
	return h.handle.ReadAll(ctx)
}
*/

func (h *uhandle) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	/* // Always allow readdir.
	if !h.userAllowed(ctx, "read") {
		return nil, ErrUserNotAllowed
	}
	*/
	return h.handle.ReadDirAll(ctx)
}

func (h *uhandle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	return h.handle.Release(ctx, req)
}

func (h *uhandle) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	if !h.userAllowed(ctx, "write") {
		return ErrUserNotAllowed
	}
	return h.handle.Write(ctx, req, resp)
}
