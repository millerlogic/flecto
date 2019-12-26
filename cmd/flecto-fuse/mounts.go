package main

import (
	"context"
	"html"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bazil.org/fuse"
	fuseFs "bazil.org/fuse/fs"
	"github.com/millerlogic/flecto/subprocfs"
	"github.com/millerlogic/flecto/userfs"
	"github.com/millerlogic/flecto/userinput"
	"github.com/pkg/errors"
)

type mountOpts struct {
	destDir string
	debug   bool
	verbose bool
}

func mountUserFS(ctx context.Context, srcDir string, opts mountOpts, input userinput.Interface) error {
	fs := userfs.New(srcDir)
	if opts.debug {
		fs.SetLogger(log.New(os.Stderr, "", log.LstdFlags))
	}
	c, err := fuse.Mount(opts.destDir, fuse.DefaultPermissions(), fuse.AsyncRead())
	if err != nil {
		return errors.WithStack(err)
	}

	var umlock sync.Mutex
	unmounted := false
	doUnmount := func() {
		umlock.Lock()
		defer umlock.Unlock()
		if !unmounted {
			err := fuse.Unmount(opts.destDir)
			if err != nil {
				log.Printf("Unable to unmount %s: %v", opts.destDir, err)
			} else {
				unmounted = true
				log.Printf("Unmounted %s", opts.destDir)
			}
		}
	}
	defer doUnmount()

	numRecentRequests := int32(0) // atomic
	go func() {
		for {
			time.Sleep(userfs.UserAllowedDefaultTimeout)
			for {
				n := atomic.LoadInt32(&numRecentRequests)
				if n == 0 {
					break
				}
				if atomic.CompareAndSwapInt32(&numRecentRequests, n, n-1) {
					break
				}
			}
		}
	}()

	allowReqs := make(chan userfs.UserRequest, 16)
	fs.AcceptUserAllowRequests(allowReqs)
	go func() {
		for req := range allowReqs {
			func() {
				recent := atomic.AddInt32(&numRecentRequests, 1)
				if opts.verbose {
					log.Printf("File %s request for \"%s\" (uid=%d, tid=%d)",
						req.Action, req.Path, req.UID, req.ThreadPID)
				}
				fname := filepath.Base(req.Path)
				fdir := filepath.Dir(req.Path)
				fext := filepath.Ext(fname)
				fdisp := strings.TrimRight(fname, fext)
				msg := "<b>" + html.EscapeString(strings.Title(req.Action)) + "</b> file:\n• <b>" +
					fdisp + "</b><small>" + html.EscapeString(fext) +
					"   <i>" + html.EscapeString(fdir) + "</i></small>"
				var choices []userinput.Choice
				if recent < 5 {
					choices = append(choices, userinput.Choice{Text: "Allow", Shortcut: 'y'})
					choices = append(choices, userinput.Choice{Text: "Deny", Shortcut: 'n'})
					choices = append(choices, userinput.Choice{Text: "Skip", Shortcut: 's', Default: true})
				} else {
					msg += "\n<small>Allow or deny <b>all</b> for 30 seconds?</small>"
					choices = append(choices, userinput.Choice{Text: "Allow All", Shortcut: 'a'})
					choices = append(choices, userinput.Choice{Text: "Deny All", Shortcut: 'd'})
					choices = append(choices, userinput.Choice{Text: "Skip", Shortcut: 's', Default: true})
				}
				ctxInput, cancel := context.WithTimeout(ctx, userfs.UserAllowedDefaultTimeout)
				defer cancel()
				in, err := input.GetInput(ctxInput, msg, choices...)
				if err != nil {
					if err == context.DeadlineExceeded {
						select {
						case <-ctx.Done(): // The parent ctx is done too.
							log.Print("Request aborted, denying...")
						default: // Only the ctxInput is done.
							log.Print("Timed out, denying...")
						}
					} else {
						log.Printf("ERROR %v", err)
						log.Print("Request error, denying...")
					}
					fs.SetUserAllowed(req.Path, userfs.UserAllowNoneOnce)
				} else {
					var ch byte
					if in != "" {
						ch = in[0]
					}
					switch ch {
					case 'y', 'Y': // yes (one)
						if opts.verbose {
							log.Print("Allowing...")
						}
						fs.SetUserAllowed(req.Path, userfs.UserAllowAll)
					case 'n', 'N':
						if opts.verbose {
							log.Print("Denying...")
						}
						fs.SetUserAllowed(req.Path, userfs.UserAllowNone)
					case 'd', 'D': // deny 30s
						if opts.verbose {
							log.Print("Denying all for 30 seconds...")
						}
						fs.AutoUserAllowRequests(userfs.UserAllowNone, time.Now().Add(30*time.Second))
					case 'a', 'A': // allow 30s
						if opts.verbose {
							log.Print("Allowing all for 30 seconds...")
						}
						fs.AutoUserAllowRequests(userfs.UserAllowAll, time.Now().Add(30*time.Second))
					default:
						if opts.verbose {
							log.Print("Skipping...")
						}
						fs.SetUserAllowed(req.Path, userfs.UserAllowNoneOnce)
					}
				}
			}()
		}
	}()

	chFinal := make(chan struct{})
	go func() {
		select {
		case <-chFinal:
		case <-ctx.Done():
			//c.Close()
			//doUnmount()
			for {
				select {
				case <-chFinal:
					return
				case <-time.After(time.Second):
					doUnmount()
				}
			}
		}
	}()

	err = fuseFs.Serve(c, fs)
	fs.StopUserAllowRequests()
	close(allowReqs)
	close(chFinal)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func mountSubProcFS(ctx context.Context, opts mountOpts) error {
	fs := subprocfs.New(
		subprocfs.MemInfo(1024*1024*1024),
		subprocfs.CPUInfo(1),
		subprocfs.RestrictMounts(os.Getenv("HOME"), ""),
		subprocfs.EmptyMountInfo(),
		subprocfs.EmptyMountStats(),
		subprocfs.NewProcInfo("/proc/swaps", "Filename\tType\tSize\tUsed\tPriority\n"),
	)
	if opts.debug {
		fs.SetLogger(log.New(os.Stderr, "subprocfs ", log.LstdFlags))
	}
	fs.LimitPIDs(true)
	c, err := fuse.Mount(opts.destDir, fuse.DefaultPermissions(), fuse.AsyncRead())
	if err != nil {
		return errors.WithStack(err)
	}

	var umlock sync.Mutex
	unmounted := false
	doUnmount := func() {
		umlock.Lock()
		defer umlock.Unlock()
		if !unmounted {
			err := fuse.Unmount(opts.destDir)
			if err != nil {
				log.Printf("Unable to unmount %s: %v", opts.destDir, err)
			} else {
				unmounted = true
				log.Printf("Unmounted %s", opts.destDir)
			}
		}
	}
	defer doUnmount()

	chFinal := make(chan struct{})
	go func() {
		select {
		case <-chFinal:
		case <-ctx.Done():
			//c.Close()
			//doUnmount()
			for {
				select {
				case <-chFinal:
					return
				case <-time.After(time.Second):
					doUnmount()
				}
			}
		}
	}()

	err = fuseFs.Serve(c, fs)
	close(chFinal)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}
