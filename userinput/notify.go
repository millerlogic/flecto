package userinput

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	notify "github.com/millerlogic/go-notify"
	"github.com/pkg/errors"
)

// NotifyInput gets user input via dbus notifications.
type NotifyInput struct {
	Summary   string
	AppIcon   string
	Category  string
	SoundName string
}

var _ Interface = &NotifyInput{}

// GetInput implements Input.
func (input *NotifyInput) GetInput(ctx context.Context, output string, choices ...Choice) (string, error) {
	ntf := notify.NewNotification(input.Summary, strings.Trim(output, "\r\n"))
	ntf.AppIcon = input.AppIcon
	ntf.Hints = make(map[string]interface{})
	if input.Category != "" {
		ntf.Hints[notify.HintCategory] = input.Category
	}
	if input.SoundName != "" {
		ntf.Hints[notify.HintSoundName] = input.SoundName
	}

	// If no deadline, just use the default notification timeout.
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		timeoutMs := int32(deadline.Sub(time.Now())/time.Millisecond) - 1000 // Minus 1sec to account for delays.
		if timeoutMs > 0 {
			ntf.Timeout = timeoutMs
		}
	}

	var randbuf [8]byte
	_, err := io.ReadFull(rand.Reader, randbuf[:])
	if err != nil {
		return "", err
	}
	pfxNum := binary.BigEndian.Uint64(randbuf[:])
	actionPrefix := strconv.FormatUint(pfxNum, 36)

	numDefaults := 0
	actionPairs := []string{}
	for _, opt := range choices {
		if opt.Default {
			numDefaults++
			if numDefaults == 1 {
				actionPairs = append(actionPairs, "default", opt.Text)
				continue
			}
		}
		actionPairs = append(actionPairs, actionPrefix+opt.Text, opt.Text)
	}
	ntf.Actions = actionPairs

	nid, err := ntf.Show()
	if err != nil {
		return "", err
	}

	ctxInput, cancel := context.WithCancel(ctx)
	defer cancel()

	userInput := ""
	noRespErr := errors.New("No response")
	finalErr := noRespErr
	ch := make(chan notify.Signal, 2)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		//defer log.Printf("done reading signals")
		for sig := range ch {
			if sig.ID == nid {
				//spew.Dump(sig)
				if sig.CloseReason == notify.NotClosed {
					notify.CloseNotification(nid)
					if strings.HasPrefix(sig.ActionKey, actionPrefix) {
						finalErr = nil
						userInput = sig.ActionKey[len(actionPrefix):]
					} else if sig.ActionKey == "default" {
						finalErr = nil
						userInput = ""
					} else {
						finalErr = errors.New("Invalid response (" + sig.ActionKey + ")")
					}
				} else {
					if finalErr == noRespErr {
						finalErr = nil
						userInput = "" // if the user closes the notification, treat as default.
					}
					cancel()
					break
				}
			}
		}
	}()
	err = notify.SignalNotify(ctxInput, ch)
	close(ch) // Ensure the loop above exits.
	if err == context.DeadlineExceeded {
		//log.Print("timed out")
		notify.CloseNotification(nid)
		finalErr = ctxInput.Err()
	} else if err != nil && err != context.Canceled {
		return "", err
	}
	wg.Wait()

	return userInput, finalErr
}

// Supported returns nil on success if supported.
func (input *NotifyInput) Supported() error {
	caps, err := notify.GetCapabilities()
	if err != nil {
		return err
	}
	if !caps.Actions {
		name := "this implementation"
		info, _ := notify.GetServerInformation()
		if info.Name != "" {
			name = info.Name
		}
		return errors.New("Notification actions not supported by " + name)
	}
	return nil
}
