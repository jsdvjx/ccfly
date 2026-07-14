# addpath.ps1 -- append a directory to the *user* PATH (HKCU\Environment).
#
# Reads the raw registry value with DoNotExpandEnvironmentNames so existing
# %VAR% references survive the round-trip, and writes back as ExpandString.
# Doing this in PowerShell instead of NSIS sidesteps NSIS's 1024-char string
# limit, which silently truncates (and thus destroys) long user PATHs.
param([Parameter(Mandatory = $true)][string]$Dir)

$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
try {
    $path = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    $parts = @($path -split ';' | Where-Object { $_ -ne '' })
    if ($parts -notcontains $Dir) {
        $key.SetValue('Path', (($parts + $Dir) -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
        Write-Output "addpath: appended $Dir to user PATH"
    } else {
        Write-Output "addpath: $Dir already on user PATH"
    }
} finally {
    $key.Close()
}
