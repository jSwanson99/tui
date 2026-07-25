// Package git contains all git/workspace domain logic. Functions here are
// synchronous and return errors
package git

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type Repo struct {
	Name   string
	Path   string
	Bare   bool
	Dirty  bool
	Origin string
}

type WorkspaceInfo struct {
	Name        string
	Namespace   string
	Path        string
	NumProjects int
	Dirty       bool
}

func (ws *WorkspaceInfo) FullName() string {
	return ws.Namespace + "/" + ws.Name
}

type Manager struct {
	CachePath string
	BaseDir   string
	// TemplatesPath overrides where templates are stored; defaults to
	// devtemplates.json next to the repo cache.
	TemplatesPath string
	Logger        *slog.Logger
}

func NewManager(cachePath, baseDir string, logger *slog.Logger) *Manager {
	return &Manager{CachePath: cachePath, BaseDir: baseDir, Logger: logger}
}

// ---- repo cache ----

func (m *Manager) ReadCache() ([]Repo, error) {
	data, err := os.ReadFile(m.CachePath)
	if err != nil {
		return nil, err
	}
	var repos []Repo
	return repos, json.Unmarshal(data, &repos)
}

func (m *Manager) WriteCache(repos []Repo) error {
	if err := os.MkdirAll(filepath.Dir(m.CachePath), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(repos)
	if err != nil {
		return err
	}
	tmp := m.CachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, m.CachePath)
}

// ---- clone ----

func (m *Manager) CloneRepo(url string) error {
	m.Logger.Info("starting repo clone", slog.String("url", url))
	cloneTarget := filepath.Join(m.BaseDir, "projects")

	repo := filepath.Base(url)
	if !strings.HasSuffix(repo, ".git") {
		repo += ".git"
	}
	repoDir := filepath.Join(cloneTarget, repo)
	tmpDir := repoDir + ".cloning"

	// Clean up any prior failed attempt.
	os.RemoveAll(tmpDir)

	if out, err := m.Cmd(cloneTarget, "clone", "--bare", url, tmpDir); err != nil {
		m.Logger.Error("error during clone", "err", err, "output", string(out))
		os.RemoveAll(tmpDir)
		return err
	}
	if out, err := m.Cmd(tmpDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		m.Logger.Error("error configuring remote", "err", err, "output", string(out))
		os.RemoveAll(tmpDir)
		return err
	}
	if out, err := m.Cmd(tmpDir, "fetch", "origin"); err != nil {
		m.Logger.Error("error fetching", "err", err, "output", string(out))
		os.RemoveAll(tmpDir)
		return err
	}
	if err := os.Rename(tmpDir, repoDir); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("rename %s -> %s: %w", tmpDir, repoDir, err)
	}
	m.Logger.Info("successful clone", "repo", repo)
	return nil
}

// ---- workspaces ----

// CreateWorkspace creates <base>/workspaces/<namespace>/<name>/ and adds a new
// worktree (on new branch <name>) in each repo. Atomic: preflight checks run
// before anything is written, and a failed worktree add rolls the dir back.
// Returns the workspace path; it only returns after every repo's fetch and
// worktree add has completed.
func (m *Manager) CreateWorkspace(namespace, name string, repos []Repo) (string, error) {
	wsPath := filepath.Join(m.BaseDir, "workspaces", namespace, name)

	for _, r := range repos {
		if _, err := m.Cmd(r.Path, "rev-parse", "--verify", name); err == nil {
			return "", fmt.Errorf("branch %q already exists in repo %q", name, r.Name)
		}
		out, err := m.Cmd(r.Path, "worktree", "list", "--porcelain")
		if err != nil {
			return "", fmt.Errorf("worktree list failed for %s: %w", r.Name, err)
		}
		if strings.Contains(string(out), wsPath) {
			return "", fmt.Errorf("worktree path %q already registered in repo %q", wsPath, r.Name)
		}
	}

	if err := os.MkdirAll(wsPath, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", wsPath, err)
	}

	var group errgroup.Group
	for _, r := range repos {
		group.Go(func() error {
			if out, err := m.Cmd(r.Path, "fetch", "origin"); err != nil {
				return fmt.Errorf("fetch failed for %q: %w\n%s", r.Name, err, out)
			}

			var branch string
			if _, err := m.Cmd(r.Path, "rev-parse", "--verify", "origin/main"); err == nil {
				branch = "origin/main"
			} else if _, err := m.Cmd(r.Path, "rev-parse", "--verify", "origin/master"); err == nil {
				branch = "origin/master"
			} else {
				return fmt.Errorf("neither origin/main nor origin/master found in %q", r.Name)
			}

			wtPath := filepath.Join(wsPath, r.Name)
			if out, err := m.Cmd(r.Path, "worktree", "add", "--no-track", "-b", name, wtPath, branch); err != nil {
				return fmt.Errorf("worktree add failed for %q: %w\n%s", r.Name, err, out)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		os.RemoveAll(wsPath)
		return "", err
	}

	m.Logger.Info("workspace created",
		slog.String("namespace", namespace),
		slog.String("name", name),
		slog.Int("repos", len(repos)))
	return wsPath, nil
}

// RemoveWorkspace tears down every worktree in the workspace (verifying the
// expected branch and a clean tree first), deletes the branches from the bare
// repos, and removes the workspace directory.
func (m *Manager) RemoveWorkspace(ws WorkspaceInfo) error {
	branchName := ws.Name

	entries, err := os.ReadDir(ws.Path)
	if err != nil {
		return fmt.Errorf("reading workspace dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wtPath := filepath.Join(ws.Path, entry.Name())
		gitFile := filepath.Join(wtPath, ".git")
		data, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}
		wtMetaDir := strings.TrimPrefix(line, "gitdir: ")
		bareRepo := filepath.Dir(filepath.Dir(wtMetaDir))

		out, err := m.Cmd(wtPath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return fmt.Errorf("rev-parse HEAD in %s: %w\n%s", entry.Name(), err, out)
		}
		if cur := strings.TrimSpace(string(out)); cur != branchName {
			return fmt.Errorf("worktree %s is on branch %q, expected %q", entry.Name(), cur, branchName)
		}
		if RepoHasChanges(wtPath) {
			return fmt.Errorf("worktree %s has uncommitted changes", entry.Name())
		}
		if out, err = m.Cmd(wtPath, "checkout", "--detach"); err != nil {
			return fmt.Errorf("detach HEAD in %s: %w\n%s", entry.Name(), err, out)
		}
		if out, err = m.Cmd(bareRepo, "branch", "-D", branchName); err != nil {
			return fmt.Errorf("branch delete %s in %s: %w\n%s", branchName, entry.Name(), err, out)
		}
		if out, err = m.Cmd(bareRepo, "worktree", "remove", wtPath); err != nil {
			return fmt.Errorf("worktree remove %s: %w\n%s", entry.Name(), err, out)
		}
	}

	if err := os.RemoveAll(ws.Path); err != nil {
		return fmt.Errorf("removing workspace dir: %w", err)
	}
	return nil
}

// FindWorkspaces scans <base>/workspaces, pruning empty namespace dirs.
func (m *Manager) FindWorkspaces() ([]WorkspaceInfo, error) {
	workspacePath := filepath.Join(m.BaseDir, "workspaces")
	namespaces, err := os.ReadDir(workspacePath)
	if err != nil {
		return nil, err
	}

	var workspaces []WorkspaceInfo
	for _, ns := range namespaces {
		if !ns.IsDir() || strings.HasPrefix(ns.Name(), ".") {
			continue
		}

		nsPath := filepath.Join(workspacePath, ns.Name())
		wsEntries, err := os.ReadDir(nsPath)
		if err != nil {
			m.Logger.Error("reading workspaces from path", slog.String("path", nsPath), slog.Any("err", err))
			continue
		}
		if len(wsEntries) == 0 {
			if err := os.Remove(nsPath); err != nil {
				m.Logger.Error("deleting empty namespace", slog.String("path", nsPath), slog.Any("err", err))
			}
			continue
		}

		for _, ws := range wsEntries {
			if !ws.IsDir() {
				continue
			}
			wsPath := filepath.Join(nsPath, ws.Name())
			entries, _ := os.ReadDir(wsPath)
			dirty := false
			for _, entry := range entries {
				if entry.IsDir() && RepoHasChanges(filepath.Join(wsPath, entry.Name())) {
					dirty = true
					break
				}
			}
			workspaces = append(workspaces, WorkspaceInfo{
				Name:        ws.Name(),
				Namespace:   ns.Name(),
				Path:        wsPath,
				NumProjects: len(entries),
				Dirty:       dirty,
			})
		}
	}
	return workspaces, nil
}

// ---- scanning ----

func (m *Manager) FindGitReposIn(dir string) ([]Repo, error) {
	var repos []Repo
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		var bare bool
		var repoPath string
		var dirty bool

		if d.IsDir() && d.Name() == ".git" {
			repoPath = filepath.Dir(path)
			dirty = RepoHasChanges(repoPath)
		} else if d.IsDir() && !strings.HasSuffix(path, ".cloning") {
			if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
				bare = true
				repoPath = path
			}
		}
		if repoPath != "" {
			repos = append(repos, Repo{
				Name:   strings.TrimSuffix(filepath.Base(repoPath), ".git"),
				Path:   repoPath,
				Bare:   bare,
				Dirty:  dirty,
				Origin: repoOrigin(repoPath),
			})
			return filepath.SkipDir
		}
		return nil
	})
	return repos, err
}

// Cmd runs git -C repoPath <args>, logging the invocation with a short uid so
// command and error lines can be correlated.
func (m *Manager) Cmd(repoPath string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repoPath}, args...)
	uid := uuid.New().String()[28:]

	m.Logger.Info("git command", slog.String("cmd", strings.Join(full, " ")), slog.String("uid", uid))
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		m.Logger.Error("git command", slog.String("uid", uid), slog.String("error", err.Error()))
	}
	return out, err
}

func RepoHasChanges(path string) bool {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return true
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func repoOrigin(path string) string {
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	url := string(out)
	if after, ok := strings.CutPrefix(url, "ssh://"); ok {
		url = after
		return strings.TrimSpace(strings.Replace(url, "/", ":", 1))
	}
	return strings.TrimSpace(url)
}

// ---- templates ----

// Template is a named selection of repositories, identified by origin URL
// (stable across rescans and path moves), not yet materialized into a
// workspace.
type Template struct {
	Name    string   `json:"name"`
	Origins []string `json:"origins"`
}

func (m *Manager) templatesPath() string {
	if m.TemplatesPath != "" {
		return m.TemplatesPath
	}
	return filepath.Join(filepath.Dir(m.CachePath), "devtemplates.json")
}

func (m *Manager) ReadTemplates() ([]Template, error) {
	data, err := os.ReadFile(m.templatesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ts []Template
	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, err
	}
	return ts, nil
}

func (m *Manager) writeTemplates(ts []Template) error {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	data, err := json.MarshalIndent(ts, "", "    ")
	if err != nil {
		return err
	}
	path := m.templatesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveTemplate upserts a template by name.
func (m *Manager) SaveTemplate(t Template) error {
	if t.Name == "" {
		return fmt.Errorf("template name is required")
	}
	if len(t.Origins) == 0 {
		return fmt.Errorf("template needs at least one repo")
	}
	ts, err := m.ReadTemplates()
	if err != nil {
		return err
	}
	replaced := false
	for i := range ts {
		if ts[i].Name == t.Name {
			ts[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		ts = append(ts, t)
	}
	m.Logger.Info("template saved", slog.String("name", t.Name), slog.Int("repos", len(t.Origins)))
	return m.writeTemplates(ts)
}

func (m *Manager) DeleteTemplate(name string) error {
	ts, err := m.ReadTemplates()
	if err != nil {
		return err
	}
	ts = slices.DeleteFunc(ts, func(t Template) bool { return t.Name == name })
	return m.writeTemplates(ts)
}

// ResolveTemplate maps a template's origins to currently-known repos,
// preferring the cache and falling back to a scan. Errors if any origin has
// no matching repo on disk.
func (m *Manager) ResolveTemplate(t Template) ([]Repo, error) {
	repos, err := m.ReadCache()
	if err != nil || len(repos) == 0 {
		repos, err = m.FindGitReposIn(filepath.Join(m.BaseDir, "projects"))
		if err != nil {
			return nil, err
		}
	}
	byOrigin := make(map[string]Repo, len(repos))
	for _, r := range repos {
		byOrigin[r.Origin] = r
	}

	var out []Repo
	var missing []string
	for _, o := range t.Origins {
		if r, ok := byOrigin[o]; ok {
			out = append(out, r)
		} else {
			missing = append(missing, o)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("template %q references repos not on disk: %s", t.Name, strings.Join(missing, ", "))
	}
	return out, nil
}

// ---- namespace naming ----

var adjectives = []string{
	"autumn", "bold", "calm", "dawn", "eager",
	"faint", "gentle", "hazy", "ivory", "jolly",
	"keen", "lush", "misty", "noble", "orange",
	"pale", "quiet", "rapid", "silver", "tidal",
}

var nouns = []string{
	"brook", "cliff", "dune", "elm", "field",
	"grove", "hill", "isle", "jade", "knoll",
	"lake", "mesa", "nest", "oak", "peak",
	"reef", "shore", "trail", "vale", "wind",
}

// NamespaceFromOrigins returns the deterministic human-readable namespace for
// a set of origin URLs (e.g. "calm-brook"). It is the single source of truth
// linking a template (stored as origins) to the workspaces created from it.
func NamespaceFromOrigins(origins []string) string {
	sorted := slices.Clone(origins)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return adjectives[int(h[0])%len(adjectives)] + "-" + nouns[int(h[1])%len(nouns)]
}

// Namespace returns the namespace for a set of repos (see
// NamespaceFromOrigins).
func Namespace(repos []Repo) string {
	origins := make([]string, len(repos))
	for i, r := range repos {
		origins[i] = r.Origin
	}
	return NamespaceFromOrigins(origins)
}
