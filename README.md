# reposync

Keep the same set of git repos in the **same state on two Windows PCs**:
every local branch, the checked-out branch, and all uncommitted and untracked
work. Sit down at either machine and carry on where you left off.

Status: **v0.2**. A tray app syncs automatically on your home network. You
can also sync from the command line.

## How it works

- Git does the transport. reposync never copies files inside `.git` from one
  machine to the other, so it can't corrupt a repo the way Syncthing or
  OneDrive can.
- A **snapshot** is an ordinary git commit whose tree is "HEAD plus every
  uncommitted and untracked file". Files matched by `.gitignore` are left out.
  The repo's state (branch, HEAD, branch tips) is stored as JSON in the commit
  message. Taking a snapshot uses a throwaway index, so what you have staged
  stays as it is.
- Snapshots travel as **git bundles** over HTTP to the reposync agent on the
  other machine. They're stored under `refs/reposync/...`, which never
  collides with your branches.
- **Who wins:** after each sync, both machines record what both sides looked
  like. On the next sync, each side is compared with that record:
  - only one side changed: it wins automatically (push or pull);
  - both changed, or no shared record (first sync, after `undo`): **nothing
    happens** until you pick a side (tray menu, or `--take <host>`). The
    losing side's work is then saved on a branch
    `sync/<host>/<branch>-<time>` on the winning machine, and from there it
    gets synced to both.
- **Lock:** a sync holds a lock on the repo on both machines while it runs.
  The two tray apps and the command line never sync the same repo at the same
  time.
- **Backups:** before reposync changes a working copy, it saves the current
  state (`reposync backups`). `reposync undo` restores it. The last 20 backups
  are kept per repo.
- Repos in the middle of a merge, rebase or cherry-pick are skipped.

## Setup (once per machine)

You need Git for Windows on both PCs.

1. Put `reposync.exe` and `reposync-tray.exe` in the same folder, e.g.
   `C:\Tools\reposync`. Add that folder to your `PATH`.
   To build them yourself (Go 1.24+):
   ```
   go build -o reposync.exe ./cmd/reposync
   go build -ldflags "-H windowsgui" -o reposync-tray.exe ./cmd/reposync-tray
   ```

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

5. Both machines: start the tray app and have it start at login.
   ```
   reposync autostart on
   reposync-tray
   ```
   When Windows Firewall asks, allow **Private networks** only.

6. Check the setup, then do the first sync with the desktop as the source:
   ```
   reposync doctor
   reposync sync --take desktop
   ```

## Daily use

With the tray app running on both PCs, there's usually nothing to do:

| When | What happens |
|---|---|
| You open the laptop at home | It finds the desktop and pulls its work. The laptop shows "Work from desktop arrived". |
| At the office | The laptop works on its own. The icon is grey because the desktop isn't reachable. |
| Back home | The laptop finds the desktop and pushes. The desktop shows "Work from laptop arrived". |
| Both online | It checks again every 10 minutes and after the laptop wakes from sleep. |
| You changed the same repo on both | The icon turns orange and you get a "needs a decision" notification. Pick a side under **Needs a decision** in the tray menu. |

**Save your files before switching machines.** reposync only syncs what's on
disk. If an editor has unsaved changes to a file that then gets updated by a
sync, the editor will ask which version to keep.

Tray icon colours: **green** in sync · **blue** syncing · **orange** needs
you · **grey** other machine not reachable, or paused.

Tray menu: **Sync now**, **Needs a decision** (only when needed),
**Pause automatic sync**, **Show status…** (opens `reposync status` in a
console), **Open config**, **Open log**, **Quit**.

## Settings

In `config.toml`:

```toml
[auto]
enabled  = true    # false: the tray app only syncs when you click "Sync now"
interval = '10m'   # how often to re-check while both PCs are reachable (min 1m)
notify   = true    # Windows notifications
```

## Commands

| Command | |
|---|---|
| `init` | write this machine's config |
| `status [repo...]` | what sync would do; changes nothing |
| `sync [--take HOST] [repo...]` | sync all or the named repos; `--take` settles conflicts and first syncs |
| `doctor` | compare both machines' setup: repo lists, git settings like `core.autocrlf`, git version, stuck locks |
| `autostart on\|off\|status` | start the tray app at login (per user, no admin needed) |
| `serve` | run the agent without the tray app (e.g. in a terminal) |
| `bootstrap` | clone repos missing on this machine |
| `backups REPO` | list saved states |
| `undo [--to ID] REPO` | restore a saved state (the newest by default) |

The tray app writes its log to `%LOCALAPPDATA%\reposync\agent.log`.

## Known limits

- **Home network only.** It uses plain HTTP with a shared token, so don't
  forward port 7420 to the internet. Use Tailscale or similar later for
  syncing from the office.
- Notifications are shown as coming from "Windows PowerShell". That avoids
  needing an installer.
- Staged and unstaged changes both arrive as **unstaged** changes.
- Stashes and ignored files (`.env`, local config) are not synced yet.
- Every sync that moves work sends the full history of the repo. That's fine
  on a LAN, but slow for very large repos. Incremental bundles are a TODO.
- Submodules sync only as pointers. Git LFS is untested.
- Windows file locks: if a running build or app has a file open, applying
  can fail. The backup is already taken; close the program and sync again.

## Roadmap

- **v0.3**: Tailscale transport (sync from the office), incremental bundles,
  syncing stashes and an allowlist of ignored files, hooks after sync (`npm ci`
  when the lockfile changed).

## Development

```
go test ./...    # spins up simulated machines with real git and HTTP
```

Layout: `internal/repo` (snapshot, bundle, apply, backups, lock: all the
git work), `internal/node` (operations by repo name), `internal/agent` (HTTP
server and client), `internal/syncer` (decides who wins),
`internal/auto` (when to sync on its own), `internal/doctor`,
`internal/desktop` (Windows notifications and autostart),
`cmd/reposync` (CLI), `cmd/reposync-tray` (tray app).
