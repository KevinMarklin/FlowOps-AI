$ErrorActionPreference = 'Stop'
$auditDirectory = $PSScriptRoot
$repositoryDirectory = (Resolve-Path (Join-Path $auditDirectory '..\..')).Path
$replacements = @{}
foreach ($package in @('security', 'handlers', 'channels', 'analyzer')) {
    $virtualPath = Join-Path $repositoryDirectory "internal\$package\audit_proof_test.go"
    $sourcePath = Join-Path $auditDirectory "proofs\${package}_test.go.txt"
    $replacements[$virtualPath] = $sourcePath
}
$overlayPath = Join-Path $auditDirectory 'overlay.json'
@{ Replace = $replacements } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $overlayPath -Encoding utf8NoBOM
Push-Location $repositoryDirectory
try {
    # PASS here means that the documented current weakness was reproduced.
    # These are not assertions of desired secure behavior.
    & go test -overlay $overlayPath -run '^TestAudit' -v ./internal/security ./internal/handlers ./internal/channels ./internal/analyzer
    $testExitCode = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $testExitCode
