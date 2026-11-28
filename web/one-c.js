(() => {
 'use strict';
 const $=s=>document.querySelector(s), form=$('#onec-form'), result=$('#onec-result'), status=$('#onec-status');
 let version=0, busy=false, saved=false, dirty=false;
 const request=(path,options)=>window.FlowOpsSupport.request('/api/v1/integrations/1c'+path,options);
 function clear(){version++;form.reset();result.textContent='';status.textContent='';$('#onec-resources').replaceChildren();$('#onec-secret-hint').textContent='Секрет не сохранён';saved=false;dirty=false;busy=false;controls(false);}
 function controls(value){form.inert=value;$('#onec-read').inert=value;form.querySelectorAll('button').forEach(b=>b.disabled=value);$('#onec-read button').disabled=value;}
 function show(data){
  for(const name of ['mode','base_url','auth','username','health_path']) form.elements[name].value=data.config[name]||({mode:'odata',auth:'basic'}[name]||'');
  form.elements.secret.value='';form.elements.clear_secret.checked=false;
  $('#onec-secret-hint').textContent=data.has_secret?'Секрет сохранён. Оставьте поле пустым, чтобы сохранить его.':'Секрет не сохранён';
  status.textContent=data.server_ready?'':'Для подключения администратор сервера должен разрешить адрес 1С в FLOWOPS_1C_ALLOWED_URLS.';
  saved=data.configured;dirty=false;
 }
 async function refresh(){if(dirty){status.textContent='В форме есть несохранённые изменения.';return;}const v=++version;try{const data=await request('');if(v===version){if(dirty){status.textContent='В форме есть несохранённые изменения.';return;}show(data);}}catch(e){if(v===version)status.textContent=e.message;}}
 async function run(action){if(busy)return;busy=true;controls(true);const v=version;status.textContent='Выполняется…';try{await action(v);}catch(e){if(v===version)status.textContent=e.message;}finally{if(v===version){busy=false;controls(false);}}}
 form.addEventListener('input',()=>{dirty=true;});form.addEventListener('change',()=>{dirty=true;});
 form.addEventListener('submit',e=>{e.preventDefault();run(async v=>{const data=Object.fromEntries(new FormData(form));data.clear_secret=form.elements.clear_secret.checked;const response=await request('',{method:'PUT',body:JSON.stringify(data)});if(v!==version)return;show(response);$('#onec-resources').replaceChildren();result.textContent='';status.textContent='Настройки сохранены. Проверьте соединение.';});});
 $('#onec-check').addEventListener('click',()=>run(async v=>{
  if(!saved||dirty)throw Error('Сначала сохраните настройки подключения.');
  const data=await request('/check',{method:'POST',body:'{}'});if(v!==version)return;
  $('#onec-resources').replaceChildren(...data.resources.map(name=>{const o=document.createElement('option');o.value=name;return o;}));status.textContent=`Соединение установлено${data.resources.length?`. Доступно ресурсов: ${data.resources.length}`:'.'}`;
 }));
 $('#onec-read').addEventListener('submit',e=>{e.preventDefault();run(async v=>{
  if(!saved||dirty)throw Error('Сначала сохраните настройки подключения.');
  result.textContent='';const query=new URLSearchParams(new FormData(e.target));const data=await request('/data?'+query);if(v!==version)return;
  result.textContent=JSON.stringify(data,null,2);status.textContent='Страница данных загружена. Для следующей страницы увеличьте число пропускаемых записей.';
 });});
 window.FlowOpsOneC={refresh,clear};
})();
