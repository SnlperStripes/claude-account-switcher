# Privacy policy

Claude Account Switcher runs on your computer only. It has no account, no server of its own, no analytics and no crash reporting. The maintainer receives nothing from it.

## What it sends, and where

| To | What | When |
| --- | --- | --- |
| `api.anthropic.com` | The sign-in token of each account you added, to read that account's profile and 5 hour and weekly usage. These are the same requests Claude and Claude Code make themselves. | Every one to ten minutes while the switcher runs |
| `api.github.com` | A request for the version number of the latest release. Nothing about you or your accounts. | Once a day. Off with `"noUpdateCheck": true` in `state.json` |
| `github.com` | The download of a new release file. | Only when you click to update |

Anthropic handles those requests under its own [privacy policy](https://www.anthropic.com/legal/privacy), GitHub under its [privacy statement](https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement).

## What it keeps on your computer

- Copies of Claude's sign-in entries and cookie file for each account you add, stored exactly as Claude encrypted them, never in plain text.
- Its settings, backups of the files it changes and a log, in `~/.claude-account-switcher` (`%USERPROFILE%\.claude-account-switcher` on Windows).
- On Windows, the profile of the WebView2 window that asks which chats continue. That window shows a page built into the switcher and loads nothing from the internet.

The files it reads and writes in Claude's own data folder are listed in the [README](README.md#how-it-works).

## Removing it

Turn off **Start at login**, quit the switcher, then delete the program file and the folder above. Nothing else stays behind.

Questions go to the [issue tracker](https://github.com/SnlperStripes/claude-account-switcher/issues), security problems to [SECURITY.md](SECURITY.md).
