[Console]::InputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ErrorActionPreference = 'Stop'
$baseUrl = 'http://localhost:8080'

function Request-Json { param([string]$Method, [string]$Path, [object]$Body)
  $json = $null
  if ($null -ne $Body) { $json = $Body | ConvertTo-Json -Depth 10 -Compress }
  $bytes = $null
  if ($null -ne $json) { $bytes = [Text.Encoding]::UTF8.GetBytes([string]$json) }
  Invoke-RestMethod -Method $Method -Uri ($baseUrl + $Path) -Body $json -ContentType 'application/json; charset=utf-8'
}
function Get-Json { param([string]$Path) Invoke-RestMethod -Method GET -Uri ($baseUrl + $Path) }
function Ok { param([int]$Step, [string]$Text) Write-Host "[OK] Шаг $Step`: $Text" -ForegroundColor Green }
function Assert-Equal { param($Actual, $Expected, [string]$Message) if ($Actual -ne $Expected) { throw "${Message}: ожидалось '$Expected', получено '$Actual'" } }
function Assert-True { param($Value, [string]$Message) if (-not $Value) { throw $Message } }

Write-Host '=== FlowOps AI: полный omnichannel regression ===' -ForegroundColor Cyan
Request-Json DELETE '/api/tickets/reset' $null | Out-Null

$stats = Get-Json '/api/stats'; Assert-True ($stats.ai_model -eq 'gemini-3.8-flash') 'Неверная модель AI'; Ok 1 "конфигурация AI: $($stats.ai_mode), $($stats.ai_model)"
$vpn = Request-Json POST '/api/tickets' @{ user_email='employee@example.ru'; raw_text='Срочно не работает VPN, ошибка 809' }; Assert-Equal $vpn.status 'awaiting_confirmation' 'VPN RAG status'; Assert-True ($vpn.rag_answer -match 'it_vpn') 'Нет VPN RAG'; Ok 2 'web: VPN 809 получил RAG'
$feedback = Request-Json POST "/api/tickets/$($vpn.id)/feedback" @{ resolved=$true }; Assert-Equal $feedback.status 'auto_resolved' 'resolved feedback'; Ok 3 'Помогло: auto_resolved'
$hr = Request-Json POST '/api/tickets' @{ user_email='hr@example.ru'; raw_text='Нужна справка 2-НДФЛ' }; Assert-True ($hr.rag_answer -match 'hr_certificates') 'Нет HR RAG'; $noHelp = Request-Json POST "/api/tickets/$($hr.id)/feedback" @{ resolved=$false }; Assert-Equal $noHelp.status 'assigned_to_operator' 'negative feedback'; Ok 4 'Не помогло: оператор видит очередь и RAG'
$email = Request-Json POST '/api/v1/ingest' @{ channel='email'; sender='client@example.ru'; subject='Лежит сервер NL-1'; message='VPS в кластере NL-1 выдает ошибку 502'; callback_url='' }; Assert-Equal $email.channel 'email' 'email channel'; Assert-Equal $email.department 'IT' 'email route'; Assert-Equal $email.status 'assigned_to_operator' 'email status'; Ok 5 'email: тема и сообщение направлены в IT'
$reply = Request-Json PATCH "/api/tickets/$($email.ticket_id)" @{ operator_reply='Авария NL-1 устранена, трафик переключен.'; status='closed' }; Assert-True $reply.updated 'operator reply'; Ok 6 'операторский ответ сохранен'
$rule = Request-Json POST "/api/knowledge?ticket_id=$($email.ticket_id)" @{ department='Unassigned'; title='Авария NL-1'; keywords=@(); content='Авария NL-1 устранена, трафик переключен.' }; Assert-Equal $rule.department 'General' 'auto keyword department'; Assert-True ($rule.keywords.Count -gt 0) 'keywords не извлечены'; Ok 7 'самообучение RAG и автоизвлечение keywords'
$bot = Request-Json POST '/api/v1/ingest' @{ channel='bot'; sender='@alex_dev'; message='Почему сервер NL-1 выдает 502?'; callback_url='' }; Assert-True $bot.auto_replied 'bot auto reply'; Assert-Equal $bot.status 'auto_resolved' 'bot status'; Ok 8 'bot: автоответ по выученному RAG'
$email2 = Request-Json POST '/api/v1/ingest' @{ channel='email'; sender='other@example.ru'; subject='VPS NL-1'; message='Когда поднимете сервер?'; callback_url='' }; Assert-True $email2.auto_replied 'email repeat auto reply'; Ok 9 'email: повторный автоответ'
$site = Request-Json POST '/api/v1/ingest' @{ channel='web'; sender='site-user'; message='Сайты снова не открываются на NL-1'; callback_url='' }; Assert-True $site.auto_replied 'site repeat auto reply'; Ok 10 'web ingest: повторный автоответ'
$billing = Request-Json POST '/api/v1/ingest' @{ channel='bot'; sender='@maria_fin'; message='Дважды списались деньги за тариф Pro, нужен возврат'; callback_url='' }; Assert-Equal $billing.department 'Billing' 'billing routing'; Ok 11 'bot: деньги маршрутизированы в Billing'
try { Request-Json POST '/api/v1/ingest' @{ channel='bot'; sender=''; message='' } | Out-Null; throw 'empty ingest unexpectedly succeeded' } catch { if ($_.Exception.Response.StatusCode.value__ -ne 400) { throw } }; try { Request-Json POST '/api/tickets/999999/feedback' @{ resolved=$true } | Out-Null; throw 'missing ticket unexpectedly succeeded' } catch { if ($_.Exception.Response.StatusCode.value__ -ne 404) { throw } }; Ok 12 'негативные сценарии: 400 и 404'

$stats = Get-Json '/api/stats'; Write-Host "`n=== Сводная аналитика ===" -ForegroundColor Cyan; $stats | ConvertTo-Json -Depth 10 | Write-Host; Write-Host "`nВсе 12 шагов пройдены." -ForegroundColor Green

Write-Host "`nШаг 13. Транслитерация, завершение инцидента и TTL" -ForegroundColor Yellow
$rule = Request-Json POST '/api/knowledge' @{ department='IT'; title='Авария в кластере NL-1'; keywords=@('NL-1','магистраль'); content='Авария в NL-1, трафик переключен.'; kind='incident'; ttl_seconds=15 }
Assert-True ($rule.filename -match '^it_avariya_v_klastere_nl_1\.md$') 'filename transliteration failed'
Assert-True ($rule.expires_at) 'incident expiry missing'
$resolved = Request-Json PATCH "/api/knowledge/$($rule.filename)" @{ action='resolve_incident'; resolution_note='Восстановили резервный канал.' }
Assert-Equal $resolved.incident_state 'resolved' 'resolve incident action'
Assert-Equal $resolved.filename $rule.filename 'update changed filename'
Assert-True ($resolved.content -match 'Сбой по теме') 'resolved response text missing'
Ok 13 'транслитерация и кнопка «Сбой устранён» проверены'
$ttlRule = Request-Json POST '/api/knowledge' @{ department='IT'; title='Краткий сбой тестового хостера'; keywords=@('TESTHOSTER'); content='Временный тестовый ответ'; kind='incident'; ttl_seconds=3 }
Assert-True ($ttlRule.filename -notmatch '_{3,}') 'broken filename created'
$beforeExpiry = Request-Json POST '/api/v1/ingest' @{ channel='web'; sender='ttl-before'; message='TESTHOSTER снова недоступен'; callback_url='' }
Assert-Equal $beforeExpiry.status 'auto_resolved' 'TTL document was not used before expiry'
Start-Sleep -Seconds 4
$afterExpiry = Get-Json '/api/knowledge'
Assert-True (-not ($afterExpiry | Where-Object filename -eq $ttlRule.filename)) 'expired document remains listed'
$afterExpiryTicket = Request-Json POST '/api/v1/ingest' @{ channel='web'; sender='ttl-after'; message='TESTHOSTER снова недоступен'; callback_url='' }
Assert-Equal $afterExpiryTicket.status 'assigned_to_operator' 'expired TTL document still produced RAG response'
Assert-Equal $afterExpiryTicket.auto_replied $false 'expired TTL document still auto-replied'
$deleted = Request-Json DELETE "/api/knowledge/$($rule.filename)" $null
Assert-Equal $deleted.deleted $true 'manual delete'
Ok 14 'TTL удалил документ, ручное удаление работает'

$stats = Get-Json '/api/stats'; Write-Host "`n=== Финальная сводная аналитика ===" -ForegroundColor Cyan; $stats | ConvertTo-Json -Depth 10 | Write-Host; Write-Host "`nВсе проверки завершены." -ForegroundColor Green
