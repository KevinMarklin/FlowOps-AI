(() => {
  'use strict';
  const script = document.currentScript || [...document.scripts].reverse().find(item => item.src && new URL(item.src, location.href).pathname.endsWith('/widget.js'));
  if (!script) return;
  const token = script.getAttribute('data-token') || '';
  const scriptURL = new URL(script.src, window.location.href);
  const apiURL = (script.getAttribute('data-api-url') || scriptURL.origin).replace(/\/$/, '');
  const host = document.createElement('div');
  host.setAttribute('data-flowops-widget', '');
  (document.body || document.documentElement).appendChild(host);
  const root = host.attachShadow({ mode: 'open' });
  const style = document.createElement('style');
  style.textContent = `
    :host{all:initial;--flowops-color:#2563eb;font-family:Arial,Helvetica,sans-serif;color:#172033}
    *,*::before,*::after{box-sizing:border-box}
    .launcher{position:fixed;right:24px;bottom:24px;z-index:2147483000;width:60px;height:60px;border:0;border-radius:50%;background:var(--flowops-color);color:#fff;box-shadow:0 8px 28px #0f172a35;cursor:pointer;font-size:26px;display:grid;place-items:center;transition:transform .18s,box-shadow .18s}
    .launcher:hover{transform:translateY(-2px);box-shadow:0 12px 32px #0f172a45}
    .panel{position:fixed;right:24px;bottom:96px;z-index:2147483000;width:min(360px,calc(100vw - 32px));height:min(500px,calc(100vh - 120px));display:none;flex-direction:column;overflow:hidden;border:1px solid #e4e8ef;border-radius:16px;background:#fff;box-shadow:0 18px 60px #0f172a30;font-size:14px}
    .panel.open{display:flex}.header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:16px 18px;background:var(--flowops-color);color:#fff;font-size:16px;font-weight:700}
    .close{border:0;background:transparent;color:#fff;font-size:24px;line-height:1;cursor:pointer}.messages{flex:1;overflow:auto;padding:16px;background:#f8fafc}.bubble{max-width:94%;margin:0 0 12px;padding:11px 13px;border-radius:12px;background:#fff;box-shadow:0 1px 4px #0f172a12;line-height:1.45;white-space:pre-wrap;overflow-wrap:anywhere}.bubble.user{margin-left:auto;background:#eaf1ff}.form{display:grid;gap:9px;padding:12px;border-top:1px solid #e8ebf0;background:#fff}.form input,.form textarea{width:100%;border:1px solid #d8dee8;border-radius:9px;padding:10px 11px;font:inherit;color:#172033;outline:none}.form input:focus,.form textarea:focus{border-color:var(--flowops-color);box-shadow:0 0 0 2px color-mix(in srgb,var(--flowops-color) 18%,transparent)}.form textarea{resize:vertical;min-height:70px;max-height:120px}.send{border:0;border-radius:9px;padding:11px;background:var(--flowops-color);color:#fff;font:700 14px Arial,sans-serif;cursor:pointer}.send:disabled{opacity:.6;cursor:wait}.status{min-height:17px;color:#64748b;font-size:12px}.powered{color:#94a3b8;font-size:10px;text-align:center}
    @media(max-width:480px){.launcher{right:16px;bottom:16px}.panel{right:8px;bottom:86px;width:calc(100vw - 16px);height:min(520px,calc(100vh - 102px))}}
  `;
  root.appendChild(style);
  const panel = document.createElement('section'); panel.className = 'panel'; panel.setAttribute('aria-label','Чат поддержки');
  const header = document.createElement('div'); header.className = 'header';
  const title = document.createElement('span'); title.textContent = 'Онлайн-поддержка';
  const close = document.createElement('button'); close.className = 'close'; close.type = 'button'; close.setAttribute('aria-label','Закрыть чат'); close.textContent = '×'; header.append(title,close);
  const messages = document.createElement('div'); messages.className = 'messages'; messages.setAttribute('aria-live','polite');
  const form = document.createElement('form'); form.className = 'form';
  const contact = document.createElement('input'); contact.type='text'; contact.maxLength=255; contact.autocomplete='email'; contact.placeholder='Email или имя (необязательно)'; contact.setAttribute('aria-label','Email или имя');
  const question = document.createElement('textarea'); question.required=true; question.maxLength=12000; question.placeholder='Напишите ваш вопрос…'; question.setAttribute('aria-label','Ваш вопрос');
  const status = document.createElement('div'); status.className='status'; status.setAttribute('role','status');
  const send = document.createElement('button'); send.className='send'; send.type='submit'; send.textContent='Отправить вопрос';
  const powered = document.createElement('div'); powered.className='powered'; powered.textContent='FlowOps AI';
  form.append(contact,question,status,send,powered); panel.append(header,messages,form);
  const launcher = document.createElement('button'); launcher.type='button'; launcher.className='launcher'; launcher.textContent='✦'; launcher.setAttribute('aria-label','Открыть чат поддержки');
  root.append(panel,launcher);
  const addMessage = (text, kind='') => { const item=document.createElement('div'); item.className=`bubble ${kind}`; item.textContent=text; messages.appendChild(item); messages.scrollTop=messages.scrollHeight; };
  const toggle = open => { panel.classList.toggle('open',open); launcher.setAttribute('aria-expanded',String(open)); if(open) question.focus(); };
  launcher.addEventListener('click',()=>toggle(!panel.classList.contains('open')));
  close.addEventListener('click',()=>toggle(false));
  fetch(`${apiURL}/api/v1/widget/config`,{headers:{'Authorization':`Bearer ${token}`}}).then(response=>response.ok?response.json():null).then(config=>{
    if(!config)return;
    if(typeof config.title==='string' && config.title.trim()) title.textContent=config.title;
    if(typeof config.color==='string' && /^#[0-9a-f]{6}$/i.test(config.color)) host.style.setProperty('--flowops-color',config.color);
    if(typeof config.greeting==='string' && config.greeting.trim()) addMessage(config.greeting);
  }).catch(()=>addMessage('Здравствуйте! Чем мы можем помочь?'));
  form.addEventListener('submit',async event=>{
    event.preventDefault(); const message=question.value.trim();
    if(!message)return;
    if(!token){status.textContent='Не настроен ключ доступа чата.';return;}
    addMessage(message,'user'); question.value=''; send.disabled=true; status.textContent='Отправляем…';
    try{
      const response=await fetch(`${apiURL}/api/v1/ingest`,{method:'POST',headers:{'Content-Type':'application/json','Authorization':`Bearer ${token}`},body:JSON.stringify({channel:'widget',sender:contact.value.trim(),message})});
      const data=await response.json().catch(()=>({}));
      if(!response.ok)throw new Error(data.error||'Не удалось отправить вопрос.');
      addMessage(data.reply_text || (data.auto_replied ? 'Ответ подготовлен.' : `Ваше обращение №${data.ticket_id} передано специалисту.`));
      status.textContent=data.auto_replied?'Ответ из базы знаний':'Заявка создана';
    }catch(error){status.textContent=error.message || 'Ошибка связи с сервером.';addMessage(status.textContent);}
    finally{send.disabled=false;question.focus();}
  });
})();
