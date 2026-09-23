# Claude Account Switcher

A small tray tool for the Claude desktop app. Right-click it to see every
Claude account you use, with its plan usage, and switch the app to another
account in a few seconds. You do not need to sign in again or wait for an
email code, and your Claude Code chats stay visible in every account.

Unofficial. Not affiliated with or endorsed by Anthropic.

```
✓ Personal (Pro)              5h 24% · week 4%
  Work (Max)                  5h 100% until 19:00 · week 47%
  ─────────────────────────
  Auto-switch when nearly full
  Auto-switch at            ▸ 90% 94% 96% 98%
  Add account…
  Import chats from all accounts
  ─────────────────────────
  Start at login
  Open data folder
  Quit
```

The tray icon is a ring that fills with the active account's 5-hour usage.
It turns orange at 75% and red at the auto-switch threshold.

## Platforms

| | |
|---|---|
| Windows 10/11 | Microsoft Store app and the direct-download app |
| macOS | Builds in CI, not yet tested on a real Mac. Reports welcome |
| Linux | No official Claude desktop app, nothing to switch |

## Install

Download the binary for your platform from Releases and start it. On macOS,
the first start asks once whether the switcher may read "Claude Safe Storage"
from your keychain. That is the key the Claude app uses to encrypt its
sign-in, and the switcher needs it to read usage.

Turn on **Start at login** in the menu to keep it running.

On Windows, programs started from inside the Store version of Claude (for
example from a Claude Code terminal) see a redirected AppData folder. If you
start the switcher that way, it notices and starts itself again through
Explorer, so it is not affected when Claude quits.

## First run

1. Sign in to the Claude app as usual. The switcher picks up that account.
2. Choose **Add account…**. Claude restarts on its sign-in screen. Your first
   account is kept. Sign in with the second account.
3. The switcher notices the new sign-in and restarts Claude once more, so
   the chats of your other accounts show up.

From then on, click an account to switch. Claude quits, the sign-in is swapped,
chats are synced and Claude starts again. Open the chat you were in and keep going.

Do not use **Sign out** in Claude to change accounts. Signing out can end that
sign-in on Anthropic's side, and the switcher's saved copy stops working. If
that happens, the account shows "signed out, add it again".

## Auto-switch

Off by default. When it is on and the active account passes the threshold
(96% by default) of its 5-hour or weekly limit, the switcher moves Claude to
the account with the most room left. That account needs to be at least 10
points below the threshold. It never switches twice within 10 minutes and
never starts Claude if it is closed.

A switch restarts Claude. A reply that is being written at that moment is
cut off, which is why the default threshold is 96% and not 100%.

Anthropic's terms do not forbid having several accounts, but they do forbid
"bypassing any of our systems or protective measures". Decide for yourself
whether automatic switching fits how you use your accounts.

## How it works

The Claude desktop app keeps its sign-in in two places inside its data folder
(`%APPDATA%\Claude` on Windows, or
`%LOCALAPPDATA%\Packages\Claude_<id>\LocalCache\Roaming\Claude` for the
Microsoft Store app, and `~/Library/Application Support/Claude` on macOS):

- `config.json`: the entries `oauth:tokenCacheV2`, `oauth:tokenCache` and
  `lastKnownAccountUuid`
- `Network/Cookies`: the claude.ai web session

A switch quits Claude, saves those for the current account, puts the target
account's copy back and starts Claude. All other settings in `config.json`
stay as they are. Before each switch, `config.json` and the cookie store are
backed up (the last 10 are kept).

Chats: Claude lists Claude Code chats from
`claude-code-sessions/<account>/<organization>/`, one small file per chat.
The conversations themselves live in `~/.claude/projects`, shared by all
accounts. The switcher copies missing chat entries into the account you
switch to, and refreshes entries that are older than another account's copy.
It never deletes anything, and chats you deleted in any account are not copied back.

Usage: the switcher decrypts the saved token cache in memory with the app's
own key (DPAPI on Windows, the keychain on macOS). It then asks
`api.anthropic.com` for the account's profile and usage, the same endpoints
the app and Claude Code use. It checks the active account every minute and
the others every 5 minutes. App tokens are valid for about a month, so a
saved account keeps showing live usage without being signed in.

## Your data

- Saved sign-ins, state and log: `~/.claude-account-switcher` (on Windows
  `%USERPROFILE%\.claude-account-switcher`). Use **Open data folder**.
- Saved tokens stay encrypted exactly as the Claude app stored them. The
  switcher never writes a token in plain text and sends it nowhere except
  `api.anthropic.com`.
- To remove an account, quit the switcher, delete its folder under
  `accounts/` and its entry in `state.json`.

## Command line

```
claude-account-switcher -status
```

prints the known accounts and their usage and exits.

## Build

Go 1.26 or newer.

```
go test ./...
go build -ldflags "-H=windowsgui -s -w" -o claude-account-switcher.exe .   # Windows
CGO_ENABLED=1 go build -o claude-account-switcher .                        # macOS
```

## Caveats

This relies on how the Claude desktop app stores its files today, not on a
public interface. An app update can break it. If a switch fails, the
switcher restores the backup and reports the error in the menu.

## License

MIT
