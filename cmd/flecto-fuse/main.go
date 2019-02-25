package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/millerlogic/flecto/userinput"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

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

	var input userinput.Interface
	switch inputMode {
	case "term":
		xinput := &userinput.TermInput{}
		input = xinput
		go xinput.Run(ctx)
	case "notify":
		xinput := &userinput.NotifyInput{Summary: notifySummary, AppIcon: notifyAppIcon, SoundName: notifySoundName}
		err := xinput.Supported()
		if err != nil {
			return errors.Wrap(err, "Notifications not supported")
		}
		input = xinput
	case "auto":
		xinput1 := &userinput.NotifyInput{Summary: notifySummary, AppIcon: notifyAppIcon, SoundName: notifySoundName}
		err1 := xinput1.Supported()
		if err1 == nil {
			log.Print("-input=auto using notify")
			input = xinput1
		} else {
			if verbose {
				log.Printf("-input=auto not using notify (because: %v)", err1)
			}
			log.Print("-input=auto using term")
			xinput2 := &userinput.TermInput{}
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
