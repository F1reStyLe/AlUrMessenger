# Regenerates the bounded Unicode Emoji 16.0 vocabulary used by reaction validation.
# Fully-qualified sequences are canonical; only the explicit Unicode aliases are accepted.
$ErrorActionPreference = 'Stop'
$source = 'https://www.unicode.org/Public/emoji/16.0/emoji-test.txt'
$data = (Invoke-WebRequest -Uri $source).Content
$lines = [Collections.Generic.List[string]]::new()
$lines.Add('// Code generated from Unicode Emoji 16.0 emoji-test.txt; DO NOT EDIT.')
$lines.Add('// Source: https://www.unicode.org/Public/emoji/16.0/emoji-test.txt')
$lines.Add('// Unicode data copyright Unicode, Inc.; https://www.unicode.org/license.txt')
$lines.Add('package message')
$lines.Add('')
$lines.Add('var reactionEmoji = map[string]string{')
$canonical = $null
foreach ($line in ($data -split "`n")) {
    if ($line -notmatch '^([0-9A-F ]+)\s*;\s*(fully-qualified|minimally-qualified|unqualified)\s*#') { continue }
    $points = $Matches[1].Trim() -split '\s+'
    $status = $Matches[2]
    $escaped = ($points | ForEach-Object { '\U' + ([Convert]::ToInt32($_,16)).ToString('x8') }) -join ''
    if ($status -eq 'fully-qualified') { $canonical = $escaped }
    if (-not $canonical) { throw 'Alias before canonical emoji' }
    $lines.Add('"' + $escaped + '": "' + $canonical + '",')
}
if ($lines.Count -lt 4000) { throw 'Incomplete emoji data' }
$lines.Add('}')
$target = Join-Path $PSScriptRoot '../internal/message/emoji_generated.go'
[IO.File]::WriteAllLines($target,$lines,[Text.UTF8Encoding]::new($false))
& gofmt -w $target
if ($LASTEXITCODE -ne 0) { throw 'Emoji formatting failed' }
