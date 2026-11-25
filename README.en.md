# remote-console

[繁體中文](README.md)

A small web console that lets you watch and drive the Claude Code / Codex sessions running in tmux on your Mac, from your phone or another computer, over Tailscale. It shows each session's terminal, live status lights ("waiting for your permission", "asking you a question", "done"), lets you start new sessions from the browser, and has an on-screen key bar for phone use. The terminal never leaves your tailnet.

- **macOS only.** It relies on launchd, macOS `ps -E`, and the Tailscale macOS app.
- The service, binary, data directory and launchd labels are all named `dev-console`.
- The Chinese README is the detailed reference (architecture, status-light heuristics, troubleshooting); this file covers what you need to install, remove, and trust it.

## Requirements

- A Mac with [Homebrew](https://brew.sh).
- The [Tailscale](https://tailscale.com) app, signed in. Your phone / other computer must be on the same tailnet.
- tmux and ttyd (installed by `install.sh` via brew), plus `go` and `jq` (`brew install go jq`). Terminal scrolling needs tmux **3.0a or later** (`#{&&:}` / `#{||:}` format operators since 2.6, `send-keys -H` listed under 3.0 in CHANGES and confirmed in the 3.0a source tag; developed on 3.7c). Not tested on older versions, but from the source: before 2.6 the conditions expand to empty, so every scroll returns 409 "screen just changed" and nothing is sent; 2.6–3.0 lack `send-keys -H`, so finger (line) scrolling returns 500 while page scrolling and shell scrolling still work. Upgrade with `brew upgrade tmux`.
- Claude Code (`claude`) and/or Codex (`codex`).
- Optional: `aiuse`, a separate multi-account launcher (not part of this repo). Without it you get "direct mode" (see [Two modes](#two-modes)).

## Install

### The three questions

| Question | Variable | What to enter |
|---|---|---|
| 1. Your Tailscale login and tailnet name | `DC_ALLOWED_LOGINS`, `DC_TAILNET` | `DC_ALLOWED_LOGINS`: the Tailscale account email(s) allowed to use the console, comma-separated. **Required; there is no default account.** `DC_TAILNET`: the `<name>` in `<machine>.<name>.ts.net` (shown in the Tailscale app, or `MagicDNSSuffix` in `tailscale status --json`). |
| 2. Service name prefix | `DC_LABEL_PREFIX` | Reverse-DNS style, e.g. `com.example.remote`. The launchd labels become `<prefix>.dev-console.{web,ttyd,rc-<name>}`; uninstall removes only these. |
| 3. Keep Claude remote-control running for the phone app? | `DC_RC_PROFILE` | Leave unset for no. With aiuse: the Claude profile to keep running. In direct mode: any name (e.g. `main`); it only names the service and tmux session, and `claude remote-control` runs with your default account. |

### Steps

```bash
git clone https://github.com/chiyum/remote-console.git
cd remote-console

# 1) Dry run into a throwaway directory: no services are started, and tailscale,
#    launchctl and brew are not called. Everything lands under /tmp/rc-try.
TARGET_HOME=/tmp/rc-try NO_LOAD=1 \
DC_LABEL_PREFIX=com.example.remote \
DC_ALLOWED_LOGINS=you@example.com \
DC_TAILNET=example-tailnet \
./install.sh
TARGET_HOME=/tmp/rc-try ./uninstall.sh       # clean up the dry run

# 2) Real install (idempotent; safe to re-run)
DC_LABEL_PREFIX=com.example.remote \
DC_ALLOWED_LOGINS=you@example.com \
DC_TAILNET=example-tailnet \
./install.sh                                   # add DC_RC_PROFILE=<name> for remote-control

# 3) Pair each browser once
~/.local/share/dev-console/bin/dev-console pair
```

`install.sh` installs tmux/ttyd with brew, builds the Go binary, prints which mode it detected, writes launchd agents, saves your answers to `~/.local/share/dev-console/install.env` (mode 0600, never in the repo), merges status hooks into `~/.claude/settings.json` (and each aiuse Claude profile's settings.json), and runs `tailscale serve --https=443` pointing at `http://127.0.0.1:7690`.

- **Re-running / upgrading**: `git pull` and run `./install.sh` again. Only `DC_LABEL_PREFIX`, `DC_ALLOWED_LOGINS`, `DC_TAILNET` and `DC_RC_PROFILE` are read back from `install.env` when you don't pass them (setting one to an empty value clears it, e.g. `DC_RC_PROFILE=`; an empty `DC_ALLOWED_LOGINS` is rejected). Everything else (`DC_UI_PORT` / `DC_HOOK_PORT`, `DC_TTYD_*`, `DC_AIUSE_PATH`, `DC_CLAUDE_BIN` / `DC_CODEX_BIN`, `DC_TS_HOSTNAME`) is not remembered; pass it again if you changed the default. Existing pairings, nicknames, status lights and the hook token are kept, running tmux sessions are left alone, and an existing serve entry pointing at this service is not touched.
- **First use of serve on a tailnet**: `install.sh` prints the "Serve is not enabled" link instead of hanging. Enable it and re-run. Note that enabling HTTPS certificates publishes your machine name in Certificate Transparency logs.
- If the root path `/` on port 443 is already used by another serve target, `install.sh` stops before changing anything; it never overwrites your config.
- `TS_API_TOKEN=... scripts/tailscale-acl.sh --src you@example.com` adds a minimal ACL rule if your Mac is tagged (tagged devices are not in `autogroup:self`).

Other optional variables: `TARGET_HOME`, `NO_LOAD=1`, `DC_TS_HOSTNAME`, `DC_UI_PORT` / `DC_HOOK_PORT` (7690 / 7691, loopback only), `DC_AIUSE_PATH` (default `~/.local/bin/aiuse`), `DC_CLAUDE_BIN` / `DC_CODEX_BIN` (absolute paths for direct mode), `DC_TTYD_*` (see the Chinese README).

### Upgrading from an older version (dev-console, or the copy bundled in another kit)

Re-run `install.sh` from this repo **with the same label prefix**. The launchd labels, the data directory `~/.local/share/dev-console` and the tailscale serve target stay the same, so paired devices, nicknames and archive records and the hook token are kept and running tmux sessions are not affected; the web, ttyd and remote-control services restart once (a few seconds).

```bash
cd remote-console
DC_LABEL_PREFIX=<prefix used by the old install> DC_ALLOWED_LOGINS=<your login> DC_TAILNET=<your tailnet> \
DC_RC_PROFILE=<old remote-control name, omit if none> ./install.sh
```

- **Do not run the old `uninstall.sh` first**: it deletes the whole data directory (including the pairing records, so every browser must pair again) and removes the tailscale serve entry on 443.
- The prefix must match the old one (see `ls ~/Library/LaunchAgents | grep dev-console`); a different prefix installs a second set of services that fight over the same port.
- Old installs have no `install.env`, so pass the three required values the first time; they are remembered afterwards.

## Uninstall

> **Warning:** `uninstall.sh` deletes the whole data directory `~/.local/share/dev-console`, **including the paired-device records** (every browser must pair again), session nicknames and archive records, status-light state, the hook token and `install.env`. To upgrade, just re-run `./install.sh` instead.

```bash
./uninstall.sh                         # prefix is read back from install.env
TARGET_HOME=<dir> ./uninstall.sh       # for a NO_LOAD / non-home install: only deletes files
```

It stops and removes the `<prefix>.dev-console.*` launchd agents (other agents with the same prefix are untouched), removes only the hooks it added (tagged `src=dev-console-hook`), removes only the `tailscale serve` handler for `/` on 443 that points at this service (other paths on 443 stay), closes the `rc-*` tmux sessions it created, and deletes the data and log directories (`DC_KEEP_LOGS=1` keeps logs). Running it again prints "already uninstalled" and exits 0. It does not remove tmux/ttyd, aiuse, your Tailscale ACL, or any other tmux session.

## Security model

- **Loopback only.** The web service listens on `127.0.0.1:7690`, the hook receiver on `127.0.0.1:7691`. ttyd has no TCP port at all; it listens on a unix socket in a 0700 directory that only the web service connects to. Nothing is reachable from your LAN or the internet.
- **Tailnet only, no funnel.** Remote access goes exclusively through `tailscale serve` on your tailnet. The installer never enables Funnel and warns if Funnel is on. The hook port is never exposed through serve.
- **Loopback is not identity.** Any local process (including Docker containers) can reach 127.0.0.1 and forge headers, so access requires a secret only the Mac's user can read:
  - **Pairing code → cookie.** `dev-console pair` writes a one-time code (10 minutes, single use, invalidated after 5 wrong tries) to a 0600 file. The browser exchanges it with `POST /auth/pair` (Origin must match) for an HttpOnly, SameSite=Strict cookie (90 days over tailnet with `Secure`; 12 hours on `http://dev-console.localhost:7690`). `dev-console devices` lists and `dev-console revoke-all` revokes them.
  - **hook-token.** Claude hooks must send `X-DC-Hook-Token`, read by curl from a 0600 header file so the token never appears in `ps` output.
  - **`DC_ALLOWED_LOGINS`.** A request carrying a Tailscale login that is not on the list is rejected. The list is required at install time; there is no default account.
- **Write APIs** (start session, nickname, archive, terminal keys) additionally require `Content-Type: application/json`, an `X-Dev-Console: 1` header and a matching `Origin`. The terminal WebSocket checks `Origin` too. The `Host` must be your tailnet name or `dev-console.localhost` (DNS-rebinding protection).
- **Input validation.** Session names, profiles and directories are whitelisted: the directory must resolve (realpath) inside your home directory and exist; in aiuse mode the profile must be an existing directory under `~/.ai-profiles/{claude,codex}/` with exactly that name; in direct mode any profile value is rejected. tmux is always called with an argument array, never through a shell.
- **CSP.** The overview page uses `script-src 'self'` (no inline script). `/term/` has to allow inline script because ttyd's page is a single inline file.

## Two modes

The mode is decided each time you start a session from the browser: if `DC_AIUSE_PATH` (default `~/.local/bin/aiuse`) is an existing executable file you are in **aiuse mode**, otherwise in **direct mode**. Installing or removing aiuse later needs no reinstall. Overview, terminal, status lights, nicknames/archive and tasks work the same in both modes.

| | aiuse mode | direct mode |
|---|---|---|
| New session in the browser | Choose Claude/Codex, a profile (directories under `~/.ai-profiles/{claude,codex}/`) and a directory | Choose Claude/Codex and a directory; **no profile menu** |
| What runs | `aiuse claude <profile>`; for Codex, aiuse creates the session and the console claims it | `claude` / `codex`, resolved to an absolute path from the session PATH (or `DC_CLAUDE_BIN` / `DC_CODEX_BIN`) and passed to tmux as an argument array |
| Session names | `aiuse-claude-<profile>-<UTC>-<hex>`, `aiuse-codex-<profile>-*` | `dc-claude-<UTC>-<hex>`, `dc-codex-<UTC>-<hex>` |
| Command missing | aiuse's own error | "claude/codex not found"; no tmux session is created |
| Remote-control service | `aiuse claude <profile> remote-control` | `claude remote-control` with the default account; a real install stops if `claude` cannot be found, so you never get a service that keeps failing |

Codex sessions started through aiuse are claimed by protocol markers, not by a list of profile names: both the token-based protocol (`codex-direct-v1` + `@aiuse_launch_token`) and the older token-less markers (`codex-direct-v1`, `codex-v1`) are supported. A session carrying a different token is never claimed; token-less sessions are only accepted after aiuse exits successfully and only if exactly one candidate appeared; launches for the same profile are serialized so two concurrent launches each claim their own session. See `docs/adr/0008-standalone-repo-dual-protocol-direct-mode.md`.

**Known limitations of direct mode** (by design):

- `claude` / `codex` started outside tmux (e.g. directly in iTerm) are **not shown**; read-only cards only recognize sessions started through aiuse profiles.
- Cards **do not show conversation content** (last reply, tool, tokens); status lights still work.
- No multiple accounts: `claude` / `codex` always use their default accounts.

## Use

- Overview: `https://<machine>.<tailnet>.ts.net/` (locally: `http://dev-console.localhost:7690/`).
- Terminal: the "open terminal" button on a card (`/term/?arg=<tmux session>`). On phones a key bar (Esc, Tab, Shift-Tab, arrows, Enter, 1–4, Ctrl-C, ▲ scroll up, ▼ scroll down) sits at the bottom.
- Scrolling on phones (ADR 0009): Claude and Codex are full-screen programs that handle scrolling themselves, and touch does not produce wheel events, so the terminal could not be scrolled on a phone. Now:
  - ▲ / ▼ scroll one page per tap (the program's own PageUp / PageDown; Claude Code 2.1.332 moves about 17 rows on a 40-row screen, Codex 0.170.0 about a full page).
  - Dragging a finger up or down on the terminal scrolls like any phone app (drag down to see older output), one row per row-height of finger movement. A touch that moves less than about 10 device pixels is still a tap (focuses the terminal and opens the keyboard); pinch zoom and mostly-horizontal drags are left alone; there is no momentum. Desktop mouse wheel and drag-to-select are unchanged.
  - The scroll position belongs to the program, so a Mac attached to the same session sees the same scrolled view.
  - Plain shells scroll through tmux copy-mode (`copy-mode -e`, leaves automatically at the bottom; press Esc to leave early). Other full-screen programs that do not enable mouse reporting (vim, less, …) get PageUp / PageDown from the buttons, but finger drags are refused rather than typing escape sequences into them.
  - API: `POST /api/sessions/scroll {"session","action","count"}` — `action` is one of `page-up`, `page-down`, `line-up`, `line-down`; `count` is 1–30 for line scrolling. The server reads the pane flags and decides what to send; the actual send is a single `if-shell -F` call, so tmux re-checks the active pane and flags and sends nothing (409) if they changed in between. Nothing user-supplied reaches `send-keys`. After a 409 the current finger drag stops sending; lift and drag again.
- On the Mac: `tmux attach -t <name>`.
- The "tasks and watchdog" panel is optional. It shows task state files that the author's own separate workflow writes under `~/.claude/state/`. On a machine without those files the panel is simply empty; nothing else depends on it and you do not need to create anything.
- The web UI and the installer's messages are in Traditional Chinese only.

## Development

```bash
go build ./... && go vet ./... && go test ./...
scripts/check.sh        # gofmt, vet, tests, shell syntax
```

`scripts/check-shell-vars.sh` (also run by `scripts/check.sh`) rejects `$name` immediately followed by a non-ASCII character: macOS's bash 3.2 merges such bytes into the variable name under UTF-8 locales and aborts under `set -u`. Scripts use a completion flag in their EXIT trap because bash 3.2 otherwise exits 0 after an unbound-variable abort. `scripts/test-install-isolation.sh` runs install/uninstall against stub `tmux`/`launchctl`/`tailscale`/`brew`/`ttyd` and fails if any of them is called in generate-only mode. `install.sh`/`uninstall.sh` never touch launchd, Tailscale or tmux unless `TARGET_HOME` is the account's real home directory (looked up with `dscl`, not `$HOME`).

**Isolating tmux when testing.** If your own sessions run inside tmux, the tmux client uses the socket in `$TMUX` and ignores `TMUX_TMPDIR`, so a "test" `kill-server` can kill all your real sessions. Always run test tmux commands as `env -u TMUX -u TMUX_PANE tmux -S <dedicated socket>` (or `-L <name>`), check first that the isolated `list-sessions` shows none of your sessions, start the service under test with `env -i` and point `DC_TMUX_PATH` at a wrapper that always adds `-S` (see the Chinese README for the script), set `DC_TTYD_WATCH_INTERVAL=0` and a non-existent `DC_TTYD_LABEL`, and stop processes by the PID you recorded.

## License

MIT. See [LICENSE](LICENSE).
