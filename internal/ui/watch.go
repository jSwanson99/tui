package ui

import (
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchFile watches the directory containing path (the directory inode
// survives the atomic temp+rename writes the opencode plugin performs, unlike
// the file inode) and invokes notify, debounced, whenever the target file
// changes. The watcher is long-lived: because it is armed once at startup and
// never torn down between events, no write can land in an unwatched gap.
//
// notify is expected to be tea.Program.Send-based and safe to call from any
// goroutine.
func WatchFile(path string, notify func()) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		var debounce *time.Timer
		for {
			select {
			case <-done:
				if debounce != nil {
					debounce.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// The directory also sees event.log/info.log appends and the
				// .tmp staging file; only the target file matters.
				if filepath.Base(event.Name) != base {
					continue
				}
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(50*time.Millisecond, notify)
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()

	return func() {
		close(done)
		watcher.Close()
	}, nil
}
