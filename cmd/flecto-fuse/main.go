package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bazil.org/fuse"
	fuseFs "bazil.org/fuse/fs"
	"github.com/millerlogic/flecto/subprocfs"
	"github.com/millerlogic/flecto/userfs"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type mountOpts struct {
	destDir string
	debug   bool
	verbose bool
}

func mountUserFS(ctx context.Context, srcDir string, opts mountOpts, input Input) error {
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
				msg := fmt.Sprintf("File <b>%s</b> request for:\n &bull; <tt>%s</tt> \n\nAllow access?",
					html.EscapeString(req.Action), html.EscapeString(req.Path))
				inopts := []InputOption{
					InputOption{Text: "Skip", Shortcut: 's', Default: true},
					InputOption{Text: "Yes", Shortcut: 'y'},
					InputOption{Text: "No", Shortcut: 'n'},
				}
				_ = recent
				/* // This isn't working as expected.
				if recent >= 5 {
					msg += "\n\n   or deny or allow all requests for 30 seconds"
					opts = append(opts, InputOption{Text: "Deny All", Shortcut: 'd'})
					opts = append(opts, InputOption{Text: "Allow All", Shortcut: 'a'})
				}
				*/
				ctxInput, cancel := context.WithTimeout(ctx, userfs.UserAllowedDefaultTimeout)
				defer cancel()
				in, err := input.GetInput(ctxInput, msg, inopts...)
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

const notifySummary = "flecto - file security"
const notifyAppIcon = "fileopen"
const notifySoundName = "dialog-question"

func run() error {
	debug := false
	flag.BoolVar(&debug, "debug", debug, "Enable debug output")
	verbose := debug
	flag.BoolVar(&verbose, "verbose", verbose, "Enable verbose output")
	inputMode := "auto"
	flag.StringVar(&inputMode, "input", inputMode, "Set the input mode (term=read the terminal, notify=notifications)")
	timeout := time.Duration(0)
	flag.DurationVar(&timeout, "_timeout", timeout, "Set timeout to stop serving (do not use, temporary feature)")
	flag.Parse()
	if flag.NArg() == 0 {
		return errors.New("Invalid args")
	}

	group, ctx := errgroup.WithContext(context.Background()) // ctx canceled when a group func fails.
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	go func() {
		sigch := make(chan os.Signal)
		signal.Notify(sigch, os.Interrupt, syscall.SIGHUP)
		for sig := range sigch {
			log.Printf("Received %s", sig)
			cancel()
			break
		}
		signal.Stop(sigch)
	}()

	var input Input
	switch inputMode {
	case "term":
		xinput := &TermInput{}
		input = xinput
		go xinput.Run(ctx)
	case "notify":
		xinput := &NotifyInput{Summary: notifySummary, AppIcon: notifyAppIcon, SoundName: notifySoundName}
		err := xinput.Supported()
		if err != nil {
			return errors.Wrap(err, "Notifications not supported")
		}
		input = xinput
	case "auto":
		xinput1 := &NotifyInput{Summary: notifySummary, AppIcon: notifyAppIcon, SoundName: notifySoundName}
		err1 := xinput1.Supported()
		if err1 == nil {
			log.Print("-input=auto using notify")
			input = xinput1
		} else {
			if verbose {
				log.Printf("-input=auto not using notify (because: %v)", err1)
			}
			log.Print("-input=auto using term")
			xinput2 := &TermInput{}
			input = xinput2
			go xinput2.Run(ctx)
		}
	default:
		return errors.New("Invalid -input=" + inputMode)
	}

	for iarg := 0; iarg < flag.NArg(); iarg++ {
		action := flag.Arg(iarg)
		switch action {

		case "userfs":
			srcDir := flag.Arg(iarg + 1)
			destDir := flag.Arg(iarg + 2)
			iarg += 2
			if srcDir == "" || destDir == "" {
				return errors.New(action + " expects args: srcDir destDir")
			}
			log.Printf(`mount %s "%s" "%s"`, action, srcDir, destDir)
			group.Go(func() error {
				err := mountUserFS(ctx, srcDir, mountOpts{destDir: destDir, debug: debug, verbose: verbose}, input)
				if err != nil {
					return errors.Wrap(err, "Problem with "+action)
				}
				return nil
			})

		case "subprocfs":
			destDir := flag.Arg(iarg + 1)
			iarg++
			if destDir == "" {
				return errors.New(action + " expects args:  destDir")
			}
			log.Printf(`mount %s "%s" "%s"`, action, "/proc", destDir)
			group.Go(func() error {
				err := mountSubProcFS(ctx, mountOpts{destDir: destDir, debug: debug, verbose: verbose})
				if err != nil {
					return errors.Wrap(err, "Problem with "+action)
				}
				return nil
			})

		default:
			return errors.New("Unknown action: " + action)
		}

	}

	return group.Wait()
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %+v\n", err)
		log.Printf("ERROR %v", err)
		os.Exit(1)
	}
}
