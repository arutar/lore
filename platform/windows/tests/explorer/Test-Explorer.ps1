# Lore Explorer integration test: every operation goes through the Explorer context menu
# (LoreShell -> Companion window), and after each step the whole working copy is checked:
#   overlays  - what Explorer draws on every file and folder (asked from LoreShell's overlay handlers)
#   disk      - every file in the working copy still has the content it should
#   lore      - `lore status --scan` agrees (modified / staged sets)
#   server    - a second working copy synced from the server holds exactly the committed files,
#               and `lore lock query` lists exactly the expected locks
# Runs in the signed-in user's desktop session (saved Lore sign-in, Explorer running), against an
# existing repository the signed-in account can write to; Run-ClientTests.ps1 creates one. Prints
# one line per step and writes results.json to -Root.
param(
    [Parameter(Mandatory)] [string] $RepositoryUrl,
    [string] $Root = (Join-Path $env:TEMP 'lore-explorer-test'),
    # A command that restarts the Lore server; adds the "locks survive a restart" step.
    [string] $RestartCommand = '',
    [string] $Lore = 'C:\Program Files\Lore\lore.exe'
)
$ErrorActionPreference = 'Stop'
$lore = $Lore
$here = Split-Path $MyInvocation.MyCommand.Path
Add-Type -Path (Join-Path $here 'ShellDriver.cs') -ReferencedAssemblies @('System.Collections', 'System.Linq', 'System.Runtime.InteropServices', 'System.Threading', 'System.Threading.Thread', 'System.Console') -IgnoreWarnings -WarningAction SilentlyContinue

$wc = Join-Path $Root 'wc'
$verify = Join-Path $Root 'verify'
Get-Process LoreCompanion -ErrorAction SilentlyContinue | Stop-Process -Force
if (Test-Path $Root) { Remove-Item $Root -Recurse -Force }
New-Item -ItemType Directory $wc, $verify | Out-Null

$results = [System.Collections.Generic.List[object]]::new()
$local = [ordered]@{}   # path -> content expected on disk in the working copy
$remote = [ordered]@{}  # path -> content expected on the server
$locks = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$states = @{}           # path -> overlay expected (files only; folders are derived)

function Write-File($path, $text) {
    $full = Join-Path $wc $path
    New-Item -ItemType Directory -Force (Split-Path $full) | Out-Null
    [IO.File]::WriteAllText($full, $text)
    $local[$path] = $text
}

# --- the Explorer menu and the Companion window -------------------------------------------
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$ui = [System.Windows.Automation.AutomationElement]
$scope = [System.Windows.Automation.TreeScope]

# The Companion page is reached through UI Automation (the accessibility tree WebView2 exposes),
# so fields are filled and buttons pressed the way a person or a screen reader does.
function Get-Elements($window) {
    $window.FindAll($scope::Descendants, [System.Windows.Automation.Condition]::TrueCondition) | ForEach-Object {
        [pscustomobject]@{ Type = $_.Current.ControlType.ProgrammaticName -replace '^ControlType\.', ''; Name = $_.Current.Name; Enabled = $_.Current.IsEnabled; Element = $_ }
    }
}
# A button by its label: the exact name, or the name it starts with (a choice button's name is
# its label followed by what it does; "Revert…" ends with an ellipsis).
function Find-Button($elements, [string] $name) {
    $enabled = @($elements | Where-Object { $_.Type -eq 'Button' -and $_.Enabled })
    $exact = $enabled | Where-Object { $_.Name -eq $name } | Select-Object -First 1
    if ($exact) { return $exact }
    $enabled | Where-Object { $_.Name.StartsWith($name) } | Select-Object -First 1
}
function Press($window, [string] $name) {
    $button = Find-Button (Get-Elements $window) $name
    if (-not $button) { throw "No enabled '$name' button" }
    $button.Element.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
}
# The choice buttons a question offers (ChoiceRequired); one on screen means the window asks.
$questionButtons = 'Commit and sync', 'Sync without committing', 'Keep my version', "Take the server's version"
function Fill($window, [string] $name, [string] $text) {
    $field = Get-Elements $window | Where-Object { $_.Type -eq 'Edit' -and $_.Name -eq $name } | Select-Object -First 1
    if (-not $field) { throw "No '$name' field" }
    $field.Element.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).SetValue($text)
}

# -Answers: buttons to press, in order, as they appear (a choice, then a confirmation). When the
# window asks and no answer is left, the question is recorded (Asked) and the window cancelled.
function Invoke-Menu([string[]] $paths, [string] $item, [string] $message = $null, [string] $url = '', [string[]] $Answers = @()) {
    Get-Process LoreCompanion -ErrorAction SilentlyContinue | Stop-Process -Force
    $started = Get-Date
    [ShellDriver]::Invoke($paths, $item) | Out-Null
    $window = $null
    for ($i = 0; $i -lt 120 -and -not $window; $i++) {
        $process = Get-Process LoreCompanion -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($process) {
            $window = $ui::RootElement.FindFirst($scope::Children, (New-Object System.Windows.Automation.PropertyCondition($ui::ProcessIdProperty, $process.Id)))
            # Wait for the page inside, not just the frame.
            if ($window -and -not (Get-Elements $window | Where-Object { $_.Type -eq 'Button' })) { $window = $null }
        }
        if (-not $window) { Start-Sleep -Milliseconds 250 }
    }
    if (-not $window) { throw "The Companion window didn't open for '$item'" }
    $title = $window.Current.Name
    if ($url) { Fill $window 'Repository URL' $url; Start-Sleep -Milliseconds 300; Press $window 'Clone' }
    if ($null -ne $message -and $item -eq 'Lore Commit...') { Fill $window 'Commit message (optional)' $message; Start-Sleep -Milliseconds 300; Press $window 'Commit' }
    # Done when the busy button is gone and a closing button is offered: OK after success, Retry
    # after an error, Refresh again for Status and History; or when the window asks a question
    # no answer is left for.
    $pending = [System.Collections.Generic.Queue[string]]::new([string[]]$Answers)
    $asked = ''
    $elements = @()
    for ($i = 0; $i -lt 1200; $i++) {
        Start-Sleep -Milliseconds 250
        $elements = @(Get-Elements $window)
        $buttons = $elements | Where-Object { $_.Type -eq 'Button' }
        if ($buttons | Where-Object { $_.Name -match 'Working|Signing' }) { continue }
        if ($pending.Count -gt 0) {
            $next = Find-Button $elements $pending.Peek()
            if ($next) { $next.Element.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke(); $pending.Dequeue() | Out-Null; Start-Sleep -Milliseconds 300 }
            continue
        }
        $question = $buttons | Where-Object { $name = $_.Name; $questionButtons | Where-Object { $name.StartsWith($_) } }
        if ($question) { $asked = @($question | ForEach-Object Name) -join ' | '; break }
        if ($buttons | Where-Object { $_.Name -in 'OK', 'Retry', 'Refresh' -and $_.Enabled }) { break }
    }
    $seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    $names = @($elements | Where-Object { $_.Type -eq 'Text' -and $_.Name } | ForEach-Object Name)
    $failed = [bool]($elements | Where-Object { $_.Type -eq 'Button' -and $_.Name -eq 'Retry' })
    $revisions = @($elements | Where-Object { $_.Type -eq 'ListItem' -and $_.Name -match '^#\d' }).Count
    $closer = $elements | Where-Object { $_.Type -eq 'Button' -and $_.Name -in 'OK', 'Close', 'Cancel' -and $_.Enabled } | Select-Object -First 1
    if ($closer) { Press $window $closer.Name }
    $process = Get-Process LoreCompanion -ErrorAction SilentlyContinue
    if ($process) { $process | Wait-Process -Timeout 15 -ErrorAction SilentlyContinue }
    $leftOpen = [bool](Get-Process LoreCompanion -ErrorAction SilentlyContinue)
    $text = ($names | Select-Object -Skip 2) -join ' / '
    if ($pending.Count -gt 0) { $failed = $true; $text = "the window never offered '$($pending.Peek())': $text" }
    [pscustomobject]@{ Item = $item; Title = $title; Ok = $(if ($failed) { '' } else { $text }); Error = $(if ($failed) { $text } else { '' }); Asked = $asked; Revisions = $revisions; Seconds = $seconds; LeftOpen = $leftOpen }
}

# --- the checks -----------------------------------------------------------------------------
$priority = @{ Conflicted = 5; StagedModified = 4; Modified = 3; Staged = 2; Locked = 1 }
function Expected-Overlays {
    $expected = [ordered]@{}
    $folders = @{ '' = '' }
    foreach ($path in $local.Keys) {
        $state = if ($states.ContainsKey($path)) { $states[$path] } else { '' }
        $expected[$path] = $state
        $parent = Split-Path $path
        while ($true) {
            $current = if ($folders.ContainsKey($parent)) { $folders[$parent] } else { '' }
            if ($state -and (-not $current -or $priority[$state] -gt $priority[$current])) { $folders[$parent] = $state } elseif (-not $folders.ContainsKey($parent)) { $folders[$parent] = '' }
            if (-not $parent) { break }
            $parent = Split-Path $parent
        }
    }
    foreach ($folder in $folders.Keys | Sort-Object) {
        # The working copy's root shows "clean" when nothing in it has a state.
        $expected["$folder\"] = if ($folders[$folder]) { $folders[$folder] } elseif (-not $folder) { 'Clean' } else { '' }
    }
    $expected
}

function Lore-Status {
    Push-Location $wc
    try { $lines = & $lore --offline status --scan 2>&1 | ForEach-Object { "$_" } } finally { Pop-Location }
    $staged = @(); $changed = @(); $section = ''
    foreach ($line in $lines) {
        if ($line -match '^Changes (to be committed|staged for commit)') { $section = 'staged'; continue }
        if ($line -match '^(Changes not staged|Untracked files)') { $section = 'changed'; continue }
        # Folders are listed too ("art\"); only files are compared.
        if ($line -match '^[AMDR?] (.+?)\s*$' -and $Matches[1] -notmatch '[\\/]$') { if ($section -eq 'staged') { $staged += $Matches[1] -replace '/', '\' } else { $changed += $Matches[1] -replace '/', '\' } }
    }
    [pscustomobject]@{ Staged = $staged; Changed = $changed; Raw = ($lines -join "`n") }
}

function Server-Locks {
    Push-Location $wc
    try { $lines = & $lore lock query 2>&1 | ForEach-Object { "$_" } } finally { Pop-Location }
    $found = foreach ($path in @($local.Keys) + @($remote.Keys) | Select-Object -Unique) { if ($lines -match [regex]::Escape(($path -replace '\\', '/'))) { $path } }
    [pscustomobject]@{ Paths = @($found); Raw = ($lines -join "`n") }
}

function Check([string] $step, [string] $action, $operation) {
    $problems = [System.Collections.Generic.List[string]]::new()
    if ($operation) {
        if ($operation.Error) { $problems.Add("Companion error: $($operation.Error)") }
        if ($operation.LeftOpen) { $problems.Add('the Companion window did not close') }
    }
    # Explorer overlays, before anything else touches the working copy.
    $expected = Expected-Overlays
    $overlays = [ordered]@{}
    foreach ($path in $expected.Keys) {
        $full = if ($path -eq '\') { $wc } else { Join-Path $wc $path.TrimEnd('\') }
        $actual = [ShellDriver]::Overlay($full)
        $overlays[$path] = $actual
        if ($actual -ne $expected[$path]) { $problems.Add("overlay $($path): expected '$($expected[$path])', shows '$actual'") }
    }
    # Disk.
    $onDisk = Get-ChildItem $wc -Recurse -File -Force | Where-Object { $_.FullName -notmatch '\\\.lore(\\|$)' } | ForEach-Object { $_.FullName.Substring($wc.Length + 1) }
    foreach ($path in $local.Keys) {
        $full = Join-Path $wc $path
        if (-not (Test-Path $full)) { $problems.Add("disk: $path is missing"); continue }
        if ([IO.File]::ReadAllText($full) -ne $local[$path]) { $problems.Add("disk: $path has different content") }
    }
    foreach ($path in $onDisk) { if (-not $local.Contains($path)) { $problems.Add("disk: unexpected file $path") } }
    # Lore's own status.
    $status = Lore-Status
    $wantStaged = @($states.Keys | Where-Object { $states[$_] -in 'Staged', 'StagedModified' } | Sort-Object)
    $wantChanged = @($states.Keys | Where-Object { $states[$_] -in 'Modified', 'StagedModified' } | Sort-Object)
    if ((@($status.Staged | Sort-Object) -join ',') -ne ($wantStaged -join ',')) { $problems.Add("lore status staged: [$($status.Staged -join ', ')], expected [$($wantStaged -join ', ')]") }
    if ((@($status.Changed | Sort-Object) -join ',') -ne ($wantChanged -join ',')) { $problems.Add("lore status changed: [$($status.Changed -join ', ')], expected [$($wantChanged -join ', ')]") }
    # Server: files through a second working copy, and the locks. A repository nothing was
    # committed to yet can't be synced, so there is nothing to compare before the first commit.
    if ($remote.Count -gt 0) {
    Push-Location $verify
    try { $syncOutput = & $lore sync 2>&1 | ForEach-Object { "$_" } } finally { Pop-Location }
    if ($LASTEXITCODE) { $problems.Add("verify sync failed: $($syncOutput | Select-Object -Last 2)") }
    $serverFiles = Get-ChildItem $verify -Recurse -File -Force | Where-Object { $_.FullName -notmatch '\\\.lore(\\|$)' } | ForEach-Object { $_.FullName.Substring($verify.Length + 1) }
    foreach ($path in $remote.Keys) {
        $full = Join-Path $verify $path
        if (-not (Test-Path $full)) { $problems.Add("server: $path is missing"); continue }
        if ([IO.File]::ReadAllText($full) -ne $remote[$path]) { $problems.Add("server: $path has different content") }
    }
    foreach ($path in $serverFiles) { if (-not $remote.Contains($path)) { $problems.Add("server: unexpected file $path") } }
    $serverLocks = Server-Locks
    if ((@($serverLocks.Paths | Sort-Object) -join ',') -ne ((@($locks) | Sort-Object) -join ',')) { $problems.Add("server locks: [$($serverLocks.Paths -join ', ')], expected [$((@($locks)) -join ', ')]") }
    }

    $row = [pscustomobject]@{
        Step = $step; Action = $action; Seconds = $operation.Seconds; Passed = $problems.Count -eq 0; Problems = @($problems)
        Overlays = ($overlays.Keys | ForEach-Object { "$_=$(if ($overlays[$_]) { $overlays[$_] } else { '-' })" }) -join ' '
        Lore = "staged [$($status.Staged -join ', ')] changed [$($status.Changed -join ', ')]"
        Server = "files [$(($remote.Keys) -join ', ')] locks [$((@($locks)) -join ', ')]"
        Message = if ($operation) { $operation.Ok } else { '' }
    }
    $results.Add($row)
    "{0,-4} {1,-6} {2,5}s  {3}" -f $step, $(if ($row.Passed) { 'PASS' } else { 'FAIL' }), $operation.Seconds, $action
    foreach ($problem in $problems) { "              - $problem" }
    $results | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $Root 'results.json') -Encoding utf8
}

function Step([string] $step, [string] $action, [scriptblock] $work) {
    try {
        $operation = & $work
        Check $step $action $operation
    } catch {
        $results.Add([pscustomobject]@{ Step = $step; Action = $action; Passed = $false; Problems = @("exception: $($_.Exception.Message)") })
        "{0,-4} FAIL          {1}`n              - exception: {2}" -f $step, $action, $_.Exception.Message
        $results | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $Root 'results.json') -Encoding utf8
    }
}

$url = $RepositoryUrl
# The second working copy that stands for "what's on the server".
& $lore clone $url $verify 2>&1 | Out-Null
if ($LASTEXITCODE) { throw "Couldn't clone $url for verification" }

Step 1 'Clone into an empty folder (Lore Clone...)' { Invoke-Menu @($wc) 'Lore Clone...' -url $url }
Write-File 'a.txt' "alpha 1`n"; Write-File 'b.txt' "bravo 1`n"; Write-File 'art\tex.png' "texture 1`n"
$states['a.txt'] = 'Modified'; $states['b.txt'] = 'Modified'; $states['art\tex.png'] = 'Modified'
Step 2 'Three new files (one in a subfolder), Lore > Status' { Invoke-Menu @($wc) 'Status' }
$states.Clear(); foreach ($p in $local.Keys) { $remote[$p] = $local[$p] }
Step 3 'Commit the folder (Lore Commit... on the working copy)' { Invoke-Menu @($wc) 'Lore Commit...' -message 'Add three files' }
Write-File 'a.txt' "alpha 2`n"; $states['a.txt'] = 'Modified'
Step 4 'Edit a.txt, Lore > Status' { Invoke-Menu @($wc) 'Status' }
$states.Remove('a.txt'); $remote['a.txt'] = $local['a.txt']
Step 5 'Commit only a.txt (Lore Commit... on the file)' { Invoke-Menu @((Join-Path $wc 'a.txt')) 'Lore Commit...' -message 'Edit a' }
$states['b.txt'] = 'Locked'; [void]$locks.Add('b.txt')
Step 6 'Lock b.txt (Lore > Acquire locks)' { Invoke-Menu @((Join-Path $wc 'b.txt')) 'Acquire locks' }
Write-File 'art\tex.png' "texture 2`n"; $states['art\tex.png'] = 'Modified'
Step 7 'Edit art\tex.png (not committed), Lore > Status: one modified, one locked' { Invoke-Menu @($wc) 'Status' }
Write-File 'a.txt' "alpha 3`n"; $remote['a.txt'] = $local['a.txt']
Step 8 'Edit a.txt and commit only it; tex.png stays modified, b.txt stays locked' { Invoke-Menu @((Join-Path $wc 'a.txt')) 'Lore Commit...' -message 'Edit a again' }
$states['art\tex.png'] = 'Staged'
Step 9 'Stage art\tex.png (Lore > Stage selected paths)' { Invoke-Menu @((Join-Path $wc 'art\tex.png')) 'Stage selected paths' }
$states['art\tex.png'] = 'Modified'
Step 9a 'Unstage art\tex.png (Lore > Unstage): the change stays' { Invoke-Menu @((Join-Path $wc 'art\tex.png')) 'Unstage' }
$states['art\tex.png'] = 'Staged'
Step 9b 'Stage art\tex.png again' { Invoke-Menu @((Join-Path $wc 'art\tex.png')) 'Stage selected paths' }
# Lore can't sync with staged files; the Companion asks first. Cancel leaves everything as it was.
Step 10 'Lore Sync while art\tex.png is staged: the window asks; Cancel changes nothing' {
    $operation = Invoke-Menu @($wc) 'Lore Sync'
    if ($operation.Asked -notmatch 'Commit and sync' -or $operation.Asked -notmatch 'Sync without committing') { $operation.Error = "expected the staged-files question, got: '$($operation.Asked)' $($operation.Ok)" }
    $operation
}
$states['art\tex.png'] = 'Modified'
Step 10a 'Lore Sync again, answer "Sync without committing": unstaged, the change stays' { Invoke-Menu @($wc) 'Lore Sync' -Answers 'Sync without committing' }
$states.Remove('art\tex.png'); $remote['art\tex.png'] = $local['art\tex.png']
Step 11 'Commit art\tex.png; b.txt stays locked' { Invoke-Menu @((Join-Path $wc 'art\tex.png')) 'Lore Commit...' -message 'New texture' }
# A local edit that isn't committed, and someone else commits c.txt from another working copy.
Write-File 'a.txt' "alpha 4 (local, not committed)`n"; $states['a.txt'] = 'Modified'
[IO.File]::WriteAllText((Join-Path $verify 'c.txt'), "charlie 1`n")
Push-Location $verify; & $lore stage c.txt 2>&1 | Out-Null; & $lore commit 'c from another copy' 2>&1 | Out-Null; & $lore push 2>&1 | Out-Null; Pop-Location
$remote['c.txt'] = "charlie 1`n"; $local['c.txt'] = "charlie 1`n"
Step 12 'Another copy pushed c.txt; Lore Sync brings it, local edit of a.txt and the lock on b.txt survive' {
    $operation = Invoke-Menu @($wc) 'Lore Sync'
    # Sync doesn't refresh overlays for files it didn't touch; Status is what a person runs next.
    if (-not $operation.Error) { $status = Invoke-Menu @($wc) 'Status'; $operation.Seconds += $status.Seconds; if ($status.Error) { $operation.Error = $status.Error } }
    $operation
}
Step 13 'Lore > History' {
    $operation = Invoke-Menu @($wc) 'History'
    if ($operation.Revisions -lt 5) { $operation.Error = "History shows $($operation.Revisions) revisions, expected at least 5" }
    $operation
}
$local['a.txt'] = $remote['a.txt']; $states.Remove('a.txt')
Step 13a 'Revert a.txt (Lore > Revert changes..., confirm "Discard changes"): back to the committed text' { Invoke-Menu @((Join-Path $wc 'a.txt')) 'Revert changes...' -Answers 'Revert', 'Discard changes' }
if ($RestartCommand) {
    Step 14 'Restart the Lore server with b.txt locked: the lock stays, Lore > Status' {
        & cmd.exe /c $RestartCommand | Out-Null
        Start-Sleep 15
        Invoke-Menu @($wc) 'Status'
    }
}
$states.Remove('b.txt'); [void]$locks.Remove('b.txt')
Step 15 'Release the lock on b.txt (Lore > Release locks)' { Invoke-Menu @((Join-Path $wc 'b.txt')) 'Release locks' }
Remove-Item (Join-Path $wc 'a.txt'); $local.Remove('a.txt'); $remote.Remove('a.txt'); $states.Remove('a.txt')
Step 16 'Delete a.txt, commit the folder' { Invoke-Menu @($wc) 'Lore Commit...' -message 'Remove a' }
"done"
