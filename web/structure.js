(() => {
  'use strict';
  const $ = selector => document.querySelector(selector);
  const api = () => window.FlowOpsSupport;
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const identity = () => `${api()?.generation()}:${api()?.identity()}`;
  let units = [], members = [], current = null, epoch = 0;
  const form = $('#unit-form');
  function path(unit) {
    const names = [], seen = new Set();
    while (unit && !seen.has(unit.id)) { seen.add(unit.id); names.unshift(unit.name); unit = units.find(u => u.id === unit.parent_id); }
    return names.join(' / ');
  }
  function membership(value = {}) {
    const row = document.createElement('div'); row.className = 'unit-member';
    row.innerHTML = `<select aria-label="Участник" data-member><option value="">Выберите участника</option>${members.filter(m => m.active || m.id === value.member_id).map(m => `<option value="${esc(m.id)}" ${m.id === value.member_id ? 'selected' : ''}>${esc(m.name || m.email)} · ${esc(m.email)}</option>`).join('')}</select><select aria-label="Отношение к подразделению" data-relation>${Object.entries({member:'Участник',manager:'Руководитель',deputy:'Заместитель',responsible:'Ответственный'}).map(([id,label]) => `<option value="${id}" ${id === value.relation ? 'selected' : ''}>${label}</option>`).join('')}</select><button type="button" class="secondary-button" data-remove>Убрать</button>`;
    row.querySelector('[data-remove]').onclick = () => row.remove(); $('#unit-members').append(row);
  }
  function edit(unit) {
    current = unit || null; form.reset(); form.hidden = false;
    $('#unit-title').textContent = unit ? 'Изменить подразделение' : 'Новое подразделение';
    for (const name of ['name','kind','routing_code']) form.elements[name].value = unit?.[name] || '';
    form.elements.active.checked = unit?.active ?? true;
    form.elements.routing_code.readOnly = Boolean(unit?.routing_code);
    const descendants = new Set(unit ? [unit.id] : []);
    let changed = true;
    while (changed) { changed = false; for (const u of units) if (descendants.has(u.parent_id) && !descendants.has(u.id)) { descendants.add(u.id); changed = true; } }
    form.elements.parent_id.innerHTML = '<option value="">Корневое подразделение</option>' + units.filter(u => !descendants.has(u.id)).map(u => `<option value="${esc(u.id)}" ${u.id === unit?.parent_id ? 'selected' : ''}>${esc(path(u))}${u.active ? '' : ' (отключено)'}</option>`).join('');
    $('#unit-members').replaceChildren(); (unit?.members || []).forEach(membership);
    form.scrollIntoView({block:'start'}); form.elements.name.focus();
  }
  function render() {
    $('#structure-list').innerHTML = units.length ? units.map(u => `<button type="button" class="unit-row secondary-button" data-unit="${esc(u.id)}"><strong>${esc(path(u))}</strong><span>${esc(u.kind)} · ${u.active ? 'Активно' : 'Отключено'}${u.routing_code ? ` · ${esc(u.routing_code)}` : ''}</span></button>`).join('') : '<p>Подразделений пока нет.</p>';
  }
  async function refresh() {
    const token = ++epoch, owner = identity(); $('#structure-status').textContent = 'Загрузка…';
    try {
      const result = await Promise.all([api().request('/api/v1/organization/units'), api().request('/api/v1/members')]);
      if (token !== epoch || owner !== identity()) return;
      [units,members] = result; render(); $('#structure-status').textContent = `Подразделений: ${units.length}`;
    } catch (error) { if (token === epoch && owner === identity()) $('#structure-status').textContent = error.message; }
  }
  $('#structure-list').onclick = event => { const button = event.target.closest('[data-unit]'); if (button) edit(units.find(u => u.id === button.dataset.unit)); };
  $('#unit-new').onclick = () => edit(null);
  $('#structure-refresh').onclick = refresh;
  $('#unit-add-member').onclick = () => membership();
  $('#unit-cancel').onclick = () => { form.hidden = true; current = null; };
  form.onsubmit = async event => {
    event.preventDefault(); const owner = identity(), token = epoch, button = form.querySelector('[type=submit]'); button.disabled = true;
    const payload = {revision:current?.revision || 0, active:form.elements.active.checked, members:[...$('#unit-members').children].map(row => ({member_id:row.querySelector('[data-member]').value,relation:row.querySelector('[data-relation]').value}))};
    for (const name of ['name','kind','routing_code','parent_id']) payload[name] = form.elements[name].value.trim();
    try {
      await api().request('/api/v1/organization/units' + (current ? '/' + encodeURIComponent(current.id) : ''), {method:current ? 'PUT' : 'POST',body:JSON.stringify(payload)});
      if (owner !== identity() || token !== epoch) return;
      form.hidden = true; current = null; await refresh();
      const me = await api().request('/api/v1/auth/me'); if (owner === identity()) api().departmentsChanged(me.departments);
    } catch (error) { if (owner === identity()) $('#structure-status').textContent = error.message; }
    finally { button.disabled = false; }
  };
  window.FlowOpsStructure = {refresh,clear(){epoch++; units=[]; members=[]; current=null; form.hidden=true; $('#structure-list').replaceChildren(); $('#structure-status').textContent='';}};
})();

