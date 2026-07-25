// Package opencode reads the plugin-owned state file, merges the TUI-owned
// usermeta sidecar, and talks to the opencode server API.
//
// Ownership model (fixes the two-writer races):
//   - state.log     written by the opencode plugin only. The TUI treats it as
//     read-only, except for pruning orphans the server no longer knows about.
//   - usermeta.json written by the TUI only (titles/notes). Merged at load.
//   - deletes       go through the server HTTP API so session.deleted fires
//     in the long-lived opencode instance whose plugin updates state.log.
package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	ID        string
	Status    string
	Slug      string
	Directory string
	Title     string
	Subagents int
	UpdatedAt time.Time
	Note      string // TUI-owned
	UserTitle string // TUI-owned, overrides Title for display
}

// DisplayTitle returns the user-set title if present, else the session title.
func (s Session) DisplayTitle() string {
	if s.UserTitle != "" {
		return s.UserTitle
	}
	return s.Title
}

// Meta is the sidecar record for a single session.
type Meta struct {
	Title string `json:"title,omitempty"`
	Note  string `json:"note,omitempty"`
}

type Store struct {
	StatePath string
	MetaPath  string
	ServerURL string // e.g. http://localhost:4096
	HTTP      *http.Client
	Logger    *slog.Logger
}

func (st *Store) client() *http.Client {
	if st.HTTP != nil {
		return st.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// ---- loading ----

type rawEntry struct {
	Status    string                     `json:"status"`
	UpdatedAt string                     `json:"updatedAt"`
	Slug      string                     `json:"slug"`
	Directory string                     `json:"directory"`
	Title     string                     `json:"title"`
	Subagents map[string]json.RawMessage `json:"subagents"`
	Usermeta  map[string]string          `json:"usermeta"`
}

func (st *Store) readRaw() (map[string]rawEntry, error) {
	data, err := os.ReadFile(st.StatePath)
	if err != nil {
		return nil, err
	}
	var raw map[string]rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (st *Store) readMeta() map[string]Meta {
	meta := map[string]Meta{}
	data, err := os.ReadFile(st.MetaPath)
	if err != nil {
		return meta
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		st.Logger.Error("parsing usermeta sidecar", slog.Any("err", err))
	}
	return meta
}

// Load parses state.log and merges the sidecar. Sidecar values win over any
// legacy title/note stored in the state file's usermeta (migration-friendly).
func (st *Store) Load() ([]Session, error) {
	raw, err := st.readRaw()
	if err != nil {
		return nil, err
	}
	meta := st.readMeta()

	sessions := make([]Session, 0, len(raw))
	for id, e := range raw {
		s := Session{
			ID:        id,
			Status:    e.Status,
			Slug:      e.Slug,
			Directory: e.Directory,
			Title:     e.Title,
			Subagents: len(e.Subagents),
		}
		if e.UpdatedAt != "" {
			if ms, err := strconv.ParseInt(e.UpdatedAt, 10, 64); err == nil {
				s.UpdatedAt = time.UnixMilli(ms)
			}
		}
		if e.Usermeta != nil {
			s.UserTitle = e.Usermeta["title"]
			s.Note = e.Usermeta["note"]
		}
		if m, ok := meta[id]; ok {
			if m.Title != "" {
				s.UserTitle = m.Title
			}
			if m.Note != "" {
				s.Note = m.Note
			}
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// ---- usermeta (sidecar, TUI is the sole writer) ----

// SetUsermeta persists a title/note for a session, pruning sidecar entries
// whose sessions no longer exist in the state file.
func (st *Store) SetUsermeta(id, title, note string) error {
	meta := st.readMeta()
	if title == "" && note == "" {
		delete(meta, id)
	} else {
		meta[id] = Meta{Title: title, Note: note}
	}
	if raw, err := st.readRaw(); err == nil {
		for k := range meta {
			if _, ok := raw[k]; !ok {
				delete(meta, k)
			}
		}
	}
	return atomicWriteJSON(st.MetaPath, meta)
}

// ---- deletion ----

// Delete removes a session via the server API. A 404 means the server no
// longer knows the session: the state.log entry is an orphan (the plugin will
// never receive session.deleted for it), so it is pruned directly.
func (st *Store) Delete(id string) error {
	req, err := http.NewRequest(http.MethodDelete, st.ServerURL+"/session/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := st.client().Do(req)
	if err != nil {
		return fmt.Errorf("opencode server at %s: %w", st.ServerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		st.Logger.Info("session unknown to server; pruning orphan", slog.String("id", id))
		return st.removeOrphan(id)
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete session %s: %s: %s", id, resp.Status, body)
	}
	return nil
}

// removeOrphan rewrites state.log without the given id. This is the one place
// the TUI writes the plugin's file; it is rare (orphans only) and atomic, so
// readers never see partial JSON. A concurrent plugin write can still race
// this, but the entry is dead by definition and any resurrection is bounded
// to the next orphan prune.
func (st *Store) removeOrphan(id string) error {
	data, err := os.ReadFile(st.StatePath)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, id)
	return atomicWriteJSON(st.StatePath, raw)
}

func atomicWriteJSON(path string, v any) error {
	out, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (st *Store) CreateSession(dir string) (string, error) {
	u := fmt.Sprintf("%s/session?directory=%s", st.ServerURL, url.QueryEscape(dir))
	resp, err := st.client().Post(u, "application/json", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session: %s: %s", resp.Status, b)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (st *Store) PromptAsync(sessionID, dir, text string) error {
	body, _ := json.Marshal(map[string]any{
		"parts": []map[string]string{{"type": "text", "text": text}},
	})
	u := fmt.Sprintf("%s/session/%s/prompt_async?directory=%s",
		st.ServerURL, sessionID, url.QueryEscape(dir))
	resp, err := st.client().Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prompt session %s: %s: %s", sessionID, resp.Status, b)
	}
	return nil
}
