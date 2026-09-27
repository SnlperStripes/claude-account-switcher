# Packaging

## winget

`winget/manifests/` mirrors the layout of [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs).
To publish a release:

1. Copy the version folder, bump `PackageVersion`, `ReleaseDate`, the URLs and `InstallerSha256`
   (the hash is in the release's `.sha256` file, upper case).
2. Check it locally:
   ```bash
   winget validate packaging/winget/manifests/s/SnlperStripes/ClaudeAccountSwitcher/<version>
   ```
3. Open a pull request to microsoft/winget-pkgs with the same folder, or let
   [`wingetcreate update`](https://github.com/microsoft/winget-create) do steps 1 and 3.
