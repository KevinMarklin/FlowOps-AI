[Console]::InputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ErrorActionPreference = 'Stop'
$baseUrl = 'http://localhost:8080'

function Invoke-JsonRequest {
    param([string]$Method, [string]$Uri, [object]$Body)
    $json = $Body | ConvertTo-Json -Depth 10 -Compress
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    Invoke-RestMethod -Method $Method -Uri ($baseUrl + $Uri) -Body $bytes -ContentType 'application/json; charset=utf-8'
}

function Get-JsonRequest {
    param([string]$Uri)
    Invoke-RestMethod -Method GET -Uri ($baseUrl + $Uri)
}

function Assert-Equal {
    param([object]$Actual, [object]$Expected, [string]$Message)
    if ($Actual -ne $Expected) { throw "$Message. Ожидалось '$Expected', получено '$Actual'." }
}

function Assert-Contains {
    param([string]$Actual, [string]$Expected, [string]$Message)
    if ($Actual -notmatch [regex]::Escape($Expected)) { throw "$Message. Не найдено '$Expected'." }
}

Write-Host '=== FlowOps AI: полный API E2E прогон ===' -ForegroundColor Cyan

Write-Host "`nОчистка демо-данных и пользовательских регламентов" -ForegroundColor DarkGray
Invoke-RestMethod -Method DELETE -Uri ($baseUrl + '/api/tickets/reset') | Out-Null

Write-Host "`nТест 1. Статус и режим ИИ" -ForegroundColor Yellow
$stats = Get-JsonRequest '/api/stats'
Write-Host "ai_mode=$($stats.ai_mode); ai_model=$($stats.ai_model)" -ForegroundColor Green
if ($stats.ai_mode -notin @('llm', 'heuristic_fallback')) { throw 'Некорректный ai_mode.' }

Write-Host "`nТест 2. Типовой IT-тикет + RAG" -ForegroundColor Yellow
$vpn = Invoke-JsonRequest 'POST' '/api/tickets' @{ user_email = 'qa@example.com'; raw_text = 'Срочно! Не работает корпоративный VPN, ошибка 809.' }
Assert-Equal $vpn.department 'IT' 'VPN department'
Assert-Equal $vpn.priority 'high' 'VPN priority'
Assert-Contains ([string]$vpn.rag_answer) 'it_vpn.md' 'VPN RAG source'
Write-Host "VPN ticket #$($vpn.id): IT / high / it_vpn.md" -ForegroundColor Green

Write-Host "`nТест 3. Feedback сотрудника: авторешение" -ForegroundColor Yellow
$feedback = Invoke-JsonRequest 'POST' "/api/tickets/$($vpn.id)/feedback" @{ resolved = $true }
Assert-Equal $feedback.status 'auto_resolved' 'Feedback status'
Write-Host "Ticket #$($vpn.id): auto_resolved" -ForegroundColor Green

Write-Host "`nТест 4. Неизвестная проблема и самообучение RAG" -ForegroundColor Yellow
$unknownText = 'Не открывается тестовый контур мобильного банка на Android, ошибка сертификата SSL_PINNING_ERR'
$unknown = Invoke-JsonRequest 'POST' '/api/tickets' @{ user_email = 'first@example.com'; raw_text = $unknownText }
Assert-Equal $unknown.status 'assigned_to_operator' 'Unknown ticket status'
if ($null -ne $unknown.rag_answer) { throw 'У неизвестного тикета неожиданно найден RAG.' }
$reply = 'Проверьте сертификат мобильного банка и обновите доверенное хранилище Android.'
$updated = Invoke-JsonRequest 'PATCH' "/api/tickets/$($unknown.id)" @{ operator_reply = $reply; status = 'closed' }
Assert-Equal $updated.updated $true 'Operator reply update'
$newRule = Invoke-JsonRequest 'POST' '/api/knowledge' @{ department = 'Unassigned'; title = 'SSL PINNING мобильного банка'; keywords = @('SSL_PINNING_ERR', 'мобильного банка', 'Android'); content = $reply }
Assert-Equal $newRule.department 'General' 'Unassigned normalization'
$repeat = Invoke-JsonRequest 'POST' '/api/tickets' @{ user_email = 'second@example.com'; raw_text = 'Ошибка SSL_PINNING_ERR в мобильном банке' }
Assert-Contains ([string]$repeat.rag_answer) $newRule.filename 'Learned RAG source'
Write-Host "New rule $($newRule.filename): General; repeat ticket #$($repeat.id) получил RAG" -ForegroundColor Green

Write-Host "`nТест 5. Итоговая аналитика" -ForegroundColor Yellow
$finalStats = Get-JsonRequest '/api/stats'
$knowledge = Get-JsonRequest '/api/knowledge'
Write-Host ($finalStats | ConvertTo-Json -Depth 10)
Write-Host "Knowledge documents: $($knowledge.Count)" -ForegroundColor Green
Write-Host "`nВсе API-проверки пройдены." -ForegroundColor Cyan

Write-Host "`nТест 6. Omnichannel ingest: авария, обучение и Billing" -ForegroundColor Yellow
$incident = Invoke-JsonRequest 'POST' '/api/v1/ingest' @{ channel = 'email'; sender = 'client@company.ru'; subject = 'Лежит сервер!'; message = 'Упал VPS-сервер хостера, сайты выдают 502.'; callback_url = '' }
Assert-Equal $incident.channel 'email' 'Ingest channel'
Assert-Equal $incident.department 'IT' 'Incident department'
Assert-Equal $incident.status 'assigned_to_operator' 'Incident status'
$operatorReply = 'Авария хостера подтверждена. Сервер восстановлен, проверьте доступность сайта.'
$updatedIncident = Invoke-JsonRequest 'PATCH' "/api/tickets/$($incident.ticket_id)" @{ operator_reply = $operatorReply; status = 'closed' }
Assert-Equal $updatedIncident.updated $true 'Incident operator reply'
$rule = Invoke-JsonRequest 'POST' "/api/knowledge?ticket_id=$($incident.ticket_id)" @{ department = 'IT'; title = 'Авария хостера NL-1'; keywords = @(); content = $operatorReply }
$repeatIncident = Invoke-JsonRequest 'POST' '/api/v1/ingest' @{ channel = 'bot'; sender = '@alex_dev'; message = 'Упал сервер хостера NL-1, сайты не открываются.' }
Assert-Equal $repeatIncident.auto_replied $true 'Incident auto reply'
$billing = Invoke-JsonRequest 'POST' '/api/v1/ingest' @{ channel = 'bot'; sender = '@maria_fin'; message = 'С карты дважды списалась оплата за тариф Pro, верните деньги на баланс.' }
Assert-Equal $billing.department 'Billing' 'Billing routing'
Write-Host "Ingest: авария обучена через $($rule.filename), бот получил автоответ, деньги направлены в Billing" -ForegroundColor Green
