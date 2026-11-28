(() => {
  'use strict';

  const state = {
    pendingTicket: null,
    tickets: [],
    historyBefore: 0, operatorBefore: 0,
    currentTicketId: null,
    employeePoll: null,
    operatorLoading: false,
    queueEpoch: 0,
    replyDrafts: new Map(),
    ticketRowsOpen: new Set(),
    historyTickets: [],
    user: null
  };

  const $ = (selector, root = document) => (root && root.querySelector ? root.querySelector(selector) : null);
  const $$ = (selector, root = document) => (root && root.querySelectorAll ? [...root.querySelectorAll(selector)] : []);
  const on = (element, event, handler) => {
    if (element) element.addEventListener(event, handler);
  };
  const initialAnalysis = $('#analysis-result')?.cloneNode(true);
  let workState = { id: null, seconds: 0, running: false, busy: false, activity: 0 };

  function renderWork() {
    $$('[data-work-time]').forEach(label => {
      const current = String(label.dataset.workTime) === String(workState.id);
      label.textContent = current ? `${workState.running ? 'В работе' : 'Пауза'} · ${Math.floor(workState.seconds / 60)} мин ${Math.floor(workState.seconds % 60)} с` : 'Время пока не учитывается';
    });
    $$('[data-action="work-pause"]').forEach(button => { button.hidden = !workState.running || String(button.closest('[data-id]')?.dataset.id) !== String(workState.id); });
  }

  async function trackWork(id, action) {
    if (!hasSession() || !['admin', 'operator'].includes(state.user?.role) || workState.busy) return false;
    const generation = sessionGeneration;
    workState.busy = true;
    try {
      const result = await api(`/api/tickets/${id}/work`, { method: 'POST', body: JSON.stringify({ action }) });
      if (generation !== sessionGeneration) return false;
      workState = { id: String(id), seconds: result.active_seconds, running: result.running, busy: false, activity: action === 'start' ? Date.now() : workState.activity };
      const ticket = state.tickets?.find(item => String(item.id) === String(id));
      if (ticket && result.status) ticket.status = result.status;
      renderWork(); return true;
    } catch (error) {
      if (generation === sessionGeneration) { workState.running = false; renderWork(); if (action !== 'heartbeat') toast(error.message); }
      return false;
    } finally { if (generation === sessionGeneration) workState.busy = false; }
  }

  let settingsEpoch = 0;
  let settingsRefresh = null;
  const roleNames = { admin: 'Администратор', operator: 'Оператор', analyst: 'Аналитик', employee: 'Сотрудник' };
  const roleTabs = {
    admin: ['admin', 'structure', 'operator', 'analytics', 'optimization', 'knowledge', 'knowledge-review', 'builder', 'channels', 'platform', 'employee'],
    operator: ['operator', 'knowledge', 'knowledge-review', 'employee'],
    analyst: ['analytics','optimization'],
    employee: ['employee']
  };
  const canTab = name => (roleTabs[state.user?.role] || []).includes(name);

  function clearWorkspaceData() {
	window.FlowOpsSupportUI?.clear();
    window.FlowOpsPlatform?.clear();
    window.FlowOpsAnalytics?.clear();
    window.FlowOpsOptimization?.clear();
    window.FlowOpsKnowledge?.clear();
    window.FlowOpsStructure?.clear();
    $('#user-settings')?.close();
    clearPersonalCodes();
    if ($('#settings-nav')) $('#settings-nav').open = false;
    workState = { id: null, seconds: 0, running: false, busy: false, activity: 0 };
    state.pendingTicket = null;
    state.tickets = []; state.currentTicketId = null; state.replyDrafts.clear(); state.ticketRowsOpen.clear();
    state.historyTickets = []; state.historyBefore = 0; state.operatorBefore = 0;
    ['#history-search', '#queue-search'].forEach(selector => { if ($(selector)) $(selector).value = ''; });
    ['#priority-filter', '#department-filter', '#status-filter'].forEach(selector => { if ($(selector)) $(selector).value = ''; });
    ['#ticket-error', '#ticket-progress'].forEach(selector => { if ($(selector)) $(selector).hidden = true; });
    $('#ticket-form')?.removeAttribute('aria-busy');
    $$('#ticket-form button, #intake-fields input').forEach(field => { field.disabled = false; });
    ['#ticket-list', '#history-list', '#metrics', '#audit-list', '#members-list', '#knowledge-list', '#smart-import-results'].forEach(selector => $(selector)?.replaceChildren());
    ['#workspace-result', '#member-issued', '#issued-key-notice'].forEach(selector => { const element = $(selector); if (element) { element.replaceChildren(); element.hidden = true; } });
    ['#ticket-form', '#knowledge-form', '#member-form', '#workspace-form'].forEach(selector => $(selector)?.reset());
    $$('#builder input, #builder textarea').forEach(field => { field.value = ''; });
    const snippet = $('#widget-snippet-box'); if (snippet) snippet.hidden = true;
    const curl = $('#crm-curl'); if (curl) curl.textContent = '';
    ++ticketRouteVersion; leaveTicketContent(); $('#closure-reasons')?.remove();
    const toastBox = $('#toast'); if (toastBox) { toastBox.textContent = ''; toastBox.classList.remove('show'); }
    if (initialAnalysis) $('#analysis-result')?.replaceWith(initialAnalysis.cloneNode(true));
  }

  function selectSettingsPanel(panel = 'connections') {
    $$('#builder [data-settings-section]').forEach(section => { section.hidden = section.dataset.settingsSection !== panel; });
    const button = $(`#settings-nav [data-settings-panel="${panel}"]`);
    const title = $('#builder .page-heading h1');
    if (title && button) title.textContent = button.textContent;
  }

  function applyRoleUI(selectDefault = true) {
    const role = state.user?.role;
    document.body.dataset.role = role || '';
    $$('.tab').forEach(tab => { tab.hidden = !canTab(tab.dataset.tab); });
    $$('[data-admin-only]').forEach(element => { element.hidden = role !== 'admin'; });
    const label = $('#role-label');
    if (label) label.textContent = roleNames[role] || '';
    const email = $('#email');
    if (email) {
      email.readOnly = role === 'employee';
      if (role === 'employee') email.value = state.user.email;
    }
    const hint = $('#identity-hint');
    if (hint) hint.hidden = role !== 'employee';
    if (!canTab('operator')) { $('#ticket-list').replaceChildren(); state.replyDrafts.clear(); }
    if (!canTab('analytics')) $('#metrics').replaceChildren();
    if (!canTab('knowledge')) $('#knowledge-list').replaceChildren();
    if (!canTab('admin')) { $('#members-list').replaceChildren(); $('#audit-list').replaceChildren(); $('#member-issued').replaceChildren(); }
    const settingsNav = $('#settings-nav');
    if (settingsNav) { settingsNav.hidden = !['knowledge','builder','channels','platform'].some(canTab); if (settingsNav.hidden) settingsNav.open = false; }
    const current = $('.tab.active');
    if (selectDefault || !current || !canTab(current.dataset.tab)) {
      const target = roleTabs[role]?.[0];
      $$('.tab, .view').forEach(element => element.classList.remove('active'));
      const tab = $(`.tab[data-tab="${target}"]`);
      tab?.classList.add('active');
      $(`#${target}`)?.classList.add('active');
      $$('.tab').forEach(item => item.setAttribute('aria-current', item === tab ? 'page' : 'false'));
      if (!selectDefault) tab?.click();
    }
  }

  let analyticsLoading = false;
  async function loadAnalytics() {
    if(window.FlowOpsAnalytics)return window.FlowOpsAnalytics.refresh();
    if (!hasSession() || !canTab('analytics') || analyticsLoading) return;
    analyticsLoading = true;
    const status = $('#metrics-status');
    try {
      const stats = await api('/api/stats', { cache: 'no-store' });
      renderMetrics(stats);
      let reasons = $('#closure-reasons');
      if (!reasons) { reasons=document.createElement('section');reasons.id='closure-reasons';reasons.className='panel builder-block';$('#metrics').after(reasons); }
      reasons.innerHTML = '<h2>Причины закрытия тикетов</h2>' + (Object.entries(stats.closure_reasons || {}).length ? '<dl class="closure-reason-counts">'+Object.entries(stats.closure_reasons).map(([reason,count])=>`<div><dt>${escapeHTML(closureReasonLabels[reason]||reason)}</dt><dd>${Number(count)||0}</dd></div>`).join('')+'</dl>' : '<p class="muted">Закрытых тикетов пока нет.</p>');
      status.textContent = `Обновлено ${new Date(stats.observed_at).toLocaleTimeString('ru-RU')} · каждые 5 с`;
      status.classList.remove('stale');
    } catch (error) {
      status.textContent = `Нет обновления: ${error.message}`;
      status.classList.add('stale');
    } finally { analyticsLoading = false; }
  }

  async function loadMembers() {
    if (!hasSession() || !canTab('admin')) return;
    const list = $('#members-list');
    try {
      const members = await api('/api/v1/members');
      list.replaceChildren();
      if (!members.length) { const row = list.insertRow(); const cell = row.insertCell(); cell.colSpan = 4; cell.textContent = 'В команде пока только владелец. Пригласите первого участника.'; }
      members.forEach(member => {
        const row = list.insertRow();
        const identity = row.insertCell();
        const name = document.createElement('strong'); name.textContent = member.name;
        const email = document.createElement('small'); email.textContent = member.email;
        identity.append(name, email);
        const roleCell = row.insertCell(); const select = document.createElement('select'); select.setAttribute('aria-label', `Роль: ${member.name}`);
        Object.entries(roleNames).forEach(([value, text]) => { const option = document.createElement('option'); option.value = value; option.textContent = text; select.append(option); });
        select.value = member.role; select.disabled = member.id === state.user?.id; roleCell.append(select);
        row.insertCell().textContent = !member.active ? 'Отключён' : member.password_ready ? 'Активирован' : 'Ожидает первого входа';
        const actions = row.insertCell(); const button = document.createElement('button'); button.type = 'button'; button.className = member.active ? 'danger-button' : 'secondary-button'; button.textContent = member.active ? 'Отключить' : 'Включить'; button.disabled = member.id === state.user?.id; actions.append(button);
        const rotate = document.createElement('button'); rotate.type = 'button'; rotate.className = 'secondary-button'; rotate.textContent = member.password_ready ? 'Сбросить пароль' : 'Новый код'; rotate.disabled = !member.active || member.id === state.user?.id; actions.append(rotate);
        on(rotate, 'click', async () => {
          rotate.disabled = true;
          try {
            const issued = await api(`/api/v1/members/${encodeURIComponent(member.id)}/rotate`, { method: 'POST' });
            showMemberCode(issued); await loadAudit(); toast('Код активации выдан. Старый пароль и сеансы отозваны.');
          } catch (error) { toast(error.message); } finally { rotate.disabled = !member.active || member.id === state.user?.id; }
        });
        const update = async changes => {
          select.disabled = true; button.disabled = true;
          try { await api(`/api/v1/members/${encodeURIComponent(member.id)}`, { method: 'PATCH', body: JSON.stringify(changes) }); await loadMembers(); await loadAudit(); toast('Доступ обновлён'); }
          catch (error) { toast(error.message); select.value = member.role; select.disabled = member.id === state.user?.id; button.disabled = member.id === state.user?.id; }
        };
        on(select, 'change', () => update({ role: select.value }));
        on(button, 'click', () => update({ active: !member.active }));
      });
    } catch (error) { list.replaceChildren(); const cell = list.insertRow().insertCell(); cell.colSpan = 4; cell.textContent = error.message; }
  }

  async function createMember(event) {
    event.preventDefault();
    if (!canTab('admin')) return;
    const form = event.currentTarget;
    const button = $('button[type=submit]', form);
    if (button.disabled) return;
    button.disabled = true;
    const issued = $('#member-issued'); issued.hidden = true; issued.replaceChildren();
    try {
      const member = await api('/api/v1/members', { method: 'POST', body: JSON.stringify({ name: $('#member-name').value.trim(), email: $('#member-email').value.trim(), role: $('#member-role').value }) });
      showMemberCode(member);
      form.reset(); await loadMembers(); await loadAudit();
    } catch (error) { toast(error.message); } finally { button.disabled = false; }
  }

  function showMemberCode(member) {
    const issued = $('#member-issued'); issued.replaceChildren();
    const message = document.createElement('p'); message.textContent = `Доступ для ${member.name} (${roleNames[member.role]}). Сохраните и передайте код лично участнику:`;
    const token = document.createElement('code'); token.className = 'issued-workspace-token'; token.textContent = member.token;
    const copy = document.createElement('button'); copy.type = 'button'; copy.textContent = 'Скопировать код';
    on(copy, 'click', async () => { try { await navigator.clipboard.writeText(member.token); toast('Код скопирован'); } catch { toast('Выделите код и скопируйте вручную'); } });
    issued.append(message, token, copy); issued.hidden = false;
  }

  // Migrate the old browser-held code into first-time activation, then erase it.
  let legacyActivationCode = '';
  try {
    legacyActivationCode = sessionStorage.getItem('flowops_operator_token') || localStorage.getItem('flowops_operator_token') || '';
    localStorage.removeItem('flowops_operator_token'); sessionStorage.removeItem('flowops_operator_token');
  } catch {}
  let sessionGeneration = 0;
  let authReady = false;
  let signingOut = false;
  // This is a public member identifier, not a token. It binds the tab to the
  // identity it opened with without placing session credentials in JS storage.
  let tabMemberID = '';
  try { tabMemberID = sessionStorage.getItem('flowops_tab_member') || ''; } catch {}
  function bindTabMember(id) {
    tabMemberID = id || '';
    try {
      if (tabMemberID) sessionStorage.setItem('flowops_tab_member', tabMemberID);
      else sessionStorage.removeItem('flowops_tab_member');
    } catch {}
  }
  function sessionChanged() {
    clearLocalSession();
    showAuth('В другой вкладке выполнен вход в другой аккаунт. Войдите заново. Для одновременной работы с разными аккаунтами используйте отдельные профили браузера.');
  }
  function csrfToken() {
    const cookie = document.cookie.split(';').map(value => value.trim()).find(value => value.startsWith('flowops_csrf='));
    return cookie ? decodeURIComponent(cookie.slice('flowops_csrf='.length)) : '';
  }
  function showAuth(error = '') {
    authReady = false;
    const gate = $('#auth-gate');
    const message = $('#auth-error');
    if (gate) gate.hidden = false;
    document.body.classList.add('auth-required');
    if (message) {
      message.textContent = error;
      message.hidden = !error;
    }
    if (error) $('#auth-notice').hidden = true;
    $('#login-email')?.focus();
  }

  function hideAuth(me) {
    authReady = true;
    state.user = me?.user || null;
    applyOrganization(me);
    bindTabMember(state.user?.id);
    const workspaceID = $('#workspace-id-display'); if (workspaceID) workspaceID.textContent = me?.tenant_id || '';
    applyRoleUI();
    window.FlowOpsPlatform?.refreshPublic();
    const gate = $('#auth-gate');
    const label = $('#workspace-name-label');
    const userLabel = $('#user-name-label');
    if (userLabel) { userLabel.textContent = me?.user?.display_name || ''; userLabel.title = me?.user?.email || ''; }
    if (gate) gate.hidden = true;
    $('#auth-notice').hidden = true; $('#auth-notice').textContent = '';
    const issued = $('#workspace-result');
    if (issued) { issued.replaceChildren(); issued.hidden = true; }
    document.body.classList.remove('auth-required');
    if (label) label.textContent = me?.name || '';
  }

  function clearLocalSession() {
    sessionGeneration++;
    authReady = false; resetIntake(); clearWorkspaceData(); state.user = null; renderHistory([]);
    $('#activation-form')?.reset(); $('#auth-form')?.reset();
    $('#activation-details').open = false;
    $('#registration-details').open = false;
    $('#activation-summary').textContent = 'Восстановить пароль';
    $('#auth-notice').hidden = true; $('#auth-notice').textContent = '';
    resetCompanyChoice();
    showAuth();
  }

  async function logout() {
    if (signingOut) return;
    signingOut = true; authReady = false; sessionGeneration++;
    try {
      const response = await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken(), ...(tabMemberID ? { 'X-FlowOps-Member': tabMemberID } : {}) }, body: '{}' });
      if (response.status === 409) { sessionChanged(); return; }
      if (!response.ok) throw new Error('Не удалось завершить сеанс. Повторите выход.');
      bindTabMember('');
      clearLocalSession();
    } catch (error) { authReady = true; toast(error.message); }
    finally { signingOut = false; }
  }

  async function restoreSession(expectedUser = null) {
    try {
      const expectedID = expectedUser?.id || tabMemberID;
      const response = await fetch('/api/v1/auth/me', { credentials: 'same-origin', cache: 'no-store', headers: expectedID ? { 'X-FlowOps-Member': expectedID } : {} });
      if (response.status === 409) { sessionChanged(); return false; }
      if (!response.ok) {
        clearLocalSession();
        if (legacyActivationCode) {
          $('#operator-token').value = legacyActivationCode; legacyActivationCode = '';
          $('#activation-details').open = true;
          showAuth('Для прежнего доступа задайте рабочую почту и пароль в форме активации.');
          $('#activation-email').focus();
        }
        return false;
      }
      const me = await response.json();
      if (expectedID && me.user?.id !== expectedID) { sessionChanged(); return false; }
      legacyActivationCode = '';
      sessionGeneration++;
      hideAuth(me);
      setAPIStatus(true); renderChannels(['Web']); $('#api-status').textContent = 'Сервис доступен';
      $('#login-password').value = ''; $('#activation-password').value = ''; $('#activation-confirm').value = ''; $('#operator-token').value = '';
      if (location.pathname.startsWith('/tickets/')) renderTicketRoute();
      else document.querySelector('.tab.active')?.click();
      return true;
    } catch { showAuth('Не удалось подключиться к сервису. Проверьте соединение.'); return false; }
  }

  async function submitLogin(event) {
    event.preventDefault();
    const form = event.currentTarget; const button = $('button[type=submit]', form);
    if (button.disabled) return;
    button.disabled = true; $('#auth-error').hidden = true;
    $('#auth-notice').hidden = true;
    const credentials = { email: $('#login-email').value.trim(), password: $('#login-password').value, workspace: $('#login-company').hidden ? $('#login-workspace-id').value.trim() : $('#login-company').value };
    try {
      const response = await fetch('/api/v1/auth/login', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(credentials) });
      const result = await response.json();
      if (response.status === 409 && result.activation_required) {
        $('#operator-token').value = credentials.password;
        $('#activation-email').value = credentials.email;
        $('#activation-password').value = ''; $('#activation-confirm').value = '';
        $('#login-password').value = '';
        $('#registration-details').open = false;
        $('#activation-summary').textContent = 'Задать пароль';
        $('#activation-details').open = true;
        $('#auth-notice').textContent = result.message || 'Код принят. Задайте пароль для входа.';
        $('#auth-notice').hidden = false;
        $('#activation-password').focus();
        return;
      }
      if (response.status === 409 && result.workspaces) {
        const select = $('#login-company'); select.replaceChildren();
        const placeholder = document.createElement('option'); placeholder.value = ''; placeholder.textContent = 'Выберите компанию'; placeholder.disabled = true; placeholder.selected = true; select.append(placeholder);
        result.workspaces.forEach(company => { const option = document.createElement('option'); option.value = company.id; option.textContent = company.name; select.append(option); });
        select.hidden = false; select.required = true; $('#login-company-label').hidden = false; select.focus();
        return;
      }
      if (response.status === 401 || result.workspace_required) $('#login-workspace-manual').hidden = false;
      if (!response.ok) throw new Error(result.error || 'Не удалось войти. Повторите попытку.');
      await restoreSession(result.user);
    } catch (error) { showAuth(error.message); }
    finally { button.disabled = false; }
  }

  function resetCompanyChoice() {
    const select = $('#login-company'); select.hidden = true; select.required = false; select.replaceChildren();
    $('#login-company-label').hidden = true;
    $('#login-workspace-id').value = '';
    $('#login-workspace-manual').hidden = true;
  }

  async function submitActivation(event) {
    event.preventDefault();
    const form = event.currentTarget; const button = $('button[type=submit]', form);
    if (button.disabled) return;
    if ($('#activation-password').value !== $('#activation-confirm').value) { showAuth('Пароли не совпадают. Проверьте повтор пароля.'); return; }
    button.disabled = true; $('#auth-error').hidden = true;
    try {
      const response = await fetch('/api/v1/auth/activate', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: $('#operator-token').value.trim(), email: $('#activation-email').value.trim(), password: $('#activation-password').value }) });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || 'Не удалось активировать доступ.');
      $('#login-email').value = $('#activation-email').value.trim();
      await restoreSession(result.user);
    } catch (error) { showAuth(error.message); }
    finally { button.disabled = false; }
  }

  async function createWorkspace(event) {
    event.preventDefault();
    const name = $('#workspace-name')?.value.trim() || '';
    const result = $('#workspace-result');
    if (!name || !result) return;
    const button = $('#workspace-submit');
    if (button?.disabled) return;
    if (button) button.disabled = true;
    result.hidden = false;
    result.textContent = 'Создаём рабочее пространство…';
    try {
      const response = await fetch('/api/v1/workspaces', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, organization_type: $('#workspace-type').value })
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error(data.error || 'Не удалось создать пространство. Повторите попытку.');
      }
      const token = data.token || data.key || data.operator_token;
      if (!token) {
        throw new Error('Пространство создано, но сервер не вернул токен.');
      }
      const tokenField = $('#operator-token');
      if (tokenField) tokenField.value = token;
      $('#activation-details').open = true;
      $('#activation-summary').textContent = 'Задать пароль владельца';
      $('#activation-email').focus();
      result.innerHTML = `<p>Организация создана. Добавлены отделы и стартовые памятки: ${Number(data.starter_documents) || 0}. Адаптируйте их в базе знаний.</p><p>Сохраните код владельца, затем задайте почту и пароль в форме активации. После активации код перестанет действовать.</p><code>${escapeHTML(token)}</code><button type="button" data-copy-workspace-token>Скопировать</button>`;
      on($('[data-copy-workspace-token]', result), 'click', async () => {
        try {
          await navigator.clipboard.writeText(token);
          toast('Токен скопирован');
        } catch {
          const range = document.createRange();
          range.selectNodeContents($('code', result));
          const selection = window.getSelection();
          selection.removeAllRanges();
          selection.addRange(range);
          document.execCommand('copy');
          toast('Токен скопирован');
        }
      });
      const error = $('#auth-error');
      if (error) error.hidden = true;
    } catch (error) {
      result.textContent = error.message;
    } finally {
      if (button) button.disabled = false;
    }
  }

  function hasSession() {
    return authReady;
  }

  function applyOrganization(me) {
    const organization = me?.organization;
    const heading = $('#service-heading');
    if (heading) heading.textContent = organization?.heading || 'Помощь в работе';
    const industry = $('#service-industry');
    if (industry) industry.textContent = organization?.name || 'Выберите близкую ситуацию';
    const examples = organization?.examples || ['Не получается войти в рабочий сервис.', 'Не работает оборудование на рабочем месте.', 'Как запросить кадровую справку?'];
    const titles = organization?.example_titles || {
      education: ['Оборудование в аудитории', 'Учебные сервисы', 'Справка об обучении'],
      bakery: ['Оборудование пекарни', 'Касса и продажи', 'Поставки сырья'],
      bank: ['Внутренние системы', 'Безопасность', 'Кадровые документы'],
      other: ['Доступ к сервису', 'Оборудование', 'Документы и справки']
    }[organization?.type] || examples.map(text=>String(text).split('.')[0].slice(0,45));
    const exampleSection=$('#employee .service-examples');if(exampleSection)exampleSection.hidden=examples.length===0;
    let notice=$('#organization-template-notice');if(!notice){notice=document.createElement('p');notice.id='organization-template-notice';notice.className='field-help';$('#employee .page-heading').after(notice);}notice.textContent=organization?.notice||'';notice.hidden=!notice.textContent;
    $$('.service-examples [data-demo]').forEach((button, index) => {
      button.hidden=!examples.length;if(!examples.length)return;
      button.dataset.demo = examples[index] || examples[0];
      $('b', button).textContent = titles[index];
      $('span', button).textContent = button.dataset.demo;
    });
    applyDepartments(me?.departments || []);
  }

  function applyDepartments(items) {
    state.departmentCodes = [...new Set([...items.map(item => item.name), 'General'])];
    items.forEach(item => { departments[item.name] = item.description || item.name; });
    ['#knowledge-department', '#smart-import-department', '#department-filter'].forEach(selector => {
      const select = $(selector); if (!select) return;
      const previous = select.value;
      select.replaceChildren();
      const codes = selector === '#department-filter' ? ['', ...state.departmentCodes, 'Unassigned'] : state.departmentCodes;
      codes.forEach(code => {
        const option = document.createElement('option'); option.value = code;
        option.textContent = code ? departments[code] || code : 'Все отделы'; select.append(option);
      });
      if (codes.includes(previous)) select.value = previous;
    });
  }

  function clearPersonalCodes() {
    settingsEpoch++;
    if (settingsRefresh) clearInterval(settingsRefresh);
    settingsRefresh = null;
    $$('[data-personal-code]').forEach(box => { box.hidden = true; $('input', box).value = ''; const link = $('[data-open-bot]', box); if (link) { link.hidden = true; link.removeAttribute('href'); } });
  }

  function setSiteTheme(theme) {
    const selected = theme === 'dark' ? 'dark' : 'light';
    document.documentElement.dataset.theme = selected;
    try { localStorage.setItem('flowops_theme', selected); } catch {}
    $$('[name="site-theme"]').forEach(input => { input.checked = input.value === selected; });
  }

  async function loadPersonalLinks() {
    const dialog = $('#user-settings');
    const epoch = settingsEpoch;
    if (!dialog?.open || !hasSession()) return;
    try {
      const status = await api('/api/v1/profile/bots');
      if (!dialog.open || epoch !== settingsEpoch) return;
      $('#settings-access-hint').hidden = status.can_connect;
      $$('[data-personal-channel]').forEach(row => {
        const connection = status.channels[row.dataset.personalChannel] || {};
        $('[data-link-status]', row).textContent = connection.connected ? 'Аккаунт подключён' : connection.linked ? 'Нужно подключить заново: доступ или пароль изменились' : 'Не подключён';
        $('[data-issue-link]', row).disabled = row.dataset.busy === 'true' || !status.can_connect || !!connection.connected;
        $('[data-issue-link]', row).textContent = connection.linked && !connection.connected ? 'Подключить заново' : 'Подключить';
        $('[data-unlink]', row).hidden = !connection.linked;
        if (connection.connected) { const box = $('[data-personal-code]', row); box.hidden = true; $('input', box).value = ''; const link = $('[data-open-bot]', box); if (link) { link.hidden = true; link.removeAttribute('href'); } }
      });
      $('#user-settings-error').hidden = true;
    } catch (error) { if (epoch !== settingsEpoch || !$('#user-settings').open) return; const box = $('#user-settings-error'); box.textContent = error.message; box.hidden = false; }
  }

  function openUserSettings() {
    clearPersonalCodes();
    $('#settings-identity').textContent = `${state.user?.display_name || ''} · ${state.user?.email || ''}`;
    setSiteTheme(document.documentElement.dataset.theme);
    $('#user-settings-error').hidden = true;
    $('#user-settings').showModal();
    loadPersonalLinks();
    settingsRefresh = setInterval(loadPersonalLinks, 5000);
  }

  async function changePersonalLink(row, disconnect) {
    if (row.dataset.busy === 'true') return;
    row.dataset.busy = 'true';
    const epoch = settingsEpoch;
    const buttons = $$('button', row); buttons.forEach(button => { button.disabled = true; });
    try {
      const data = await api(`/api/v1/profile/bots/${row.dataset.personalChannel}`, { method: disconnect ? 'DELETE' : 'POST', body: '{}' });
      if (epoch !== settingsEpoch || !$('#user-settings').open) return;
      const box = $('[data-personal-code]', row);
      if (disconnect) { box.hidden = true; $('input', box).value = ''; const link = $('[data-open-bot]', box); if (link) { link.hidden = true; link.removeAttribute('href'); } }
      else {
        $('input', box).value = data.command;
        const link = $('[data-open-bot]', box);
        if (link) {
          link.hidden = true; link.removeAttribute('href');
          try { const url = new URL(data.open_url); if (url.protocol === 'https:' && url.hostname === 't.me' && !url.username && !url.password) { link.href = url.href; link.hidden = false; } } catch {}
        }
        $('[data-code-expiry]', box).textContent = `Действует до ${new Date(data.expires_at).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })}. Новый код отменит предыдущий.`;
        box.hidden = false;
      }
      await loadPersonalLinks();
    } catch (error) { if (epoch !== settingsEpoch || !$('#user-settings').open) return; const box = $('#user-settings-error'); box.textContent = error.message; box.hidden = false; }
    finally { delete row.dataset.busy; buttons.forEach(button => { button.disabled = false; }); if (epoch === settingsEpoch) loadPersonalLinks(); }
  }

  async function api(path, options = {}) {
    if (!hasSession()) throw new Error('Сначала войдите в рабочее пространство.');
    const requestGeneration = sessionGeneration;
    const response = await fetch(path, {
      ...options,
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        ...(tabMemberID ? { 'X-FlowOps-Member': tabMemberID } : {}),
        ...(options.headers || {}),
        ...(!['GET', 'HEAD', 'OPTIONS'].includes(options.method || 'GET') ? { 'X-CSRF-Token': csrfToken() } : {})
      }
    });
    if (requestGeneration !== sessionGeneration) throw new Error('Сессия изменилась. Повторите действие.');
    if (response.status === 401) clearLocalSession();
    if (response.status === 403 && path !== '/api/v1/auth/me') await checkAPI();
    if(response.ok && options.responseType === 'blob') return response.blob();
    const data = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (response.status === 409 && data?.code === 'session_changed') {
      sessionChanged();
      throw new Error('Аккаунт изменился в другой вкладке. Войдите заново.');
    }
    if (!response.ok) throw new Error(data?.error || `Ошибка API: ${response.status}`);
    return data;
  }

  function escapeHTML(value = '') {
    const element = document.createElement('div');
    element.textContent = value;
    return element.innerHTML;
  }

  const departments = {
    Unassigned: 'Не назначен',
    General: 'Общее',
    IT: 'ИТ-поддержка',
    Billing: 'Финансы и биллинг',
    HR: 'HR и кадры',
    Accounting: 'Бухгалтерия',
    Facilities: 'АХО и офис'
  };

  const channels = {
    web: 'Сайт',
    email: 'Почта',
    bot: 'Бот',
    api: '🔌 API'
  };

  function channelTag(value) {
    return `<span class="tag channel">${escapeHTML(channels[value] || value || 'Сайт')}</span>`;
  }

  const priorities = {
    high: 'Высокий',
    medium: 'Средний',
    low: 'Низкий'
  };

  function priorityTag(value) {
    return `<span class="tag priority-${escapeHTML(value)}">${escapeHTML(priorities[value] || value)}</span>`;
  }

  function departmentTag(value) {
    return `<span class="tag department">${escapeHTML(departments[value] || value || 'Общее')}</span>`;
  }

  function regulationMarkup(value) {
    let text = String(value || '');
    const source = text.match(/\n\n\[Источник: «([^\n]+)» \(knowledge_base\/[^\n]+\)\]$/);
    if (source) text = text.slice(0, source.index);
    // Escape first; only the two explicit text-formatting patterns become HTML.
    const body = escapeHTML(text).replace(/^#{1,6} (.+)$/gm, '<strong>$1</strong>').replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    return `${body}${source ? `<span class="regulation-source">Регламент: ${escapeHTML(source[1])}</span>` : ''}`;
  }

  function statusLabel(value) {
    return (
      {
        new: 'Новый',
        awaiting_confirmation: 'Ожидает подтверждения',
        assigned_to_operator: 'Передано специалисту',
        auto_resolved: 'Решено по регламенту',
        resolved: 'Решено специалистом',
        closed: 'Закрыт'
      }[value] || value
    );
  }

  function statusClass(value) {
    return ['auto_resolved', 'closed', 'resolved'].includes(value)
      ? 'status-success'
      : value === 'assigned_to_operator'
        ? 'status-pending'
        : '';
  }

  function toast(message) {
    const element = $('#toast');
    if (!element) return;
    element.textContent = message;
    element.classList.add('show');
    setTimeout(() => element.classList.remove('show'), 3500);
  }

  function setAPIStatus(ok) {
    const dot = $('#api-dot');
    const label = $('#api-status');
    if (dot) dot.className = `status-dot ${ok ? 'ok' : 'bad'}`;
    if (label) label.textContent = ok ? 'API подключен (Online)' : 'Ошибка соединения с API';
  }

  function renderChannels(active = []) {
    const element = $('#channel-indicators');
    if (!element) return;
    const labels = { Web: 'Сайт', Telegram: 'Telegram', MAX: 'MAX', Email: 'Почта' };
    element.hidden=!active.some(channel=>channel!=='Web');
    element.innerHTML = active.filter(channel=>channel!=='Web')
      .map(
        channel =>
          `<span class="channel-indicator" title="Канал подключён"><i></i>${escapeHTML(labels[channel] || channel)}</span>`
      )
      .join('');
  }

  async function checkAPI() {
    if (!hasSession()) return;
    try {
      const me = await api('/api/v1/auth/me');
      if (state.user?.id && state.user.id !== me.user.id) { sessionChanged(); return; }
      if (state.user?.role !== me.user.role || state.user?.id !== me.user.id) clearWorkspaceData();
      state.user = me.user;
      applyRoleUI(false);
      setAPIStatus(true);
      renderChannels(['Web']);
      const label = $('#api-status');
      if (label) {
        label.textContent = 'Сервис доступен';
      }
    } catch {
      setAPIStatus(false);
    }
  }

  function updateTicketPaging(prefix, tickets, size, cursor) {
    const older = $(`#${prefix}-older`), newest = $(`#${prefix}-newest`);
    if (older) { older.hidden = tickets.length < size; older.dataset.before = tickets.length ? String(tickets[tickets.length - 1].id) : ''; }
    if (newest) newest.hidden = !cursor;
    const note = $(`#${prefix}-page-note`);
    if (note) note.textContent = 'Поиск по ID, слову или фразе работает по всей доступной истории.';
  }

  async function loadHistory() {
    if (!hasSession() || !canTab('employee')) return;
    try {
      const email = $('#email');
      const cursor = state.historyBefore, query = ($('#history-search')?.value || '').trim(), generation = sessionGeneration;
      const tickets = await api(`/api/tickets?limit=20&before_id=${cursor}&q=${encodeURIComponent(query)}`);
      if (cursor !== state.historyBefore || query !== ($('#history-search')?.value || '').trim() || generation !== sessionGeneration) return;
      updateTicketPaging('history', tickets, 20, cursor);
      state.tickets = tickets;
      const value = email ? email.value.trim() : '';
      renderHistory(tickets.filter(ticket => state.user?.role === 'employee' || !value || ticket.user_email === value));
      const emptyWorkspace = $('#employee-workspace-empty');
      if (emptyWorkspace) emptyWorkspace.hidden = true;
      const employeeGrid = $('.employee-grid');
      if (employeeGrid) employeeGrid.hidden = false;
      if (state.currentTicketId !== null) {
        const current = tickets.find(ticket => String(ticket.id) === String(state.currentTicketId));
        if (current && !document.activeElement?.matches('#analysis-result button, #analysis-result input, #analysis-result textarea, #analysis-result a')) renderAnalysis(current);
      }
    } catch (error) {
      const list = $('#history-list');
      if (list) list.textContent = `Не удалось загрузить историю. ${error.message}`;
    }
  }

  function renderHistory(tickets) {
    const list = $('#history-list');
    if (!list) return;
    if (tickets) state.historyTickets = tickets;
    const query = ($('#history-search')?.value || '').trim().toLocaleLowerCase('ru');
    tickets = state.historyTickets.slice(0, 20);
    list.innerHTML = tickets.length
      ? tickets
          .map(
            ticket =>
              `<details class="history-row ticket-row" data-ticket-id="${ticket.id}" ${state.ticketRowsOpen?.has(`history:${ticket.id}`)?'open':''}><summary class="ticket-row-summary">${ticketRowSummary(ticket)}</summary><div class="history-detail">${ticket.rag_answer ? `<b>Предложенный регламент ИИ</b><div class="rag-mini">${regulationMarkup(ticket.rag_answer)}</div>` : ''}${intakeContextMarkup(ticket.raw_text)}${ticket.operator_reply ? `<b>Ответ специалиста поддержки</b><div class="support-reply">${escapeHTML(ticket.operator_reply)}</div>` : ['closed', 'resolved'].includes(ticket.status) ? '<div class="support-reply">Ответ специалиста: тикет закрыт</div>' : '<p class="muted">Ответ специалиста пока не добавлен.</p>'}</div></details>`
          )
          .join('')
      : `<p class="muted">${query ? 'Ничего не найдено. Попробуйте другой номер или слово.' : 'Пока нет обращений. Опишите проблему в форме — ответ появится здесь.'}</p>`;

    $$('.history-row', list).forEach(row => {
      row.addEventListener('toggle',()=>{const key=`history:${row.dataset.ticketId}`;if(row.open)state.ticketRowsOpen.add(key);else state.ticketRowsOpen.delete(key);});
      on(row, 'click', event => {
        if (event.target.closest('button, a')) return;
        const ticket = state.tickets.find(item => String(item.id) === String(row.dataset.ticketId));
        if (!ticket) return;
        state.currentTicketId = ticket.id;
        renderAnalysis(ticket);
      });
    });
  }

  function renderAnalysis(ticket) {
    const result = $('#analysis-result');
    if (!result) return;
    window.FlowOpsSupportUI?.open(ticket.id);
    const operatorReply = String(ticket.operator_reply || '').trim();
    const complete = ['auto_resolved', 'closed', 'resolved'].includes(ticket.status);
    const waiting = ticket.rag_answer && !operatorReply && !complete && ticket.status !== 'assigned_to_operator';
    const title = operatorReply ? 'Ответ специалиста' : complete ? 'Обращение решено' : waiting ? 'Нашли инструкцию для вас' : 'Передано специалисту';
    const description = operatorReply ? 'Ответ сохранён в переписке ниже. Продолжайте диалог или подтвердите решение.' : complete ? 'Обращение закрыто. Решение останется в истории.' : waiting ? 'Выполните действия из регламента и сообщите, помогли ли они.' : ticket.rag_answer ? 'Регламент не помог. Специалист выбранного отдела продолжит работу.' : 'Готового регламента нет. Обращение направлено в нужный отдел.';
    const next = !complete && !waiting && !operatorReply ? '<p class="next-action">Переписка доступна ниже. Пока обращение открыто, вы можете дополнять его сообщениями.</p>' : '';
    result.classList.remove('empty');
    result.innerHTML = `<div class="result-heading"><span class="ticket-reference">Обращение #${ticket.id}</span><span class="status ${statusClass(ticket.status)}">${escapeHTML(statusLabel(ticket.status))}</span></div><h2>${title}</h2><p class="result-description">${description}</p><div class="analysis-summary">${escapeHTML(ticket.extracted_summary || ticket.raw_text || 'Суть не выделена')}</div><dl class="routing-details"><div><dt>Ответственный отдел</dt><dd>${departmentTag(ticket.department)}</dd></div><div><dt>Приоритет</dt><dd>${priorityTag(ticket.priority)}</dd></div></dl>${next}`;
    $$('[data-feedback]', result).forEach(button => on(button, 'click', () => handleFeedback(ticket, result, button)));
  }

  async function handleFeedback(ticket, result, clickedButton) {
    const buttons = $$('[data-feedback]', result);
    if (!buttons.length || buttons.some(button => button.disabled)) return;
    buttons.forEach(button => {
      button.disabled = true;
    });
    const isResolved = clickedButton.dataset.feedback === 'true';
    try {
      const response = await api(`/api/tickets/${ticket.id}/feedback`, {
        method: 'POST',
        body: JSON.stringify({ resolved: isResolved })
      });
      ticket.status = response.status || (isResolved ? 'auto_resolved' : 'assigned_to_operator');
      const current = state.tickets.find(item => String(item.id) === String(ticket.id));
      if (current) current.status = ticket.status;
      renderAnalysis(ticket);
      const email = $('#email')?.value.trim() || '';
      renderHistory(state.tickets.filter(item => state.user?.role === 'employee' || !email || item.user_email === email));
    } catch (error) {
      buttons.forEach(button => {
        button.disabled = false;
      });
      toast(error.message);
    }
  }

  let intakeEpoch = 0;
  let intakeMessage = '';
  let intakeDetails = {};
  function resetIntake() {
    intakeEpoch++; intakeMessage = ''; intakeDetails = {};
    const panel = $('#intake-panel'); if (panel) panel.hidden = true;
    const fields = $('#intake-fields'); if (fields) fields.replaceChildren();
    const label = $('#ticket-form .primary-button span'); if (label) label.textContent = 'Продолжить';
  }
  function collectIntake() {
    $$('#intake-fields input').forEach(input => { intakeDetails[input.dataset.intakeKey] = input.value.trim(); });
  }
  async function previewIntake(message) {
    if (message !== intakeMessage) { resetIntake(); intakeMessage = message; }
    collectIntake();
    const epoch = intakeEpoch;
    const plan = await api('/api/v1/intake/preview', { method: 'POST', body: JSON.stringify({message, details: intakeDetails}) });
    if (epoch !== intakeEpoch) return false;
    if (plan.ready) return true;
    $('#intake-panel').hidden = false;
    $('#intake-title').textContent = plan.title;
    // Preserve answers and keyboard focus when another validation pass is needed.
    const keys = new Set($$('#intake-fields input').map(input => input.dataset.intakeKey));
    plan.fields.forEach(field => {
      if (keys.has(field.key)) return;
      const label = document.createElement('label'); label.htmlFor = `intake-${field.key}`; label.textContent = field.label;
      const input = document.createElement('input'); input.id = label.htmlFor; input.dataset.intakeKey = field.key;
      input.maxLength = 300; input.autocomplete = 'off';
      input.placeholder = { location: 'Например: корпус А, аудитория 313', service: 'Название сервиса', symptom: 'Опишите, что видите', impact: 'Как это мешает работе?', tried: 'Что уже пробовали?' }[field.key] || '';
      input.addEventListener('input', () => { intakeDetails.skip = false; });
      const hint = document.createElement('p'); hint.id = `${input.id}-hint`; hint.className = 'field-help'; hint.textContent = field.hint;
      input.setAttribute('aria-describedby',hint.id);
      const group = document.createElement('div'); group.append(label,input,hint); $('#intake-fields').append(group);
    });
    const label = $('#ticket-form .primary-button span'); if (label) label.textContent = 'Отправить обращение';
    const first = plan.fields.map(field => $(`#intake-${field.key}`)).find(input => input && !input.value.trim());
    first?.focus();
    return false;
  }
  function intakeContextMarkup(text) {
    const marker = '\n\nУточнения заявителя:\n';
    const index = (text || '').lastIndexOf(marker);
    if (index < 0) return '';
    return `<section class="intake-context"><h4>Контекст для специалиста</h4><p>${escapeHTML(text.slice(index + marker.length))}</p></section>`;
  }

  function prepareTicketRequest(email, text, details = {}) {
    const body = JSON.stringify({ user_email: email.trim(), raw_text: text.trim(), intake: details });
    if (!state.pendingTicket || state.pendingTicket.body !== body) {
      const bytes = new Uint8Array(16); crypto.getRandomValues(bytes);
      state.pendingTicket = { body, key: `web-${Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')}` };
    }
    return state.pendingTicket;
  }

  async function submitTicket(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const email = $('#email');
    const problem = $('#problem');
    const button = $('.primary-button', form);
    if (!email || !problem || button?.disabled) return;
    if (!problem.value.trim()) { toast('Опишите проблему перед отправкой.'); problem.focus(); return; }
    const errorBox = $('#ticket-error');
    if (errorBox) errorBox.hidden = true;
    const submissionSession = sessionGeneration;
    const submissionUser = state.user?.id;
    const submissionRole = state.user?.role;
    const isCurrentSubmission = () => submissionSession === sessionGeneration && state.user?.id === submissionUser && state.user?.role === submissionRole;
    const controls = [...(form.querySelectorAll?.('#intake-fields input, #intake-skip') || [])];
    controls.forEach(field => { field.disabled = true; });
    const progress = $('#ticket-progress');
    if (progress) { progress.textContent = 'Проверяем, хватает ли сведений для обращения…'; progress.hidden = false; }
    form.setAttribute('aria-busy', 'true');
    if (button) {
      button.disabled = true;
      const label = $('span', button);
      if (label) label.textContent = 'Проверяем обращение…';
    }
    const submittedProblem = problem.value;
    try {
      if (!await previewIntake(submittedProblem.trim()) || problem.value !== submittedProblem || !isCurrentSubmission()) return;
      if (progress) progress.textContent = 'Сохраняем обращение и проверяем базу регламентов…';
      const request = prepareTicketRequest(email.value, submittedProblem, intakeDetails);
      const ticket = await api('/api/tickets', {
        method: 'POST', headers: { 'Idempotency-Key': request.key }, body: request.body
      });
      if (!isCurrentSubmission()) return;
      if (state.pendingTicket === request) state.pendingTicket = null;
      if (problem.value === submittedProblem) { problem.value = ''; resetIntake(); }
      state.currentTicketId = ticket.id;
      state.historyBefore = 0;
      renderAnalysis(ticket);
      $('#analysis-result')?.focus({ preventScroll: true });
      $('#analysis-result')?.scrollIntoView({ behavior: 'auto', block: 'nearest' });
      toast(`Обращение #${ticket.id} принято`);
      try { await loadHistory(); } catch (_) { if (isCurrentSubmission()) toast('Обращение принято. Список обращений пока не обновился — обновите его позже.'); }
    } catch (error) {
      if (!isCurrentSubmission()) return;
      if (errorBox) { errorBox.textContent = `Не удалось отправить обращение. ${error.message} Текст сохранён в форме — попробуйте ещё раз.`; errorBox.hidden = false; }
      toast(error.message);
    } finally {
      if (!isCurrentSubmission()) return;
      controls.forEach(field => { field.disabled = false; });
      form.removeAttribute('aria-busy');
      if (progress) progress.hidden = true;
      if (button) {
        button.disabled = false;
        const label = $('span', button);
        if (label) label.textContent = $('#intake-panel')?.hidden ? 'Продолжить' : 'Отправить обращение';
      }
    }
  }

  async function loadOperator(forceRender = false) {
    if (!hasSession() || !canTab('operator') || state.operatorLoading) return;
    state.operatorLoading = true;
    const query = ($('#queue-search')?.value || '').trim(), epoch = state.queueEpoch, generation = sessionGeneration;
    try {
      const cursor = state.operatorBefore;
      const tickets = await api(`/api/tickets?limit=100&before_id=${cursor}&q=${encodeURIComponent(query)}&queue_status=${encodeURIComponent($('#status-filter')?.value || '')}`);
      if (cursor !== state.operatorBefore || query !== ($('#queue-search')?.value || '').trim() || epoch !== state.queueEpoch || generation !== sessionGeneration) return;
      updateTicketPaging('queue', tickets, 100, cursor);
      state.tickets = tickets;
      tickets.forEach(ticket=>setTicketUnread(ticket.id,ticket.unread_employee_message));
      if (forceRender === true || !$('#ticket-list')?.contains(document.activeElement)) renderTickets();
      const emptyOperator = $('#operator-empty');
      if (emptyOperator) {emptyOperator.hidden = tickets.length !== 0; $('h3',emptyOperator).textContent=query?'По запросу ничего не найдено':'Личный список тикетов пуст';}
      setAPIStatus(true);
    } catch (error) {
      setAPIStatus(false);
      const status = $('#metrics-status');
      if (status) {
        status.textContent = 'Нет обновления: показаны последние загруженные данные, если они есть';
        status.classList.add('stale');
      }
      toast(error.message);
    } finally {
      state.operatorLoading = false;
      if (generation === sessionGeneration && (query !== ($('#queue-search')?.value || '').trim() || epoch !== state.queueEpoch)) loadOperator(true);
    }
  }

  async function loadAudit() {
    const list = $('#audit-list');
    if (!list) return;
    try {
      const entries = await api('/api/audit');
      list.innerHTML = entries.length
        ? entries
            .map(
              entry =>
                `<article class="audit-entry"><time>${new Date(entry.created_at).toLocaleString('ru-RU')}</time><b>${escapeHTML(entry.event_type)}</b><span>${escapeHTML(entry.details || '')}</span>${entry.ticket_id ? `<small>Тикет #${entry.ticket_id}</small>` : ''}</article>`
            )
            .join('')
        : '<p class="muted">Записей пока нет.</p>';
    } catch (error) {
      list.innerHTML = `<p class="muted">${escapeHTML(error.message)}</p>`;
    }
  }

  function renderMetrics(stats) {
    const element = $('#metrics');
    if (!element) return;
    const number = value => (Number.isFinite(Number(value)) && Number(value) >= 0 ? Number(value) : 0);
    const format = (value, digits = 0) => number(value).toLocaleString('ru-RU', { maximumFractionDigits: digits });
    const codes = [
      ...new Set([
        'IT',
        'Billing',
        'HR',
        'Accounting',
        'Facilities',
        'General',
        'Unassigned',
        ...Object.keys(stats.by_department || {}),
        ...Object.keys(stats.active_by_department || {})
      ])
    ];
    const total = number(stats.total);
    const open = number(stats.open);
    element.innerHTML = `
      <article class="metric"><div class="metric-label">Всего обращений</div><div class="metric-value">${format(total)}</div><div class="metric-detail">за всё время · все каналы</div></article>
      <article class="metric metric-accent"><div class="metric-label">Deflection Rate</div><div class="metric-value">${format(stats.deflection_rate_percent, 1)}<small>%</small></div><div class="metric-detail">${format(stats.auto_resolved)} из ${format(total)} подтверждены без ответа оператора</div></article>
      <article class="metric"><div class="metric-label">Сэкономлено часов · оценка</div><div class="metric-value">${format(stats.estimated_saved_hours, 2)}<small>ч</small></div><div class="metric-detail">${format(stats.auto_resolved)} × ${format(stats.baseline_minutes_per_ticket, 1)} мин / 60</div></article>
      <article class="metric"><div class="metric-label">Открытые обращения</div><div class="metric-value">${format(open)}</div><div class="metric-detail">из них ${format(stats.awaiting_confirmation)} ожидают подтверждения</div></article>
      <article class="metric"><div class="metric-label">Работа операторов · наблюдение</div><div class="metric-value">${format(number(stats.operator_active_seconds) / 60, 1)}<small>мин</small></div><div class="metric-detail">${format(stats.operator_measured_tickets)} обращений · среднее ${format(number(stats.operator_average_seconds) / 60, 1)} мин; только интервалы активности</div></article>
      <article class="metric"><div class="metric-label">Исправления маршрута</div><div class="metric-value">${format(stats.routing_corrected_tickets)}</div><div class="metric-detail">обращений с изменённым отделом · ${format(stats.routing_corrections)} изменений; это не оценка точности ИИ</div></article>
      <article class="metric load-panel"><div class="load-heading"><h3>Распределение нагрузки по отделам</h3><span>Открытые / всего · доля открытой очереди</span></div><div class="department-load">${codes
        .map(code => {
          const count = number(stats.active_by_department?.[code]);
          const share = open ? Math.min(100, (count * 100) / open) : 0;
          return `<div class="load-item"><div class="load-label"><span>${escapeHTML(departments[code] || code)}</span><strong>${format(count)} <small>/ ${format(stats.by_department?.[code])}</small></strong></div><div class="load-track" role="meter" aria-label="${escapeHTML(departments[code] || code).replace(/"/g, '&quot;')}: доля открытой очереди" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${share.toFixed(1)}"><span style="width:${share}%"></span></div></div>`;
        })
        .join('')}</div>${open ? '' : '<p class="muted">Открытых обращений нет.</p>'}</article>`;
  }

  function renderTickets() {
    const list = $('#ticket-list');
    if (!list) return;
    const department = $('#department-filter');
    const status = $('#status-filter');
    const priority = $('#priority-filter')?.value || '';
    const query = ($('#queue-search')?.value || '').trim().toLocaleLowerCase('ru');
    const tickets = state.tickets.filter(
      ticket =>
        (!department || !department.value || ticket.department === department.value) &&
        (!status || !status.value || ticket.display_status === status.value) &&
        (!priority || ticket.priority === priority)
    );
    const count = $('#queue-count');
    if (count) count.textContent = `Найдено: ${tickets.length} из ${state.tickets.length}`;
    list.innerHTML = tickets.length
      ? tickets.map(ticketCard).join('')
      : state.tickets.length
        ? '<div class="panel empty-state" style="padding:50px;text-align:center"><div class="empty-icon">✓</div><h3>Очередь пуста</h3><p>По выбранным фильтрам обращений нет.</p></div>'
        : '';
    $$('.ticket-card', list).forEach(bindTicketControls);
  }

  let ticketRouteVersion = 0;
  function navigateTicket(id) {
    history.pushState({}, '', `/tickets/${Number(id)}`);
    renderTicketRoute();
  }
  async function renderTicketRoute() {
    const version = ++ticketRouteVersion;
    const match = location.pathname.match(/^\/tickets\/(\d+)\/?$/);
    if (!match || !authReady) return;
    const generation = sessionGeneration;
    leaveTicketContent();
    $$('.view').forEach(view => view.classList.remove('active'));
    $('#ticket-detail').classList.add('active');
    $('#ticket-detail-title').textContent = `Тикет #${match[1]}`;
    $('#ticket-detail-status').textContent = 'Загрузка тикета…';
    try {
      const ticket = await api(`/api/tickets/${match[1]}`);
      if (version !== ticketRouteVersion || generation !== sessionGeneration) return;
      $('#ticket-detail-status').textContent = '';
      $('#ticket-detail-title').textContent = `ID ${ticket.id}: ${ticketRowTitle(ticket)}`;
      if (canTab('operator')) {
        const template = document.createElement('template');
        template.innerHTML = ticketCard(ticket, 'detail');
        const card = template.content.querySelector('.ticket-card');
        $('#ticket-detail-content').append(card);
        bindTicketControls(card);
        window.FlowOpsSupportUI?.openOperator(ticket.id);
        card.querySelector('.work-status').before($('#operator-dialogue'));
        card.querySelector('[data-open-dialogue]')?.remove();
        window.FlowOpsAnalytics?.attachTimeline(card,ticket);
        window.FlowOpsCopilot?.attach(card,ticket);
      } else if (state.user?.role === 'employee') { window.FlowOpsSupportUI?.open(ticket.id); window.FlowOpsAnalytics?.attachTimeline($('#ticket-detail-content'),ticket); }
    } catch (error) {
      if (version === ticketRouteVersion && generation === sessionGeneration) $('#ticket-detail-status').textContent = error.message;
    }
    window.scrollTo({top:0,behavior:'instant'});
  }
  function leaveTicketContent() {
    const operator = $('#operator-dialogue');
    if (operator) { operator.hidden = true; $('#ticket-list').before(operator); }
    const employee = $('#employee-dialogue');
    if (employee) { employee.hidden = true; $('#analysis-result').after(employee); }
    $('#ticket-detail-content')?.replaceChildren();
  }
  function leaveTicketRoute() {
    ++ticketRouteVersion;
    leaveTicketContent();
  }

  function ticketRowTitle(ticket) {
    return String(ticket.subject || ticket.extracted_summary || ticket.raw_text || 'Без темы').split(/\s*Уточнения (?:заявителя|пропущены)/i)[0].replace(/\s+/g,' ').trim() || 'Без темы';
  }

  function ticketRowSummary(ticket, indicator = false) {
    const title = `ID ${ticket.id}: ${ticketRowTitle(ticket)}`;
    const status = statusLabel(ticket.status);
    const compactStatus=({new:'Новый',in_progress:'В работе',closed:'Закрыт'}[ticket.display_status]) || {new:'Новый',awaiting_confirmation:'Ждёт подтверждения',assigned_to_operator:'В работе',auto_resolved:'Закрыт',resolved:'Есть ответ',closed:'Закрыт'}[ticket.status]||status;
    const date = new Date(ticket.created_at).toLocaleDateString('ru-RU',{day:'numeric',month:'short',year:'numeric'});
    return `<span class="ticket-row-status" title="${escapeHTML(status)}">${escapeHTML(compactStatus)}</span><a class="ticket-row-title" href="/tickets/${ticket.id}" title="${escapeHTML(title)}">${escapeHTML(title)}</a><time class="ticket-row-date" datetime="${escapeHTML(ticket.created_at)}">${escapeHTML(date)}</time>${indicator?`<span class="ticket-unread-dot" data-unread="${Boolean(ticket.unread_employee_message)}" role="img" aria-label="Новое обращение или сообщение сотрудника" aria-hidden="${!ticket.unread_employee_message}"></span>`:''}`;
  }

  function setTicketUnread(id, unread) {
    const dot = document.querySelector(`#ticket-list .ticket-row[data-ticket-id="${Number(id)}"] .ticket-unread-dot`);
    if(dot){dot.dataset.unread=String(Boolean(unread));dot.setAttribute('aria-hidden',String(!unread));}
  }

  let closureReasonLabels = {solved:'Вопрос решён',duplicate:'Дубликат обращения',cancelled:'Отменено сотрудником',no_response:'Нет ответа сотрудника',out_of_scope:'Вне зоны поддержки',other:'Другая причина',unspecified:'Причина не указана'};
  function ticketCard(ticket, scope = 'queue') {
    const departmentCodes = [...new Set([...(state.departmentCodes || ['IT', 'Billing', 'HR', 'Accounting', 'Facilities', 'General']), ticket.department].filter(Boolean))];
    const priorityCodes = ['high', 'medium', 'low'];
    const piiBadge =
      Number(ticket.pii_masked_count) > 0
        ? `<div class="pii-badge">ПДн обезличены перед отправкой в ИИ (скрыто: ${Number(ticket.pii_masked_count)} — ${escapeHTML((ticket.pii_masked_types || []).join(', '))})</div>`
        : '';
    return `<details class="ticket-row" data-ticket-id="${ticket.id}" ${state.ticketRowsOpen?.has(`queue:${ticket.id}`)?'open':''}><summary class="ticket-row-summary">${ticketRowSummary(ticket,true)}</summary><article class="ticket-card" data-id="${ticket.id}" data-status="${escapeHTML(ticket.status)}" data-closure-reason="${escapeHTML(ticket.closure_reason || '')}"><div class="ticket-top"><span class="ticket-id">#${ticket.id} · ${new Date(ticket.created_at).toLocaleString('ru-RU')}</span><span class="ticket-email">${escapeHTML(ticket.requester_email || (/^(tg:|max:)/.test(ticket.user_email || '') ? 'Почта не привязана' : ticket.user_email) || 'Email не указан')}</span></div><div class="ticket-assignment"><select data-field="department" aria-label="Отдел">${departmentCodes
      .map(
        item => `<option value="${escapeHTML(item)}" ${item === ticket.department ? 'selected' : ''}>${escapeHTML(departments[item] || item)}</option>`
      )
      .join('')}</select><select data-field="priority" aria-label="Приоритет">${priorityCodes
      .map(item => `<option value="${item}" ${item === ticket.priority ? 'selected' : ''}>${priorities[item]}</option>`)
      .join('')}</select></div>${piiBadge}<h3 class="ticket-summary">${escapeHTML(ticket.subject || ticket.extracted_summary || 'Без выделенной сути')}</h3><details class="original-request"><summary>Исходное обращение</summary><p class="ticket-raw">${escapeHTML(ticket.raw_text)}</p></details>${intakeContextMarkup(ticket.raw_text)}<div class="ticket-meta">${departmentTag(ticket.department)}${priorityTag(ticket.priority)}${channelTag(ticket.channel)}<span class="status ${statusClass(ticket.status)}">${escapeHTML(statusLabel(ticket.status))}</span></div>${ticket.rag_answer ? `<div class="rag-mini"><b>Предложенный регламент</b><br>${regulationMarkup(ticket.rag_answer)}</div>` : ''}${ticket.operator_reply ? `<div class="operator-reply existing"><b>Отправленный ответ</b><p>${escapeHTML(ticket.operator_reply)}</p></div>` : ''}<div class="work-status"><span data-work-time="${ticket.id}">Время пока не учитывается</span><button type="button" class="secondary-button" data-action="work-pause" hidden>Пауза</button></div><div class="operator-reply reply-composer"><label class="rag-check"><input type="checkbox" data-rag-toggle> Пополнить базу знаний этим решением</label><select data-rag-department aria-label="Категория регламента"><option value="General">Общее / Авто (без отдела)</option>${departmentCodes
      .filter(item => item !== 'General')
      .map(item => `<option value="${escapeHTML(item)}">${escapeHTML(departments[item] || item)}</option>`)
      .join('')}</select><label class="reply-label">Ответ сотруднику<textarea data-reply aria-label="Ответ сотруднику" placeholder="Напишите решение или ответ для сотрудника..." rows="4"></textarea></label><input class="rag-title" data-rag-title placeholder="Название регламента (необязательно)" hidden><select class="rag-ttl" data-rag-ttl hidden><option value="0">Бессрочно</option><option value="15">Демо-таймер: 15 секунд</option><option value="1800">На 30 минут</option><option value="7200">На 2 часа</option></select><input class="rag-keywords" data-rag-keywords placeholder="Ключевые слова через запятую" hidden><button type="button" class="reply-button" data-action="reply">Отправить</button></div><div class="ticket-bottom"><div class="ticket-controls"><button type="button" class="close" data-action="close" popovertarget="close-ticket-${scope}-${ticket.id}">Закрыть тикет</button><div id="close-ticket-${scope}-${ticket.id}" class="close-ticket-choice" popover><h3>Причина закрытия</h3><label>Выберите причину<select data-close-reason><option value="">Выберите…</option>${Object.entries(closureReasonLabels).filter(([key])=>key!=='unspecified').map(([value,label])=>`<option value="${value}">${label}</option>`).join('')}</select></label><p class="field-help">Клик снаружи или Escape отменяет выбор.</p><button type="button" class="close" data-confirm-close>Закрыть тикет</button></div></div></div></article></details>`;
  }

  function bindTicketControls(card) {
    const id = card.dataset.id;
    const row=card.closest('.ticket-row');
    row?.addEventListener('toggle',()=>{const key=`queue:${id}`;if(row.open)state.ticketRowsOpen.add(key);else state.ticketRowsOpen.delete(key);});
    const dialogueButton=document.createElement('button');dialogueButton.type='button';dialogueButton.className='secondary-button';dialogueButton.dataset.openDialogue='';dialogueButton.textContent='Раскрыть диалог';
    dialogueButton.addEventListener('click',()=>location.pathname===`/tickets/${id}`?window.FlowOpsSupportUI?.openOperator(id):navigateTicket(id));$('.ticket-controls',card)?.prepend(dialogueButton);
    const draftFields = '[data-reply], [data-rag-toggle], [data-rag-department], [data-rag-title], [data-rag-ttl], [data-rag-keywords]';
    const draft = state.replyDrafts.get(id);
    const fields = $$(draftFields, card);
    if (draft) fields.forEach((field, index) => {
      if (field.type === 'checkbox') field.checked = draft[index];
      else field.value = draft[index];
    });
    const saveDraft = () => state.replyDrafts.set(id, fields.map(field => field.type === 'checkbox' ? field.checked : field.value));
    on(card, 'input', saveDraft);
    on(card, 'change', saveDraft);
    const ragEnabled = card.querySelector('[data-rag-toggle]')?.checked;
    card.querySelectorAll('[data-rag-keywords], [data-rag-title], [data-rag-ttl]').forEach(field => field.hidden = !ragEnabled);
    const close = card.querySelector('[data-action="close"]');
    const status = card.dataset.status;
    if (status === 'closed' || status === 'auto_resolved') {
      if (close) {
        close.disabled = true;
        close.textContent = 'Закрыт';
        close.removeAttribute('popovertarget');
        const reason = document.createElement('span'); reason.className = 'ticket-closure-reason';
        const ticket = state.tickets.find(item => String(item.id) === id);
        const code = card.dataset.closureReason || ticket?.closure_reason || '';
        reason.textContent = `Причина: ${closureReasonLabels[code] || (status === 'auto_resolved' ? 'Решено по регламенту' : 'Не указана')}`;
        close.after(reason);
        card.querySelector('.reply-composer').hidden = true;
      }
    }
    card.querySelectorAll('select[data-field]').forEach(select =>
      on(select, 'change', () => updateTicket(id, { [select.dataset.field]: select.value }))
    );
    on($('[data-action="work-pause"]', card), 'click', () => trackWork(id, 'pause'));
    on(card, 'input', () => { if (String(workState.id) === id) { workState.activity = Date.now(); if (!workState.running) trackWork(id, 'start'); } });
    on($('[data-reply]', card), 'focus', () => { if (!workState.running || String(workState.id) !== id) trackWork(id, 'start'); });
    renderWork();
    const choice = card.querySelector('.close-ticket-choice');
    on(choice, 'toggle', event => { if (event.newState === 'closed') choice.querySelector('[data-close-reason]').value = ''; });
    on(card.querySelector('[data-confirm-close]'), 'click', async event => {
      const reason = choice.querySelector('[data-close-reason]');
      if (!reason.value) { reason.focus(); toast('Выберите причину закрытия'); return; }
      const saved = await updateTicket(id, {status:'closed',closure_reason:reason.value}, event.currentTarget);
      if (saved && choice.isConnected) choice.hidePopover();
    });
    const toggle = card.querySelector('[data-rag-toggle]');
    on(toggle, 'change', () => {
      card
        .querySelectorAll('[data-rag-keywords], [data-rag-title], [data-rag-ttl]')
        .forEach(input => (input.hidden = !toggle.checked));
    });
    on(card.querySelector('[data-action="reply"]'), 'click', () => sendOperatorReply(id, card));
  }

  async function sendOperatorReply(id, card) {
    const reply = card.querySelector('[data-reply]');
    const button = card.querySelector('[data-action="reply"]');
    if (button?.disabled) return;
    if (!reply || !reply.value.trim()) {
      toast('Введите ответ сотруднику');
      return;
    }
    if (button) button.disabled = true;
    try {
      await api(`/api/tickets/${id}/messages`, {
        method: 'POST',
        body: JSON.stringify(window.FlowOpsSupportUI.replyRequest(id,reply.value.trim()))
      });
      if (String(workState.id) === String(id)) await trackWork(id, 'pause');
      const toggle = card.querySelector('[data-rag-toggle]');
      if (toggle && toggle.checked) {
        const ticket = state.tickets.find(item => String(item.id) === String(id)) || {raw_text:''};
        const keywords = card.querySelector('[data-rag-keywords]');
        const department = card.querySelector('[data-rag-department]');
        const supplied = (keywords ? keywords.value : '')
          .split(',')
          .map(item => item.trim())
          .filter(Boolean);
        const titleInput = card.querySelector('[data-rag-title]');
        const ttlInput = card.querySelector('[data-rag-ttl]');
        const suffix = supplied.length ? '' : `?ticket_id=${encodeURIComponent(id)}`;
        await api(`/api/knowledge${suffix}`, {
          method: 'POST',
          body: JSON.stringify({
            department: department ? department.value : 'General',
            title:
              titleInput && titleInput.value.trim()
                ? titleInput.value.trim()
                : ticket?.extracted_summary || 'Решение оператора',
            keywords: supplied,
            ttl_seconds: Number(ttlInput ? ttlInput.value : 0),
            kind: Number(ttlInput ? ttlInput.value : 0) > 0 ? 'incident' : 'permanent',
            content: reply.value.trim()
          })
        });
        toast('Ответ отправлен, регламент добавлен в базу знаний');
      } else {
        toast('Ответ отправлен сотруднику');
      }
      state.replyDrafts.delete(String(id));
      window.FlowOpsSupportUI.replySent(id);
      await loadOperator(true);
      if (location.pathname === `/tickets/${id}`) renderTicketRoute();
    } catch (error) {
      toast(error.message);
    } finally {
      if (button) button.disabled = false;
    }
  }

  async function submitIngest() {
    const result = $('#ingest-result');
    const button = $('#ingest-submit');
    if (button?.disabled) return;
    if (!$('#ingest-sender')?.value.trim() || !$('#ingest-message')?.value.trim()) { toast('Укажите отправителя и сообщение.'); return; }
    if (button) button.disabled = true;
    try {
      const data = await api('/api/v1/ingest', {
        method: 'POST',
        body: JSON.stringify({
          channel: $('#ingest-channel')?.value,
          sender: $('#ingest-sender')?.value,
          subject: $('#ingest-subject')?.value,
          message: $('#ingest-message')?.value
        })
      });
      if (result) result.textContent = JSON.stringify(data, null, 2);
    } catch (error) {
      if (result) result.textContent = error.message;
    } finally {
      if (button) button.disabled = false;
    }
  }

  const integrationOrigin = () => window.location.origin;

  function updateWidgetPreview() {
    const title = $('#widget-title')?.value.trim() || 'Онлайн-поддержка';
    const greeting = $('#widget-greeting')?.value.trim() || 'Здравствуйте! Чем мы можем помочь?';
    let color = $('#widget-color-text')?.value.trim() || '#0f766e';
    if (!/^#[0-9a-f]{6}$/i.test(color)) color = $('#widget-color')?.value || '#0f766e';
    const previewTitle = $('#preview-title');
    const previewGreeting = $('#preview-greeting');
    const previewLauncher = $('#preview-launcher');
    if (previewTitle) {
      previewTitle.textContent = title;
      previewTitle.style.backgroundColor = color;
    }
    if (previewGreeting) previewGreeting.textContent = greeting;
    if (previewLauncher) previewLauncher.style.backgroundColor = color;
  }

  async function saveWidgetConfig() {
    try {
      const result = await api('/api/v1/widget/config', {
        method: 'PUT',
        body: JSON.stringify({
          title: $('#widget-title')?.value.trim(),
          greeting: $('#widget-greeting')?.value.trim(),
          color: $('#widget-color-text')?.value.trim()
        })
      });
      if ($('#widget-title')) $('#widget-title').value = result.title;
      if ($('#widget-greeting')) $('#widget-greeting').value = result.greeting;
      if ($('#widget-color-text')) $('#widget-color-text').value = result.color;
      if ($('#widget-color')) $('#widget-color').value = result.color;
      updateWidgetPreview();
      toast('Настройки виджета сохранены');
    } catch (error) {
      toast(error.message);
    }
  }

  async function loadWidgetConfig() {
    if (!hasSession()) return;
    try {
      const config = await api('/api/v1/widget/config');
      if ($('#widget-title')) $('#widget-title').value = config.title;
      if ($('#widget-greeting')) $('#widget-greeting').value = config.greeting;
      if ($('#widget-color-text')) $('#widget-color-text').value = config.color;
      if ($('#widget-color')) $('#widget-color').value = config.color;
      updateWidgetPreview();
    } catch {
      /* Keep defaults visible until API is available. */
    }
  }

  async function loadAPIKeys() {
    const body = $('#api-keys-list');
    if (!body) return;
    try {
      const keys = await api('/api/v1/keys');
      body.innerHTML = keys.length
        ? keys
            .map(
              key =>
                `<tr><td>${escapeHTML(key.name)}</td><td><code>${escapeHTML(key.key_prefix)}…</code></td><td>${escapeHTML(key.scope)}${key.allowed_origin ? `<small>${escapeHTML(key.allowed_origin)}</small>` : ''}</td><td>${new Date(key.created_at).toLocaleString('ru-RU')}</td><td>${key.last_used_at ? new Date(key.last_used_at).toLocaleString('ru-RU') : '—'}</td><td>${key.is_active ? `<button type="button" class="danger-button" data-revoke-key="${key.id}">Отозвать</button>` : '<span class="muted">Отозван</span>'}</td></tr>`
            )
            .join('')
        : '<tr><td colspan="6">Ключи пока не выпущены.</td></tr>';
      $$('[data-revoke-key]', body).forEach(button => on(button, 'click', () => revokeAPIKey(button.dataset.revokeKey)));
    } catch (error) {
      body.innerHTML = `<tr><td colspan="6">${escapeHTML(error.message)}</td></tr>`;
    }
  }

  async function createAPIKey(scope) {
    const payload = {
      name: scope === 'widget' ? 'Виджет сайта' : $('#crm-key-name')?.value.trim() || '1С / CRM',
      scope,
      allowed_origin: scope === 'widget' ? $('#widget-origin')?.value.trim() || '' : ''
    };
    try {
      const result = await api('/api/v1/keys', { method: 'POST', body: JSON.stringify(payload) });
      if (scope === 'widget') {
        if ($('#widget-snippet')) {
          $('#widget-snippet').value = `<script src="${integrationOrigin()}/widget.js" data-token="${result.key}" data-api-url="${integrationOrigin()}"></script>`;
        }
        if ($('#widget-snippet-box')) $('#widget-snippet-box').hidden = false;
        if ($('#issued-key-notice')) $('#issued-key-notice').hidden = true;
      } else {
        const notice = $('#issued-key-notice');
        if (notice) {
          notice.textContent = `Новый ключ (сохраните сейчас, повторно показать его нельзя): ${result.key}`;
          notice.hidden = false;
        }
        if ($('#crm-curl')) {
          $('#crm-curl').textContent = `curl -X POST ${integrationOrigin()}/api/v1/ingest -H "Authorization: Bearer ${result.key}" -H "Content-Type: application/json" -d '{"channel":"api","sender":"crm","message":"Вопрос клиента"}'`;
        }
      }
      await loadAPIKeys();
      toast('API-ключ создан. Скопируйте его сейчас.');
    } catch (error) {
      toast(error.message);
    }
  }

  async function revokeAPIKey(id) {
    if (!confirm('Отозвать этот API-ключ?')) return;
    try {
      await api(`/api/v1/keys/${encodeURIComponent(id)}`, { method: 'DELETE' });
      await loadAPIKeys();
      toast('API-ключ отозван');
    } catch (error) {
      toast(error.message);
    }
  }

  async function copyWidgetSnippet() {
    const field = $('#widget-snippet');
    if (!field) return;
    try {
      await navigator.clipboard.writeText(field.value);
      toast('Сниппет скопирован');
    } catch {
      field.select();
      document.execCommand('copy');
      toast('Сниппет скопирован');
    }
  }

  let builderDepartments = [];

  function renderBuilderDepartments() {
    const root = $('#builder-departments');
    if (!root) return;
    root.innerHTML = builderDepartments
      .map(
        (department, index) =>
          `<div class="department-row" data-department-index="${index}"><label>Название<input data-dept-name value="${escapeHTML(department.name)}" maxlength="80"></label><label>За какие вопросы отвечает<input data-dept-description value="${escapeHTML(department.description)}" maxlength="500"></label><button type="button" class="danger-button" data-remove-department="${index}">Удалить</button></div>`
      )
      .join('');
    $$('[data-remove-department]', root).forEach(button =>
      on(button, 'click', () => {
        builderDepartments.splice(Number(button.dataset.removeDepartment), 1);
        renderBuilderDepartments();
      })
    );
  }

  async function loadBuilderSettings() {
    try {
      const config = await api('/api/v1/settings/builder');
      builderDepartments = config.departments || [];
      renderBuilderDepartments();
      if ($('#builder-tone')) $('#builder-tone').value = config.tone || 'friendly';
      if ($('#builder-start-template')) $('#builder-start-template').value = config.start_template || '';
      if ($('#builder-handoff-template')) $('#builder-handoff-template').value = config.handoff_template || '';
      if ($('#builder-signature')) $('#builder-signature').value = config.signature || '';

      const channelsData = await api('/api/v1/settings/channels');
      $$('[data-channel-card]').forEach(card => {
        const configured = channelsData.configured || {};
        const metadata = (channelsData.channels || {})[card.dataset.channelCard] || {};
        const name = card.dataset.channelCard;
        const active =
          name === 'telegram'
            ? configured.telegram_token
            : name === 'max'
              ? configured.max_token
              : configured.mail_user && configured.mail_imap_host && configured.mail_smtp_host;
        const owner = metadata.bot_username
          ? `@${metadata.bot_username}`
          : metadata.email_user || metadata.bot_name || '';
        const ownerElement = $(`[data-channel-owner="${name}"]`, card);
        if (ownerElement) ownerElement.textContent = owner ? `Подключено: ${owner}` : '';
        const status = $('.channel-status', card);
        if (status) {
          status.textContent =
            metadata.status === 'warning'
              ? `Предупреждение: ${metadata.last_error}`
              : active
                ? metadata.status === 'error' && metadata.last_error
                  ? `Ошибка: ${metadata.last_error}`
                  : 'Подключён'
                : 'Не настроен';
          status.classList.toggle('configured', !!active);
        }
        const disable = $('[data-disable-channel]', card);
        if (disable) disable.hidden = !active;
      });
    } catch (error) {
      toast(error.message);
    }
  }

  async function saveBuilderSettings() {
    const departmentsList = $$('[data-department-index]').map(row => ({
      name: $('[data-dept-name]', row)?.value.trim() || '',
      description: $('[data-dept-description]', row)?.value.trim() || ''
    }));
    try {
      await api('/api/v1/settings/builder', {
        method: 'PUT',
        body: JSON.stringify({
          departments: departmentsList,
          tone: $('#builder-tone')?.value,
          start_template: $('#builder-start-template')?.value,
          handoff_template: $('#builder-handoff-template')?.value,
          signature: $('#builder-signature')?.value
        })
      });
      builderDepartments = departmentsList;
      applyDepartments(departmentsList);
      renderBuilderDepartments();
      toast('Настройки конструктора сохранены');
    } catch (error) {
      toast(error.message);
    }
  }

  function channelConfigPayload(name) {
    if (name === 'telegram') {
      return { telegram_token: $('#builder-telegram-token')?.value.trim() || '' };
    }
    if (name === 'max') {
      return { max_token: $('#builder-max-token')?.value.trim() || '' };
    }
    const [imap, smtp] = ($('#builder-mail-hosts')?.value || '').split('/').map(value => value.trim());
    return {
      mail_imap_host: imap || '',
      mail_smtp_host: smtp || '',
      mail_user: $('#builder-mail-user')?.value.trim() || '',
      mail_password: $('#builder-mail-password')?.value || ''
    };
  }

  async function saveBuilderChannel(name) {
    try {
      const result = await api('/api/v1/settings/channels', {
        method: 'PUT',
        body: JSON.stringify(channelConfigPayload(name))
      });
      toast(result.restart_required ? 'Канал сохранён и запущен' : 'Настройки канала сохранены');
      await loadBuilderSettings();
    } catch (error) {
      toast(error.message);
    }
  }

  async function disableBuilderChannel(name) {
    if (!name) return;
    try {
      await api(`/api/v1/settings/channels/${name}`, { method: 'DELETE' });
      await loadBuilderSettings();
      toast('Канал отключён');
    } catch (error) {
      toast(error.message);
    }
  }

  async function checkBuilderChannel(name) {
    const config = channelConfigPayload(name);
    const complete =
      name === 'email'
        ? config.mail_imap_host && config.mail_smtp_host && config.mail_user && config.mail_password
        : config[`${name}_token`];
    if (complete) {
      try {
        await api('/api/v1/settings/channels', { method: 'PUT', body: JSON.stringify(config) });
      } catch (error) {
        toast(error.message);
        return;
      }
    }
    const status = $('.channel-status', $(`[data-channel-card="${name}"]`));
    if (status) status.textContent = 'Проверяем…';
    try {
      const result = await api(`/api/v1/settings/channels/${name}/check`, { method: 'POST', body: '{}' });
      if (status) {
        status.textContent = result.ok ? result.message || 'Связь установлена' : `Ошибка: ${result.message}`;
        status.classList.toggle('configured', !!result.ok);
      }
      await loadBuilderSettings();
    } catch (error) {
      if (status) status.textContent = error.message;
    }
  }

  async function runSmartImport() {
    const input =
      $('#smart-import-text') ||
      $('#smart-import-content') ||
      $('#smart-import-input') ||
      $('#builder-smart-text');
    const departmentSelect = $('#smart-import-department') || $('#builder-smart-department');
    const resultBox = $('#smart-import-results');
    const button = $('#smart-import-submit');
    const text = input?.value.trim() || '';
    if (!text) {
      toast('Вставьте текст регламента или инструкции');
      return;
    }
    if (button) button.disabled = true;
    try {
      const response = await api('/api/v1/kb/smart-import', {
        method: 'POST',
        body: JSON.stringify({
          raw_text:text,
          default_department: departmentSelect?.value || 'General'
        })
      });
      const articles = response.articles || response.items || response.documents || [];
      const count = response.count ?? articles.length;
      if (resultBox) {
        resultBox.hidden = false;
        resultBox.innerHTML = `<div class="smart-import-summary">Создано черновиков: ${Number(count)} · источник: ${escapeHTML(response.source==='fallback'?'разбор текста без ИИ':response.source==='gigachat'?'ИИ':response.provider||response.source||'не указан')} · замаскировано ПДн: ${Number(response.pii_masked_count || 0)}</div>${articles
          .map(
            item =>
              `<article class="smart-import-card"><strong>${escapeHTML(item.title || 'Регламент')}</strong><small>${escapeHTML(item.department || '')}${item.keywords?.length ? ` · ${escapeHTML(item.keywords.join(', '))}` : ''}</small><pre>${escapeHTML(item.content || '')}</pre></article>`
          )
          .join('')}`;
      }
      if (input) input.value = '';
      await loadKnowledge();
      toast(`Сгенерировано регламентов: ${Number(count)}`);
    } catch (error) {
      toast(error.message);
    } finally {
      if (button) button.disabled = false;
    }
  }

  async function runSandboxPreview() {
    const message = $('#sandbox-message')?.value.trim() || '';
    if (!message) {
      toast('Введите вопрос для тестирования');
      return;
    }
    const button = $('#sandbox-run');
    if (button) button.disabled = true;
    try {
      const result = await api('/api/v1/settings/preview', {
        method: 'POST',
        body: JSON.stringify({ message, channel: $('#sandbox-channel')?.value })
      });
      const meta = $('#sandbox-meta');
      if (meta) {
        meta.textContent = `${result.matched_kb_title ? `База знаний: ${result.matched_kb_title}` : 'Совпадений в базе знаний нет'} · ${result.department} · приоритет: ${result.priority} · ${result.auto_replied ? 'автоответ' : 'передача оператору'} · ПДн скрыто: ${result.pii_masked_count || 0}`;
      }
      const email = $('#sandbox-email-preview');
      const text = $('#sandbox-text-preview');
      if ($('#sandbox-channel')?.value === 'email') {
        if (text) text.hidden = true;
        if (email) {
          email.hidden = false;
          email.srcdoc = result.rendered_preview;
        }
      } else {
        if (email) email.hidden = true;
        if (text) {
          text.hidden = false;
          text.textContent = result.rendered_preview;
        }
      }
    } catch (error) {
      const meta = $('#sandbox-meta');
      if (meta) meta.textContent = error.message;
    } finally {
      if (button) button.disabled = false;
    }
  }

  function insertBuilderVariable(value) {
    const target = $('#builder-handoff-template');
    if (!target) return;
    target.focus();
    const start = target.selectionStart ?? target.value.length;
    const end = target.selectionEnd ?? start;
    target.setRangeText(value, start, end, 'end');
    target.focus();
  }

  function setIngestDemo(type) {
    const channel = $('#ingest-channel');
    const sender = $('#ingest-sender');
    const subject = $('#ingest-subject');
    const message = $('#ingest-message');
    if (!channel || !sender || !subject || !message) return;
    if (type === 'billing') {
      channel.value = 'bot';
      sender.value = '@maria_fin';
      subject.value = '';
      message.value =
        'С корпоративной карты дважды списалась оплата за подписку по тарифу Pro, верните переплату на баланс.';
    } else if (type === 'bot') {
      channel.value = 'bot';
      sender.value = '@alex_dev';
      subject.value = '';
      message.value = 'Привет, почему не работает сервер в кластере NL-1? Всё упало!';
    } else {
      channel.value = 'email';
      sender.value = 'client@company.ru';
      subject.value = 'Лежит сервер!';
      message.value = 'Здравствуйте! У нас перестал отвечать VPS-сервер в кластере NL-1, сайты выдают 502.';
    }
  }

  async function updateTicket(id, changes, button) {
    const prior = button?.textContent;
    if (button) {
      button.disabled = true;
      button.classList.add('is-loading');
      button.textContent = 'Сохраняем…';
    }
    try {
      await api(`/api/tickets/${id}`, { method: 'PATCH', body: JSON.stringify(changes) });
      if (location.pathname === `/tickets/${id}` && changes.status === 'closed') renderTicketRoute();
      const ticket = state.tickets.find(item => String(item.id) === String(id));
      if (ticket) ticket.status = changes.status || ticket.status;
      await loadOperator(true);
      toast(changes.status === 'closed' ? `Тикет #${id} закрыт` : `Тикет #${id} взят в работу`);
      return true;
    } catch (error) {
      toast(error.message);
      return false;
    } finally {
      if (button) {
        button.classList.remove('is-loading');
        button.textContent = prior;
        button.disabled = false;
      }
    }
  }

  async function clearQueue() {
    const button=$('#clear-queue'); if(button.disabled)return;
    button.disabled=true;
    const generation=sessionGeneration;
    try {
      await api('/api/tickets/queue/clear', {method:'POST',body:'{}'});
      if(generation!==sessionGeneration)return;
      state.queueEpoch++;state.operatorBefore=0;state.tickets=[];
      $('#queue-search').value='';renderTickets();
      window.FlowOpsSupportUI?.hideOperator();
      await loadOperator(true);
      toast('Личный список очищен. Тикеты доступны через поиск.');
    } catch(error) {if(generation===sessionGeneration)toast(error.message);}
    finally {if(generation===sessionGeneration)button.disabled=false;}
  }

  async function loadKnowledge() {
    const list = $('#knowledge-list');
    if (!list || !hasSession() || !canTab('knowledge')) return;
    try {
      const documents = await api('/api/knowledge');
      const emptyKb = $('#knowledge-empty');
      if (emptyKb) emptyKb.hidden = documents.length !== 0;
      list.innerHTML = documents.length
        ? documents
            .map(
              document =>
                `<article class="knowledge-card" data-filename="${escapeHTML(document.filename)}"><div class="knowledge-card-top">${departmentTag(document.department || 'Не назначен')}<span class="ticket-id">${escapeHTML(document.filename)}</span></div><h3>${escapeHTML(document.title)}</h3><p class="knowledge-keywords">${(document.keywords || []).map(keyword => `<span>${escapeHTML(keyword)}</span>`).join('')}</p>${document.expires_at ? `<p class="muted">Автоудаление: ${new Date(document.expires_at).toLocaleString('ru-RU')}</p>` : '<p class="muted">Бессрочный регламент</p>'}<div class="knowledge-content">${escapeHTML(document.content)}</div><div class="knowledge-actions"><button type="button" data-knowledge-action="resolve">Сбой устранён</button><button type="button" data-knowledge-action="edit">Изменить ответ</button><button type="button" data-knowledge-action="delete">Удалить регламент</button></div></article>`
            )
            .join('')
        : '';
      if (state.user?.role !== 'admin') $$('[data-knowledge-action=delete], [data-knowledge-action=resolve]', list).forEach(button => button.hidden = true);
      $$('[data-knowledge-action]', list).forEach(button => on(button, 'click', () => manageKnowledge(button)));
    } catch (error) {
      toast(error.message);
    }
  }

  async function manageKnowledge(button) {
    const card = button.closest('[data-filename]');
    const filename = card?.dataset.filename;
    if (!filename) return;
    try {
      if (button.dataset.knowledgeAction === 'delete') {
        await api(`/api/knowledge/${encodeURIComponent(filename)}`, { method: 'DELETE' });
      } else if (button.dataset.knowledgeAction === 'resolve') {
        await api(`/api/knowledge/${encodeURIComponent(filename)}`, {
          method: 'PATCH',
          body: JSON.stringify({ action: 'resolve_incident', resolution_note: 'Оператор подтвердил восстановление.' })
        });
      } else {
        const content = prompt('Новый текст ответа', card.querySelector('.knowledge-content')?.textContent || '');
        if (content === null) return;
        await api(`/api/knowledge/${encodeURIComponent(filename)}`, {
          method: 'PATCH',
          body: JSON.stringify({ content })
        });
      }
      await loadKnowledge();
    } catch (error) {
      toast(error.message);
    }
  }

  async function submitKnowledge(event) {
    event.preventDefault();
    const form = event.currentTarget;
    try {
      await api('/api/knowledge', {
        method: 'POST',
        body: JSON.stringify({
          department: $('#knowledge-department')?.value,
          title: $('#knowledge-title')?.value.trim(),
          keywords: ($('#knowledge-keywords')?.value || '')
            .split(',')
            .map(item => item.trim())
            .filter(Boolean),
          content: $('#knowledge-content')?.value.trim()
        })
      });
      if (form) form.reset();
      await loadKnowledge();
      toast('Регламент сохранен и доступен ИИ');
    } catch (error) {
      toast(error.message);
    }
  }

  function startEmployeePolling() {
    if (state.employeePoll) clearInterval(state.employeePoll);
    state.employeePoll = setInterval(() => {
      if (document.hidden || !authReady || state.currentTicketId === null) return;
      const activeTab = $('.tab.active')?.dataset?.tab;
      if (activeTab && activeTab !== 'employee') return;
      const current = state.tickets.find(item => String(item.id) === String(state.currentTicketId));
      if (current && ['auto_resolved', 'closed', 'resolved'].includes(current.status)) return;
      loadHistory();
    }, 5000);
  }

  function init() {
    document.body.classList.add('auth-required');

    on($('#member-form'), 'submit', createMember);
    on($('#refresh-members'), 'click', loadMembers);
    on($('#auth-form'), 'submit', submitLogin);
    on($('#activation-form'), 'submit', submitActivation);
    on($('#login-email'), 'input', resetCompanyChoice);
    on($('#login-password'), 'input', resetCompanyChoice);
    on($('#workspace-form'), 'submit', createWorkspace);
    on($('#toggle-token'), 'click', () => {
      const field = $('#operator-token');
      if (!field) return;
      field.type = field.type === 'password' ? 'text' : 'password';
      $('#toggle-token')?.setAttribute(
        'aria-label',
        field.type === 'password' ? 'Показать код' : 'Скрыть код'
      );
    });
    $$('[data-toggle-password]').forEach(button => on(button, 'click', () => { const field = document.getElementById(button.dataset.togglePassword); field.type = field.type === 'password' ? 'text' : 'password'; button.setAttribute('aria-label', field.type === 'password' ? 'Показать пароль' : 'Скрыть пароль'); }));
    on($('#logout'), 'click', logout);

    $$('.tab').forEach(tab =>
      on(tab, 'click', () => {
        if (!canTab(tab.dataset.tab)) return;
        const wasBuilder = $('#builder')?.classList.contains('active');
        if (location.pathname.startsWith('/tickets/')) { history.pushState({}, '', '/'); leaveTicketRoute(); }
        $$('.tab, .view').forEach(element => element.classList.remove('active'));
        tab.classList.add('active');
        if (tab.closest('#settings-nav')) $('#settings-nav').open = true;
        if (tab.dataset.tab === 'builder') selectSettingsPanel(tab.dataset.settingsPanel);
        $$('.tab').forEach(item => item.setAttribute('aria-current', item === tab ? 'page' : 'false'));
        const view = $(`#${tab.dataset.tab}`);
        if (view) view.classList.add('active');
        window.scrollTo({ top: 0, behavior: 'instant' });
        if (!hasSession()) return;
        if (tab.dataset.tab === 'employee') loadHistory();
        if (tab.dataset.tab === 'operator') loadOperator();
        if (tab.dataset.tab === 'analytics') loadAnalytics();
        if (tab.dataset.tab === 'optimization') window.FlowOpsOptimization?.refresh();
        if (tab.dataset.tab === 'admin') { loadMembers(); loadAudit(); }
        if (tab.dataset.tab === 'knowledge') loadKnowledge();
        if (tab.dataset.tab === 'knowledge-review') window.FlowOpsKnowledge?.refresh();
        if (tab.dataset.tab === 'structure') window.FlowOpsStructure?.refresh();
        if (tab.dataset.tab === 'platform') window.FlowOpsPlatform?.openSettings();
        if (tab.dataset.tab === 'channels') {
          loadWidgetConfig();
          loadAPIKeys();
        }
        if (tab.dataset.tab === 'builder' && !wasBuilder) {
          loadBuilderSettings();
          loadWidgetConfig();
          loadAPIKeys();
        }
      })
    );

    $$('[data-open-builder]').forEach(button =>
      on(button, 'click', () => {
        $('.tab[data-tab="builder"]')?.click();
      })
    );

    on($('#ingest-submit'), 'click', submitIngest);
    $$('[data-ingest-demo]').forEach(button =>
      on(button, 'click', () => setIngestDemo(button.dataset.ingestDemo))
    );
    $$('[data-demo]').forEach(button =>
      on(button, 'click', () => {
        const problem = $('#problem');
        if (problem) {
          resetIntake();
          problem.value = button.dataset.demo || '';
          problem.focus();
        }
      })
    );

    on($('#problem'), 'input', resetIntake);
    on($('#intake-cancel'), 'click', () => { resetIntake(); $('#problem')?.focus(); });
    on($('#intake-skip'), 'click', () => { if ($('#ticket-form .primary-button')?.disabled) return; collectIntake(); intakeDetails.skip = true; $('#ticket-form')?.requestSubmit(); });
    on($('#ticket-form'), 'submit', submitTicket);
    on($('#email'), 'change', loadHistory);
    on($('#refresh-history'), 'click', () => { state.historyBefore = 0; loadHistory(); });
    on($('#refresh-queue'), 'click', () => { state.operatorBefore = 0; loadOperator(); });
    on($('#history-older'), 'click', () => { state.historyBefore = Number($('#history-older').dataset.before) || 0; loadHistory(); });
    on($('#history-newest'), 'click', () => { state.historyBefore = 0; loadHistory(); });
    on($('#queue-older'), 'click', () => { state.operatorBefore = Number($('#queue-older').dataset.before) || 0; loadOperator(true); });
    on($('#queue-newest'), 'click', () => { state.operatorBefore = 0; loadOperator(true); });
    on($('#clear-queue'), 'click', clearQueue);
    on($('#department-filter'), 'change', renderTickets);
    on($('#status-filter'), 'change', () => {state.queueEpoch++;state.operatorBefore=0;loadOperator(true);});
    on($('#priority-filter'), 'change', renderTickets);
    on($('#queue-search'), 'input', () => {state.queueEpoch++;state.operatorBefore=0;clearTimeout(state.queueSearchTimer);state.queueSearchTimer=setTimeout(()=>loadOperator(true),250);});
    on($('#history-search'), 'input', () => {state.historyBefore=0;clearTimeout(state.historySearchTimer);state.historySearchTimer=setTimeout(loadHistory,250);});
    on($('#knowledge-form'), 'submit', submitKnowledge);
    on($('#refresh-knowledge'), 'click', loadKnowledge);

    on($('#widget-save'), 'click', saveWidgetConfig);
    ['#widget-title', '#widget-greeting', '#widget-color-text'].forEach(selector =>
      on($(selector), 'input', updateWidgetPreview)
    );
    on($('#widget-color'), 'input', event => {
      const text = $('#widget-color-text');
      if (text) text.value = event.currentTarget.value;
      updateWidgetPreview();
    });
    on($('#create-widget-key'), 'click', () => createAPIKey('widget'));
    on($('#create-ingest-key'), 'click', () => createAPIKey('ingest'));
    on($('#refresh-api-keys'), 'click', loadAPIKeys);
    on($('#copy-widget-snippet'), 'click', copyWidgetSnippet);

    on($('#add-builder-department'), 'click', () => {
      builderDepartments.push({ name: '', description: '' });
      renderBuilderDepartments();
    });
    on($('#save-builder-style'), 'click', saveBuilderSettings);
    $$('[data-disable-channel]').forEach(button =>
      on(button, 'click', () => disableBuilderChannel(button.closest('[data-channel-card]')?.dataset.channelCard))
    );
    $$('[data-insert-variable]').forEach(button =>
      on(button, 'click', () => insertBuilderVariable(button.dataset.insertVariable))
    );
    $$('[data-check-channel]').forEach(button =>
      on(button, 'click', () => checkBuilderChannel(button.dataset.checkChannel))
    );
    $$('[data-save-channel]').forEach(button =>
      on(button, 'click', () => saveBuilderChannel(button.dataset.saveChannel))
    );
    on($('#smart-import-submit'), 'click', runSmartImport);
    on($('#sandbox-run'), 'click', runSandboxPreview);

    on($('#open-user-settings'), 'click', openUserSettings);
    on($('#close-user-settings'), 'click', () => $('#user-settings').close());
    on($('#user-settings'), 'close', clearPersonalCodes);
    on($('#refresh-user-links'), 'click', loadPersonalLinks);
    $$('[name="site-theme"]').forEach(input => on(input, 'change', () => setSiteTheme(input.value)));
    $$('[data-personal-channel]').forEach(row => {
      on($('[data-issue-link]', row), 'click', () => changePersonalLink(row, false));
      on($('[data-unlink]', row), 'click', () => changePersonalLink(row, true));
      on($('[data-copy-link]', row), 'click', async () => {
        const input = $('[data-personal-code] input', row);
        try { await navigator.clipboard.writeText(input.value); toast('Команда скопирована. Отправьте её боту в личный чат.'); }
        catch { input.focus(); input.select(); toast('Скопируйте выделенную команду вручную.'); }
      });
    });

    on(document, 'click', event => {
      if (event.target.closest('[data-ticket-back]')) {
        event.preventDefault(); history.pushState({}, '', '/'); leaveTicketRoute();
        $(`.tab[data-tab="${canTab('operator')?'operator':'employee'}"]`)?.click(); return;
      }
      const analyticsLink = event.target.closest('[data-analytics-ticket]');
      if(analyticsLink && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey){event.preventDefault();navigateTicket(analyticsLink.dataset.analyticsTicket);return;}
      const summary = event.target.closest('.ticket-row-summary');
      if (!summary || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
      const id = summary.closest('[data-ticket-id]')?.dataset.ticketId;
      if (id) { event.preventDefault(); navigateTicket(id); }
    });
    on(window, 'popstate', () => {
      if (location.pathname.startsWith('/tickets/')) renderTicketRoute();
      else { leaveTicketRoute(); $(`.tab[data-tab="${canTab('operator')?'operator':'employee'}"]`)?.click(); }
    });

    $$('dialog').forEach(dialog => on(dialog, 'click', event => {
      if (event.target !== dialog) return;
      const box = dialog.getBoundingClientRect();
      if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close();
    }));

    selectSettingsPanel();
    on(document, 'click', event => { if (!event.target.closest('#settings-nav')) $('#settings-nav').open = false; });
    on(document, 'keydown', event => { if (event.key === 'Escape' && $('#settings-nav').open) { $('#settings-nav').open = false; $('#settings-nav summary').focus(); } });

    restoreSession();

    on(document, 'visibilitychange', () => { if (!document.hidden && authReady) checkAPI(); });
    on(window, 'focus', () => { if (authReady) checkAPI(); });

    setInterval(() => {
      if (!workState.running || workState.busy || !hasSession()) return;
      const focusedCard = document.activeElement?.closest('.ticket-card');
      const active = !document.hidden && document.hasFocus() && ($('#operator')?.classList.contains('active') || $('#ticket-detail')?.classList.contains('active')) && focusedCard?.dataset.id === String(workState.id) && Date.now() - workState.activity < 60000;
      trackWork(workState.id, active ? 'heartbeat' : 'pause');
    }, 15000);
    on(window, 'blur', () => { if (workState.running) trackWork(workState.id, 'pause'); });
    on(document, 'visibilitychange', () => { if (document.hidden && workState.running) trackWork(workState.id, 'pause'); });
    startEmployeePolling();
    let heartbeat = 0;
    setInterval(() => {
      if (!document.hidden && authReady) {
        if (++heartbeat % 6 === 0) checkAPI();
        if ($('#operator')?.classList.contains('active')) loadOperator();
        if ($('#analytics')?.classList.contains('active')) loadAnalytics();
      }
    }, 5000);
  }

  window.FlowOpsSupport={departmentsChanged:applyDepartments,organizationChanged:c=>{
    if(c.name)$('#workspace-name-label').textContent=c.name;
    window.FlowOpsAnalytics?.setTimezone(c.timezone);
    if(Array.isArray(c.closure_reasons)){closureReasonLabels={unspecified:'Причина не указана',...Object.fromEntries(c.closure_reasons.map(x=>[x.id,x.label]))};}
  },requestBlob:(path,options={})=>api(path,{...options,responseType:'blob'}),role:()=>state.user?.role,request:api,identity:()=>authReady?state.user?.id:'',generation:()=>sessionGeneration,canRate:()=>state.user?.role==='employee',notify:toast,regulation:regulationMarkup,read:(id,unread)=>{
    const ticket=state.tickets.find(item=>String(item.id)===String(id));if(ticket)ticket.unread_employee_message=unread;setTicketUnread(id,unread);
  },changed:(id,status,messages)=>{
    for(const ticket of [...state.tickets,...state.historyTickets]){if(String(ticket.id)!==String(id))continue;ticket.status=status;const operator=messages.filter(message=>message.role==='operator').at(-1);if(operator)ticket.operator_reply=operator.body;}
    const current=state.tickets.find(ticket=>String(ticket.id)===String(id));if(current&&String(state.currentTicketId)===String(id))renderAnalysis(current);renderHistory();
  }};
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
