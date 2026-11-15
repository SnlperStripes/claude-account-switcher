# Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io), certificate by [SignPath Foundation](https://signpath.org).

**Status:** Windows releases are not signed yet. Signing starts once the SignPath Foundation accepts the project. Until then every release, v0.3.9 included, is unsigned.

## What gets signed

Only the Windows release file `claude-account-switcher-windows-amd64.exe`.

- It is built by [`.github/workflows/build.yml`](.github/workflows/build.yml) on GitHub hosted runners, from a version tag in this repository, and from nothing else. No file built on a personal computer is ever signed.
- The build uploads the unsigned file to SignPath, a maintainer approves the signing request by hand, and the signed file goes into the GitHub release together with its `.sha256`.
- The file carries the product name "Claude Account Switcher" and the release version in its version info.
- Only code from this repository is signed. The Go modules it uses are compiled into that one file from source, as listed in [`go.mod`](go.mod).
- The macOS file is not covered. Apple only accepts its own Developer ID certificates, so the macOS build stays unsigned for now.

## Team roles

| Role | Members |
| --- | --- |
| Committers and reviewers | [SnlperStripes](https://github.com/SnlperStripes) |
| Approvers | [SnlperStripes](https://github.com/SnlperStripes) |

The project has one maintainer, who owns the repository and holds all three roles. Changes from anyone else arrive as pull requests and are reviewed before they are merged, with extra care for the workflow and build files, since those decide what gets signed. Everyone with a role uses multi factor authentication for GitHub and for SignPath.

## Privacy policy

See [PRIVACY.md](PRIVACY.md). In short: the switcher talks to `api.anthropic.com` for the accounts you add, and once a day to GitHub to look for a new version, which you can turn off. It sends nothing to the maintainer or anyone else.

## Check a download

- **Signature:** right click the exe, **Properties**, **Digital Signatures**. A signed release shows "SignPath Foundation".
- **Checksum:** every download has a `.sha256` file next to it with the SHA-256 of the file as published.

Found a signed file from this project that should not be signed? Report it privately, see [SECURITY.md](SECURITY.md), or to SignPath at support@signpath.io.
