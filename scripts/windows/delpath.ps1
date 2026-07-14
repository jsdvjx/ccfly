# delpath.ps1 -- remove a directory from the *user* PATH (HKCU\Environment).
# Counterpart of addpath.ps1; same DoNotExpand/ExpandString round-trip.
param([Parameter(Mandatory = $true)][string]$Dir)

$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if ($null -eq $key) { exit 0 }
try {
    $path = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    $parts = @($path -split ';' | Where-Object { $_ -ne '' -and $_ -ne $Dir })
    $key.SetValue('Path', ($parts -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
    Write-Output "delpath: removed $Dir from user PATH"
} finally {
    $key.Close()
}
