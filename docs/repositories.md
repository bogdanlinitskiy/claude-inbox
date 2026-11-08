# Repositories in the start form

With no configuration, **New session** offers the repositories Claude ran in before (read from
`~/.claude/projects`), most recent first, then the git repositories in `roots`. That is usually enough.

`roots` and `workspaces` add folders; `repo_groups` replaces the whole list with your own.

## roots and workspaces

```json
{
  "roots": ["~/work", "~/libs"],
  "workspaces": ["~/work/shared"]
}
```

- `roots` — folders whose git repositories can host a session. Each root is also offered as a workspace.
- `workspaces` — further folders a session can start in, for work that spans repositories.

Both take `--roots` / `--workspaces` as comma-separated flags.

## repo_groups

`repo_groups` lists the start form's repositories top to bottom, under headings, in place of the recent and other
repositories:

```json
"repo_groups": [
  {"title": "Important", "repos": [{"some": "~/work/{wallet, gateway}"}, {"repo": "~/libs/constants"}]},
  {"title": "", "repos": [{"repo": "~/tools/scripts"}]},
  {"title": "Other", "repos": [{"all": "~/work"}, {"all": "~/libs"}]}
]
```

Each entry in `repos` takes exactly one of:

| Key | Means |
|---|---|
| `repo` | One repository |
| `some` | The named repositories of a folder, `~/work/{wallet, gateway}` |
| `all` | Every git repository directly in a folder |
| `workspace` | A folder for work across repositories |

An empty `title` lists its repositories under no heading. A repository is listed once, at its first place, so an
`all` after a `some` lists only the rest. Workspaces from `roots` and `workspaces` that no group lists follow the
groups.

A named repository that is not a git repository is an error in the start form, so a typo shows instead of
disappearing.

## Shorter names

`strip_prefix` leaves a prefix out of repository names in the list and in tmux session names: with
`"strip_prefix": "acme-"`, `acme-wallet` is shown as `wallet`.
