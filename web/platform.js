/* Organization settings use the existing authenticated app adapter. */
(() => {
 'use strict';
 const api=()=>window.FlowOpsSupport;
 const $=s=>document.querySelector(s);
 let current=null, epoch=0;
 const escape=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 const numeric=['hourly_rate','baseline_minutes','monthly_infrastructure','implementation','monthly_maintenance'];
 const identity=()=>`${api()?.generation()}:${api()?.identity()}`;
 function pairRow(a,b,kind){return `<div class="org-pair"><input data-key value="${escape(a)}" aria-label="${kind==='reason'?'Код причины':'Код отдела'}" maxlength="100"><input data-value value="${escape(b)}" aria-label="${kind==='reason'?'Название причины':'Стоимость часа'}" ${kind==='rate'?'type="number" min="0" step="0.01"':'maxlength="160"'}><button type="button" class="secondary-button" data-remove-pair aria-label="Удалить строку">Удалить</button></div>`;}
 async function refreshPublic(){
  const owner=identity();if(!api()?.identity())return;
  try{const c=await api().request('/api/v1/organization');if(owner!==identity())return;api().organizationChanged?.(c);}catch{}
 }
 async function openSettings(){
  const version=++epoch,owner=identity();const status=$('#organization-status');status.textContent='Загрузка…';$('#organization-form').hidden=true;
  try{const c=await api().request('/api/v1/settings/organization');if(version!==epoch||owner!==identity())return;current=c;
   for(const field of ['name','description','industry','timezone','contact_email'])$(`[name="org-${field}"]`).value=c[field]||'';
   for(const day of document.querySelectorAll('[name="org-working-day"]'))day.checked=c.calendar.days.includes(Number(day.value));
   $('[name="org-start"]').value=c.calendar.start;$('[name="org-end"]').value=c.calendar.end;$('[name="org-holidays"]').value=c.calendar.holidays.join('\n');
   $('[name="org-currency"]').value=c.economics.currency;
   for(const key of numeric)$(`[name="org-${key}"]`).value=c.economics[key];
   $('[name="org-sla-clock"]').value=c.sla.clock;
   for(const key of ['first_response_minutes','resolution_minutes'])$(`[name="org-${key}"]`).value=c.sla[key];
   $('#org-reasons').innerHTML=c.closure_reasons.map(x=>pairRow(x.id,x.label,'reason')).join('');
   $('#org-rates').innerHTML=Object.entries(c.economics.department_rates||{}).map(([a,b])=>pairRow(a,b,'rate')).join('');
   status.textContent='';$('#organization-form').hidden=false;
  }catch(e){if(version===epoch&&owner===identity())status.textContent=e.message;}
 }
 async function save(event){event.preventDefault();if(!current)return;const button=$('#organization-save');if(button.disabled)return;button.disabled=true;const owner=identity();const c=structuredClone(current),status=$('#organization-status');
  try{
   for(const field of ['name','description','industry','timezone','contact_email'])c[field]=$(`[name="org-${field}"]`).value.trim();
   c.calendar={days:[...document.querySelectorAll('[name="org-working-day"]:checked')].map(x=>Number(x.value)),start:$('[name="org-start"]').value,end:$('[name="org-end"]').value,holidays:$('[name="org-holidays"]').value.split(/\s+/).filter(Boolean)};
   c.economics.currency=$('[name="org-currency"]').value;
   for(const key of numeric)c.economics[key]=Number($(`[name="org-${key}"]`).value);
   c.economics.department_rates={};for(const row of document.querySelectorAll('#org-rates .org-pair')){const key=row.querySelector('[data-key]').value.trim();if(Object.hasOwn(c.economics.department_rates,key))throw Error('Коды отделов не должны повторяться');c.economics.department_rates[key]=Number(row.querySelector('[data-value]').value);}
   c.sla={clock:$('[name="org-sla-clock"]').value,first_response_minutes:Number($('[name="org-first_response_minutes"]').value),resolution_minutes:Number($('[name="org-resolution_minutes"]').value)};
   c.closure_reasons=[...document.querySelectorAll('#org-reasons .org-pair')].map(row=>({id:row.querySelector('[data-key]').value.trim(),label:row.querySelector('[data-value]').value.trim()}));
   status.textContent='Сохранение…';const saved=await api().request('/api/v1/settings/organization',{method:'PUT',body:JSON.stringify(c)});if(owner!==identity())return;current=saved;status.textContent='Настройки сохранены';await refreshPublic();
  }catch(e){if(owner===identity())status.textContent=e.message;}finally{if(owner===identity())button.disabled=false;}
 }
 function clear(){epoch++;current=null;$('#organization-form')?.reset();if($('#organization-form'))$('#organization-form').hidden=true;$('#org-reasons')?.replaceChildren();$('#org-rates')?.replaceChildren();if($('#organization-status'))$('#organization-status').textContent='';}
 function init(){
  $('#organization-form')?.addEventListener('submit',save);
  $('#org-reload')?.addEventListener('click',openSettings);
  $('#organization-form')?.addEventListener('click',event=>event.target.closest('[data-remove-pair]')?.closest('.org-pair').remove());
  $('#org-add-reason')?.addEventListener('click',()=>$('#org-reasons').insertAdjacentHTML('beforeend',pairRow('','','reason')));
  $('#org-add-rate')?.addEventListener('click',()=>$('#org-rates').insertAdjacentHTML('beforeend',pairRow('','0','rate')));
 }
 window.FlowOpsPlatform={openSettings,refreshPublic,clear};
 if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',init);else init();
})();
