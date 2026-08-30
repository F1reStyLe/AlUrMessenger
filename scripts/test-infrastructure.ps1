# Reproducible local test fixture. No existing project containers/volumes are reused.
param()
$ErrorActionPreference = 'Stop'
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$fixture = Join-Path $repo 'test/integration/compose.yaml'
$project = 'alurinfra-' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$secretDir = [IO.Path]::GetFullPath((Join-Path $repo "build/$project"))
if (-not $secretDir.StartsWith((Join-Path $repo 'build') + [IO.Path]::DirectorySeparatorChar)) { throw 'Unsafe test directory' }
$prior = @{}
$createdFiles = @()
$ports = [Collections.Generic.HashSet[int]]::new()

# Save environment so invocation in the current PowerShell does not leave test credentials.
function Set-TestEnv([string]$Name, $Value) {
    if (-not $prior.ContainsKey($Name)) { $prior[$Name] = [Environment]::GetEnvironmentVariable($Name, 'Process') }
    if ($null -eq $Value) { Remove-Item -LiteralPath ("Env:"+$Name) -ErrorAction SilentlyContinue }
    else { [Environment]::SetEnvironmentVariable($Name, [string]$Value, 'Process') }
}

# Ephemeral ports are loopback only. A race with another process fails safely at Docker bind.
function Get-TestPort {
    do {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
        $listener.Start()
        $port = $listener.LocalEndpoint.Port
        $listener.Stop()
    } while (-not $ports.Add($port))
    return $port
}

# Pass CLI arguments as an array; never compose a shell command containing credentials.
function Invoke-Fixture([string[]]$Arguments) {
    & docker compose -f $fixture -p $project @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Fixture command failed: $($Arguments[0])" }
}

Push-Location $repo
try {
    New-Item -ItemType Directory -Path $secretDir | Out-Null
    $secrets = @{}
    foreach ($name in @('postgres-admin','postgres-runtime','postgres-migration','redis','minio-admin','minio-runtime')) {
        $value = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
        $secrets[$name] = $value
        $path = Join-Path $secretDir $name
        [IO.File]::WriteAllText($path,$value)
        $createdFiles += $path
    }
    $redisConfig = Join-Path $secretDir 'redis.conf'
    [IO.File]::WriteAllText($redisConfig,"bind 0.0.0.0`nappendonly yes`nrequirepass $($secrets['redis'])`n")
    $createdFiles += $redisConfig
    # Public key is enough for startup probes; test signing keys never enter runtime.
    $publicKey = Join-Path $secretDir 'jwt-public.pem'
    $rsa = [Security.Cryptography.RSA]::Create(2048)
    try { [IO.File]::WriteAllText($publicKey,$rsa.ExportSubjectPublicKeyInfoPem()) } finally { $rsa.Dispose() }
    $createdFiles += $publicKey
    Set-TestEnv ALUR_TEST_SECRETS $secretDir.Replace('\','/')
    Set-TestEnv ALUR_TEST_PG_PORT (Get-TestPort)
    Set-TestEnv ALUR_TEST_REDIS_PORT (Get-TestPort)
    Set-TestEnv ALUR_TEST_KAFKA_PORT (Get-TestPort)
    Set-TestEnv ALUR_TEST_MINIO_PORT (Get-TestPort)
    Set-TestEnv ALUR_TEST_PROJECT $project
    Set-TestEnv ALUR_INTEGRATION_TEST '1'
    Set-TestEnv APP_ENV 'test'
    # Explicit test credentials cannot be combined with the developer's *_FILE overrides.
    foreach ($name in @('POSTGRES_URL','MIGRATION_POSTGRES_URL','REDIS_URL','MINIO_ACCESS_KEY','MINIO_SECRET_KEY','KAFKA_USERNAME','KAFKA_PASSWORD')) { Set-TestEnv ($name+'_FILE') $null }
    Set-TestEnv POSTGRES_URL "postgres://alur_runtime:$($secrets['postgres-runtime'])@127.0.0.1:$env:ALUR_TEST_PG_PORT/alur?sslmode=disable"
    Set-TestEnv MIGRATION_POSTGRES_URL "postgres://alur_migrator:$($secrets['postgres-migration'])@127.0.0.1:$env:ALUR_TEST_PG_PORT/alur?sslmode=disable"
    Set-TestEnv POSTGRES_MAX_CONNS '4'
    Set-TestEnv REDIS_URL "redis://:$($secrets['redis'])@127.0.0.1:$env:ALUR_TEST_REDIS_PORT/0"
    Set-TestEnv KAFKA_BROKERS "127.0.0.1:$env:ALUR_TEST_KAFKA_PORT"
    Set-TestEnv KAFKA_SECURITY_PROTOCOL 'PLAINTEXT'
    Set-TestEnv MINIO_ENDPOINT "http://127.0.0.1:$env:ALUR_TEST_MINIO_PORT"
    Set-TestEnv MINIO_BUCKET 'chat-attachments'
    Set-TestEnv MINIO_REGION 'us-east-1'
    Set-TestEnv MINIO_ACCESS_KEY 'test-runtime'
    Set-TestEnv MINIO_SECRET_KEY $secrets['minio-runtime']
    Set-TestEnv ALUR_TEST_MINIO_ADMIN $secrets['minio-admin']
    Set-TestEnv INFRA_TIMEOUT '5s'
    Set-TestEnv GOOS (& go env GOHOSTOS)
    Set-TestEnv GOARCH (& go env GOHOSTARCH)
    Set-TestEnv CGO_ENABLED '1'
    Invoke-Fixture @('up','-d')
    & go test -race -tags=integration -count=1 -timeout=8m ./test/integration
    if ($LASTEXITCODE -ne 0) { throw 'Infrastructure tests failed' }
    & go run ./cmd/migrate status
    if ($LASTEXITCODE -ne 0) { throw 'Migration command failed' }
    & go run ./cmd/minio-init
    if ($LASTEXITCODE -ne 0) { throw 'Bucket command failed' }
    # Build real Linux binaries and run as PID 1; the image is only a runtime shell,
    # compilation uses the project's Go toolchain. No production image is implied.
    Set-TestEnv GOOS 'linux'
    Set-TestEnv GOARCH 'amd64'
    Set-TestEnv CGO_ENABLED '0'
    foreach ($role in @('api','worker')) {
        $binary = Join-Path $secretDir "chat-$role"
        & go build -o $binary "./cmd/$role"
        if ($LASTEXITCODE -ne 0) { throw 'Linux build failed' }
        $createdFiles += $binary
    }
    $runtimeEnv = Join-Path $secretDir 'runtime.env'
    $runtimeSettings = @(
        'APP_ENV=test', 'HTTP_ADDR=0.0.0.0:8080', 'INFRA_TIMEOUT=5s', 'AUTH_MODE=dev-rsa', 'AUTH_PUBLIC_KEY_FILE=/jwt-public.pem',
        "POSTGRES_URL=postgres://alur_runtime:$($secrets['postgres-runtime'])@postgres:5432/alur?sslmode=disable",
        "REDIS_URL=redis://:$($secrets['redis'])@redis:6379/0",
        'KAFKA_BROKERS=kafka:29092', 'KAFKA_SECURITY_PROTOCOL=PLAINTEXT',
        'MINIO_ENDPOINT=http://minio:9000', 'MINIO_BUCKET=chat-attachments',
        'MINIO_ACCESS_KEY=test-runtime', "MINIO_SECRET_KEY=$($secrets['minio-runtime'])"
    )
    [IO.File]::WriteAllLines($runtimeEnv,$runtimeSettings)
    $createdFiles += $runtimeEnv
    foreach ($role in @('api','worker')) {
        $container = "$project-$role-smoke"
        try {
            & docker run -d --name $container --label "com.docker.compose.project=$project" --network "${project}_default" --read-only --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 --mount "type=bind,source=$secretDir/chat-$role,target=/chat,readonly" --mount "type=bind,source=$publicKey,target=/jwt-public.pem,readonly" --env-file $runtimeEnv --entrypoint /chat golang:1.25-bookworm
            if ($LASTEXITCODE -ne 0) { throw 'Runtime container failed' }
            & docker exec $container curl --silent --show-error --fail --retry 10 --retry-connrefused --retry-delay 1 --max-time 3 http://127.0.0.1:8080/health/ready
            if ($LASTEXITCODE -ne 0) { throw 'Runtime readiness failed' }
            & docker stop --timeout 25 $container
            if ($LASTEXITCODE -ne 0) { throw 'SIGTERM stop failed' }
            $code = & docker inspect --format '{{.State.ExitCode}}' $container
            if ($code -ne '0') { throw "Runtime shutdown exit=$code" }
            Write-Output "$role SIGTERM exit=0"
        }
        finally {
            $labels = & docker inspect --format '{{json .Config.Labels}}' $container 2>$null
            if ($LASTEXITCODE -eq 0 -and ($labels | ConvertFrom-Json).'com.docker.compose.project' -eq $project) {
                & docker stop --timeout 5 $container
                & docker rm $container
            }
        }
    }
}
finally {
    # Only the unique project created above is removed; no prune or shared-volume cleanup.
    & docker compose -f $fixture -p $project down --volumes --remove-orphans
    if ($LASTEXITCODE -ne 0) { Write-Warning "Fixture cleanup failed: $project" }
    foreach ($path in $createdFiles) {
        if ([IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($path)) -ne $secretDir) { throw 'Unsafe secret cleanup path' }
        Remove-Item -LiteralPath $path
    }
    if (Test-Path -LiteralPath $secretDir) { Remove-Item -LiteralPath $secretDir }
    foreach ($name in $prior.Keys) {
        if ($null -eq $prior[$name]) { Remove-Item -LiteralPath ("Env:"+$name) -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($name,$prior[$name],'Process') }
    }
    Pop-Location
}
