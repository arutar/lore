# Windows MSI

The per-machine MSI installs the Lore CLI, React + WebView2 companion, Explorer context menu and icon overlays into `C:\Program Files\Lore`. Windows Installer owns file replacement, COM registration, the machine `PATH` entry and removal.

## Build

```powershell
platform/windows/Build-Client.ps1
platform/windows/Build-Msi.ps1 -Version 0.1.0
platform/windows/Test-Msi.ps1
```

Outputs in `platform/windows/bin`:

- `LoreClient-x64.msi` — per-machine installer with a stable upgrade code;
- `LoreClient-win-x64.zip` — portable files, without MSI registration;
- `msi-smoke/*.log` — install/uninstall logs from the smoke test.

`Build-Msi.ps1` uses WiX Toolset 7.0.1 and passes `-acceptEula wix7`. See the [Open Source Maintenance Fee terms](https://wixtoolset.org/osmf/).

## Install and remove

WebView2 Runtime must already be installed. The PowerShell installer in `platform/clients/Install-Windows.ps1` can install it automatically; a WiX Burn setup bundle with the Runtime prerequisite is not implemented yet.

```powershell
msiexec /i LoreClient-x64.msi /qn /norestart
msiexec /x LoreClient-x64.msi /qn /norestart
```

`Test-Msi.ps1` performs that install/uninstall cycle, checks the CLI, Explorer registration and shell smoke test. Run it on a disposable Windows VM or GitHub runner, not on a developer machine with an existing Lore installation.

## GitHub releases

`.github/workflows/windows-client.yml` builds and tests the client on pull requests and `main`. A tag of the form `platform-vX.Y.Z` runs the same validation and publishes the MSI, ZIP and `SHA256SUMS` to GitHub Releases.

Create the `platform-release` environment in repository settings and require reviewer approval before the first release. The release job uses only the built-in `GITHUB_TOKEN`.

Code signing is not configured yet. Signing certificates must be added through a secure signing workflow before production distribution; private keys must not be stored in the repository.
