general idea is:

- keep bare git repos in $HOME/src/projects
- use the tui to compose these into templates
- a template is a set of git repos which can be turned into a workspace
- a workspace is when all repos in a template are materialzied into new worktrees
- workspaces go in $HOME/src/workspaces/...
- opencode sessions which are opened within a workspace's path are tracked under that workspace
