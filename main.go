package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"jds.net/tui/internal/domain/git"
	"jds.net/tui/internal/domain/opencode"
	"jds.net/tui/internal/domain/systemd"
	"jds.net/tui/internal/ui"
	"jds.net/tui/internal/ui/views"
)

func main() {
	f, err := os.OpenFile("app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	logger := slog.New(slog.NewJSONHandler(f, nil))

	home := os.Getenv("HOME")
	trackerDir := home + "/.local/share/opencode/log/session-tracker"
	statePath := trackerDir + "/state.log"

	serverURL := os.Getenv("OPENCODE_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://localhost:4096"
	}

	ocService := &systemd.Service{Name: "opencode"}
	if !ocService.IsActive() {
		logger.Info("starting opencode because it wasn't active")
		ocService.Start()
	}

	manager := git.NewManager(home+"/.cache/devcache", home+"/src", logger)
	store := &opencode.Store{
		StatePath: statePath,
		MetaPath:  trackerDir + "/usermeta.json",
		ServerURL: serverURL,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		Logger:    logger,
	}

	appViews := []views.View{
		views.NewTreeView(manager, store, logger),
		views.NewRepoView(manager),
	}

	app := ui.NewApp(appViews, logger)
	program := tea.NewProgram(app, tea.WithAltScreen())

	stop, err := ui.WatchFile(statePath, func() {
		program.Send(views.SessionsFileChangedMsg{})
	})
	if err != nil {
		logger.Error("watching state file", slog.Any("err", err))
		os.Exit(1)
	}
	defer stop()

	ui.Notify("   Greetings", "")
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
