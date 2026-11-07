package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/transcript"
)

// The start form's headings when no repository groups are configured.
const (
	groupRecent     = "Recent"
	groupWorkspaces = "Workspace: across repositories"
	groupOther      = "Other repositories"
)

type Repo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Workspace: a root itself, for sessions that work across its repositories.
	Workspace bool `json:"workspace,omitempty"`
	// Group is the start form's heading the folder is listed under; empty lists it under none.
	Group string `json:"group,omitempty"`
}

// RepoGroup is one heading of the start form's repository list. An empty Title lists its repositories under no
// heading.
type RepoGroup struct {
	Title string       `json:"title"`
	Repos []RepoSource `json:"repos"`
}

// RepoSource is one config entry of a group: {"repo": "/dir/name"} one repository, {"all": "/dir"} every git
// repository directly in a folder, {"some": "/dir/{a, b}"} the named ones of a folder, {"workspace": "/dir"} a
// folder for work across repositories.
type RepoSource struct {
	// Dir is the folder the repositories are in, or the workspace itself.
	Dir string
	// Names are the chosen repositories in Dir; nil with All and Workspace.
	Names     []string
	All       bool
	Workspace bool
}

func (s *RepoSource) UnmarshalJSON(data []byte) error {
	var entry map[string]string
	if err := json.Unmarshal(data, &entry); err != nil {
		return err
	}
	if len(entry) != 1 {
		return fmt.Errorf("repository entry %s: want one of repo, all, some, workspace", data)
	}
	for kind, value := range entry {
		if value == "" {
			return fmt.Errorf("repository entry %s: empty path", data)
		}
		switch kind {
		case "repo":
			value = filepath.Clean(value)
			*s = RepoSource{Dir: filepath.Dir(value), Names: []string{filepath.Base(value)}}
		case "all":
			*s = RepoSource{Dir: filepath.Clean(value), All: true}
		case "workspace":
			*s = RepoSource{Dir: filepath.Clean(value), Workspace: true}
		case "some":
			dir, list, ok := strings.Cut(value, "/{")
			list, closed := strings.CutSuffix(list, "}")
			if !ok || !closed {
				return fmt.Errorf("repository entry %s: want /dir/{a, b}", data)
			}
			*s = RepoSource{Dir: filepath.Clean(dir)}
			for name := range strings.SplitSeq(list, ",") {
				if name = strings.TrimSpace(name); name == "" {
					return fmt.Errorf("repository entry %s: empty name", data)
				}
				s.Names = append(s.Names, name)
			}
		default:
			return fmt.Errorf("repository entry %s: unknown type %q, want repo, all, some or workspace", data, kind)
		}
	}
	return nil
}

// Repos lists the places a session can start, in the start form's order. With repository groups configured: the
// groups' repositories and workspaces in config order, each once at its first place, then the workspaces no group
// lists. Without: the git
// repositories Claude ran in before (latest first), the configured workspaces and each root as a workspace, then
// the other git repositories directly under the roots and the workspaces.
func (m *Manager) Repos() ([]Repo, error) {
	workspaces := make([]Repo, 0, len(m.cfg.Workspaces)+len(m.cfg.Roots))
	for _, dir := range slices.Concat(m.cfg.Workspaces, m.cfg.Roots) {
		workspaces = append(workspaces, Repo{Name: filepath.Base(dir), Dir: filepath.Clean(dir), Workspace: true, Group: groupWorkspaces})
	}
	if len(m.cfg.RepoGroups) > 0 {
		grouped, err := m.groupedRepos()
		if err != nil {
			return nil, err
		}
		placed := map[string]bool{}
		for _, repo := range grouped {
			placed[repo.Dir] = true
		}
		return slices.Concat(grouped, slices.DeleteFunc(workspaces, func(w Repo) bool { return placed[w.Dir] })), nil
	}
	recent, err := m.recentRepos()
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, repo := range recent {
		listed[repo.Dir] = true
	}
	repos := []Repo{}
	for _, parent := range workspaces {
		names, err := gitReposIn(parent.Dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if dir := filepath.Join(parent.Dir, name); !listed[dir] {
				repos = append(repos, Repo{Name: name, Dir: dir, Group: groupOther})
			}
		}
	}
	slices.SortFunc(repos, func(a, b Repo) int { return strings.Compare(a.Name, b.Name) })
	return slices.Concat(recent, workspaces, repos), nil
}

// groupedRepos lists the configured groups' repositories. A named repository that is no git repository is an
// error, so a typo in the config shows instead of the repository silently missing.
func (m *Manager) groupedRepos() ([]Repo, error) {
	seen := map[string]bool{}
	var repos []Repo
	for _, group := range m.cfg.RepoGroups {
		for _, source := range group.Repos {
			if source.Workspace {
				if seen[source.Dir] {
					continue
				}
				if info, err := os.Stat(source.Dir); err != nil || !info.IsDir() {
					return nil, fmt.Errorf("repository group %q: workspace %s is not a folder", group.Title, source.Dir)
				}
				seen[source.Dir] = true
				repos = append(repos, Repo{Name: filepath.Base(source.Dir), Dir: source.Dir, Workspace: true, Group: group.Title})
				continue
			}
			names := source.Names
			if source.All {
				var err error
				if names, err = gitReposIn(source.Dir); err != nil {
					return nil, err
				}
			}
			for _, name := range names {
				dir := filepath.Join(source.Dir, name)
				if seen[dir] {
					continue
				}
				if !isGitRepo(dir) {
					return nil, fmt.Errorf("repository group %q: %s is not a git repository", group.Title, dir)
				}
				seen[dir] = true
				repos = append(repos, Repo{Name: name, Dir: dir, Group: group.Title})
			}
		}
	}
	return repos, nil
}

// gitReposIn names the git repositories directly in dir, sorted.
func gitReposIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && isGitRepo(filepath.Join(dir, entry.Name())) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// recentRepos are the folders of Claude Code's transcripts that are git repositories, latest first, each once.
func (m *Manager) recentRepos() ([]Repo, error) {
	if m.cfg.Transcripts == "" {
		return nil, nil
	}
	conversations, err := transcript.List(m.cfg.Transcripts)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var repos []Repo
	for _, c := range conversations {
		dir := filepath.Clean(c.Dir)
		if c.Dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if isGitRepo(dir) {
			repos = append(repos, Repo{Name: filepath.Base(dir), Dir: dir, Group: groupRecent})
		}
	}
	return repos, nil
}

// repoAt returns the listed repository or workspace at dir. Only listed ones qualify, so a request cannot start
// Claude in an arbitrary folder.
func (m *Manager) repoAt(dir string) (Repo, error) {
	repos, err := m.Repos()
	if err != nil {
		return Repo{}, err
	}
	clean := filepath.Clean(dir)
	for _, repo := range repos {
		if repo.Dir == clean {
			return repo, nil
		}
	}
	return Repo{}, fmt.Errorf("%w: %q", ErrInvalidRepo, dir)
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
