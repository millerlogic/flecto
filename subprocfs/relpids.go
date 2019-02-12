package subprocfs

import (
	"io/ioutil"
	"log"
	"path/filepath"

	proc "github.com/c9s/goprocinfo/linux"
	"github.com/pkg/errors"
)

// GetRelatedPIDs calls callback for all the related pids: all parents and children, including pid itself.
// procPath is typically "/proc"
func GetRelatedPIDs(procPath string, pid uint32, callback func(pid uint32)) error {
	mpid := make(map[uint32]*proc.ProcessStat)    // pid -> info
	mppid := make(map[uint32][]*proc.ProcessStat) // ppid -> children
	dir, err := ioutil.ReadDir(procPath)
	if err != nil {
		return errors.WithStack(err)
	}

	foundpid := false
	for _, fi := range dir {
		if fi.IsDir() {
			spid := fi.Name()
			matches, _ := filepath.Match("[0-9]*", spid)
			if matches {
				pstat, err := proc.ReadProcessStat(filepath.Join(procPath, spid, "stat"))
				if err != nil {
					log.Printf("GetRelatedPIDs: Unable to read process stat for pid %v: %v", spid, err)
				} else {
					xpid := uint32(pstat.Pid)
					if xpid == pid {
						foundpid = true
					}
					mpid[xpid] = pstat
					xppid := uint32(pstat.Ppid)
					mppid[xppid] = append(mppid[xppid], pstat)
				}
			}
		}
	}

	if !foundpid {
		log.Printf("GetRelatedPIDs: Did not find pid %v", pid)
		return nil
	}

	// pid itself
	callback(pid)

	// all pid parents
	for xpid := pid; ; {
		pstat := mpid[xpid]
		if pstat == nil {
			break
		}
		xppid := uint32(pstat.Ppid)
		if xppid == 0 {
			break
		}
		callback(xppid)
		xpid = xppid
	}

	// add all pid children
	var addAllChildren func(callback func(pid uint32), xpid uint32)
	addAllChildren = func(callback func(pid uint32), xpid uint32) {
		for _, pcstat := range mppid[xpid] {
			xcpid := uint32(pcstat.Pid)
			callback(xcpid)
			addAllChildren(callback, xcpid)
		}
	}
	addAllChildren(callback, pid)

	return nil
}

func GetRelatedPIDsMap(procPath string, pid uint32) (map[uint32]struct{}, error) {
	m := make(map[uint32]struct{})
	err := GetRelatedPIDs(procPath, pid, func(xpid uint32) {
		m[xpid] = struct{}{}
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}
