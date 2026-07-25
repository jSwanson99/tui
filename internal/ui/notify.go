package ui

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gen2brain/beeep"
)

var icons struct {
	sync.Once
	data [][]byte
	err  error
}

func loadIcons() {
	dir := filepath.Join(os.Getenv("HOME"), ".local/share/icons")
	entries, err := os.ReadDir(dir)
	if err != nil {
		icons.err = err
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			icons.data = append(icons.data, b)
		}
	}
	if len(icons.data) == 0 {
		icons.err = fmt.Errorf("no icons found")
	}
}

func randomIcon() []byte {
	icons.Do(loadIcons)
	if icons.err != nil {
		return nil
	}
	return icons.data[rand.IntN(len(icons.data))]
}

// Notify sends a desktop notification with a random icon; failures are
// silently ignored (notifications are best-effort).
func Notify(title, body string) {
	beeep.Notify(title, body, randomIcon())
}
