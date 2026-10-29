# reposync

Keep the same set of git repos in the **same state on two Windows PCs**:
every local branch, the checked-out branch, and all uncommitted and untracked
work. Sit down at either machine, run `reposync sync`, and carry on where you
left off.

Status: **v0.1**. It works from the command line on your home network. A
background agent, tray icon and automatic triggers are planned for v0.2.

## How it works

- Git does the transport. reposync never copies files inside `.git` from one
  machine to the other, so it can't corrupt a repo the way Syncthing or
  OneDrive can.
- A **snapshot** is an ordinary git commit whose tree is "HEAD plus every
  uncommitted and untracked file". Files matched by `.gitignore` are left out.
  The repo's state (branch, HEAD, branch tips) is stored as JSON in the commit
  message. Taking a snapshot uses a throwaway index, so what you have staged
  stays as it is.
- Snapshots travel as **git bundles** over HTTP to a small agent
  (`reposync serve`) on the other machine. They're stored under
  `refs/reposync/...`, which never collides with your branches.
- **Who wins:** after each sync, both machines record what both sides looked
  like. On the next sync, each side is compared with that record:
  - only one side changed: it wins automatically (push or pull);
  - both changed, or no shared record (first sync, after `undo`): **nothing
    happens** until you pick a side with `--take <host>`. The losing side's
    work is then saved on a branch `sync/<host>/<branch>-<time>` on the
    winning machine, and from there it gets synced to both.
- **Backups:** before reposync changes a working copy, it saves the current
  state (`reposync backups`). `reposync undo` restores it. The last 20 backups
  are kept per repo.
- Repos in the middle of a merge, rebase or cherry-pick are skipped.

## Setup (once)

You need Git for Windows on both PCs.

1. Put `reposync.exe` in a folder on your `PATH`, e.g. `C:\Tools`.
   To build it yourself: `go build -o reposync.exe ./cmd/reposync` (Go 1.22+).

2. **Desktop.** `--peer` is how this machine reaches the other one: its
   Windows computer name (run `hostname` on it) or its IP address.
   ```
   reposync init --host desktop --peer MY-LAPTOP --root C:\code
   ```
   This prints a token. **Laptop:**
   ```
   reposync init --host laptop --peer MY-DESKTOP --root C:\code --token <token from desktop>
   ```

3. On both machines, add your repos to `%APPDATA%\reposync\config.toml`. Use
   the same list on both. Relative paths are resolved from `root`.
   ```toml
   [[repo]]
   name = 'api'
   path = 'work\api'
   url  = 'git@github.com:you/api.git'
   ```

4. Laptop: `reposync bootstrap` clones anything that's missing.

5. Desktop: `reposync serve`. When Windows Firewall asks, allow
   **Private networks** only.

6. Laptop: run the first sync and choose the desktop as the source:
   ```
   reposync status
   reposync sync --take desktop
   ```

Both machines must use the same `core.autocrlf` setting (Git for Windows
defaults to `true`). Otherwise every file looks changed.

## Daily use

The machine you're **not** sitting at must be running `reposync serve`. You
run the commands on the one you are sitting at. Either machine can start a
sync.

| When | On the laptop |
|---|---|
| Before leaving for the office | `reposync sync` (pulls the desktop's work) |
| Back home | `reposync sync` (pushes the laptop's work) |
| Anytime, to check | `reposync status` |

To start the agent at login, run this once on each machine:
```
schtasks /Create /TN reposync-serve /SC ONLOGON /TR "C:\Tools\reposync.exe serve"
```
In v0.1 this keeps a console window open. v0.2 replaces it with a tray app.

## Commands

| Command | |
|---|---|
| `init` | write this machine's config |
| `serve` | run the agent the other machine syncs with (default port 7420) |
| `status [repo...]` | what sync would do; changes nothing |
| `sync [--take HOST] [repo...]` | sync all or the named repos; `--take` settles conflicts and first syncs |
| `bootstrap` | clone repos missing on this machine |
| `backups REPO` | list saved states |
| `undo [--to ID] REPO` | restore a saved state (the newest by default) |

## Known limits of v0.1

- **Home network only.** It uses plain HTTP with a shared token, so don't
  forward port 7420 to the internet. Use Tailscale or similar later for
  syncing from the office.
- Staged and unstaged changes both arrive as **unstaged** changes.
- Stashes and ignored files (`.env`, local config) are not synced yet.
- Every sync sends the full history of the repo. That's fine on a LAN, but
  slow for very large repos. Incremental bundles are a TODO.
- Submodules sync only as pointers. Git LFS is untested.
- Windows file locks: if a running build or app has a file open, applying
  can fail. The backup is already taken; close the program and run sync
  again.

## Roadmap

- **v0.2**: tray agent that starts with Windows; syncs automatically when the
  peer appears on the network and when the laptop wakes; toast notifications;
  `reposync doctor` (checks autocrlf, long paths and other settings).
- **v0.3**: Tailscale transport, incremental bundles, stash and
  ignored-file allowlist, hooks after sync (`npm ci` when the lockfile
  changed).

## Development

```
go test ./...    # spins up two simulated machines with real git and HTTP
```

Layout: `internal/repo` (snapshot, bundle, apply, backups: all the git
work), `internal/node` (operations by repo name), `internal/agent` (HTTP
server and client), `internal/syncer` (decides who wins),
`cmd/reposync` (CLI).
