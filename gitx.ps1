param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$GitArgs
)

$repoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

& git -C $repoRoot @GitArgs
exit $LASTEXITCODE
