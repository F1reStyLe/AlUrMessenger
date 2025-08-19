#!/usr/bin/env pwsh

$configPath = Resolve-Path "../src/internal/config/local.yaml"
Write-Host "Reading config from: $configPath" -ForegroundColor Yellow

$content = Get-Content $configPath -Raw

# Простой поиск значений по regex
$dbHost = if ($content -match 'host:\s*"([^"]+)"') { $matches[1] } else { "localhost" }
$dbPort = if ($content -match 'dbport:\s*"([^"]+)"') { $matches[1] } else { "5432" }
$dbName = if ($content -match 'name:\s*"([^"]+)"') { $matches[1] } else { "messenger" }
$dbUser = if ($content -match 'user:\s*"([^"]+)"') { $matches[1] } else { "postgres" }
$dbPass = if ($content -match 'password:\s*"([^"]+)"') { $matches[1] } else { "password" }

Write-Host "Parsed values:" -ForegroundColor Green
Write-Host "DB_HOST: $dbHost" -ForegroundColor Gray
Write-Host "DB_PORT: $dbPort" -ForegroundColor Gray
Write-Host "DB_NAME: $dbName" -ForegroundColor Gray
Write-Host "DB_USER: $dbUser" -ForegroundColor Gray
Write-Host "DB_PASSWORD: $dbPass" -ForegroundColor Gray

# Создаем .env файл
$envContent = @"
# Database
DB_HOST=$dbHost
DB_PORT=$dbPort
DB_NAME=$dbName
DB_USER=$dbUser
DB_PASSWORD=$dbPass
"@

$envContent | Out-File -FilePath ".env" -Encoding UTF8
Write-Host "`n✅ .env file created successfully!" -ForegroundColor Green

# Показываем содержимое
Write-Host "`n📄 .env file content:" -ForegroundColor Cyan
Get-Content .\.env