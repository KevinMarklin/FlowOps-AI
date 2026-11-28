(() => {
  'use strict';
  const script = document.currentScript || [...document.scripts].reverse().find(item => item.src && new URL(item.src, location.href).pathname.endsWith('/widget.js'));
  if (!script) return;
  const token = script.getAttribute('data-token') || '';
  const scriptURL = new URL(script.src, window.location.href);
  const apiURL = (script.getAttribute('data-api-url') || scriptURL.origin).replace(/\/$/, '');
  const logoURL = new URL('./assets/flowops-mark.png', scriptURL).href;
  function foregroundFor(color) {
    const luminance = value => { const channels = [1,3,5].map(offset => parseInt(value.slice(offset,offset+2),16)/255).map(channel => channel<=0.04045?channel/12.92:((channel+0.055)/1.055)**2.4); return channels[0]*0.2126+channels[1]*0.7152+channels[2]*0.0722; };
    const light = luminance(color);
    return 1.05/(light+0.05)>=(light+0.05)/(luminance('#102a43')+0.05)?'#ffffff':'#102a43';
  }
  const host = document.createElement('div');
  host.setAttribute('data-flowops-widget', '');
  (document.body || document.documentElement).appendChild(host);
  const root = host.attachShadow({ mode: 'open' });
  const style = document.createElement('style');
  style.textContent = `
    :host{all:initial;--flowops-color:#0b5367;--flowops-on-color:#ffffff;font-family:Arial,Helvetica,sans-serif;color:#172033}
    *,*::before,*::after{box-sizing:border-box}
    .launcher{position:fixed;right:24px;bottom:24px;z-index:2147483000;width:60px;height:60px;border:0;border-radius:50%;background:var(--flowops-color);color:var(--flowops-on-color);box-shadow:0 8px 28px #0f172a35;cursor:pointer;font-size:26px;display:grid;place-items:center;transition:transform .18s,box-shadow .18s}
    .launcher:hover{transform:translateY(-2px);box-shadow:0 12px 32px #0f172a45}
    .panel{position:fixed;right:24px;bottom:96px;z-index:2147483000;width:min(360px,calc(100vw - 32px));height:min(500px,calc(100vh - 120px));display:none;flex-direction:column;overflow:auto;border:1px solid #e4e8ef;border-radius:16px;background:#fff;box-shadow:0 18px 60px #0f172a30;font-size:14px}
    .panel.open{display:flex}.header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:16px 18px;background:var(--flowops-color);color:var(--flowops-on-color);font-size:16px;font-weight:700}
    .close{min-width:44px;min-height:44px;flex:0 0 auto;display:grid;place-items:center;border:0;background:transparent;color:var(--flowops-on-color);font-size:24px;line-height:1;cursor:pointer}.messages{flex:1;min-height:80px;overflow:auto;padding:16px;background:#f8fafc}.bubble{max-width:94%;margin:0 0 12px;padding:11px 13px;border-radius:12px;background:#fff;box-shadow:0 1px 4px #0f172a12;line-height:1.45;white-space:pre-wrap;overflow-wrap:anywhere}.bubble.user{margin-left:auto;background:#eaf1ff}.form{display:grid;gap:9px;padding:12px;border-top:1px solid #e8ebf0;background:#fff}.form input,.form textarea{width:100%;border:1px solid #d8dee8;border-radius:9px;padding:10px 11px;font:inherit;color:#172033;outline:none}.form input:focus,.form textarea:focus{border-color:var(--flowops-color);box-shadow:0 0 0 2px color-mix(in srgb,var(--flowops-color) 18%,transparent)}.form label{display:grid;gap:6px;color:#526d82;font-size:12px;line-height:1.5}.form input,.form textarea{min-height:44px}.form textarea{resize:vertical;min-height:70px;max-height:120px}.send{border:0;border-radius:9px;padding:11px;background:var(--flowops-color);color:var(--flowops-on-color);font:700 14px Arial,sans-serif;cursor:pointer}.send:disabled{opacity:.6;cursor:wait}.status{min-height:17px;color:#64748b;font-size:12px}.powered{color:#526d82;font-size:11px;text-align:center}
    .header .title{flex:1;min-width:0;overflow-wrap:anywhere}.brand-logo{object-fit:contain;flex:0 0 auto;background:#fff;border-radius:8px;padding:3px}.launcher .brand-logo{width:38px;height:38px}.header .brand-logo{width:32px;height:32px}.launcher:focus-visible,.send:focus-visible{outline:3px solid #0b5367;outline-offset:3px}.close:focus-visible{outline:2px solid currentColor;outline-offset:-4px}.panel{height:min(500px,calc(100dvh - 120px))}
    @media(prefers-reduced-motion:reduce){.launcher{transition:none}.launcher:hover{transform:none}}
    @media(max-width:480px){.launcher{right:16px;bottom:16px}.panel{right:8px;bottom:86px;width:calc(100vw - 16px);height:min(520px,calc(100vh - 102px));height:min(520px,calc(100dvh - 102px))}}
  `;
  root.appendChild(style);
  const panel = document.createElement('section'); panel.className = 'panel'; panel.id='flowops-support-panel'; panel.setAttribute('aria-label','Чат поддержки');
  const header = document.createElement('div'); header.className = 'header';
  const title = document.createElement('span'); title.className='title'; title.textContent = 'Онлайн-поддержка';
  const close = document.createElement('button'); close.className = 'close'; close.type = 'button'; close.setAttribute('aria-label','Закрыть чат'); close.textContent = '×'; const headerLogo=document.createElement('img');headerLogo.className='brand-logo';headerLogo.src=logoURL;headerLogo.alt='';headerLogo.width=32;headerLogo.height=32;header.append(headerLogo,title,close);
  const messages = document.createElement('div'); messages.className = 'messages'; messages.setAttribute('aria-live','polite');
  const form = document.createElement('form'); form.className = 'form';
  const contact = document.createElement('input'); contact.type='text'; contact.maxLength=255; contact.autocomplete='email'; contact.placeholder='Email или имя (необязательно)'; contact.setAttribute('aria-label','Email или имя');
  const question = document.createElement('textarea'); question.required=true; question.maxLength=12000; question.placeholder='Напишите ваш вопрос…'; question.setAttribute('aria-label','Ваш вопрос');
  const status = document.createElement('div'); status.className='status'; status.setAttribute('role','status');
  const send = document.createElement('button'); send.className='send'; send.type='submit'; send.textContent='Отправить вопрос';
  const powered = document.createElement('div'); powered.className='powered'; powered.textContent='FlowOps AI';
  const contactLabel=document.createElement('label');contactLabel.textContent='Email или имя (необязательно)';contactLabel.append(contact);
  const questionLabel=document.createElement('label');questionLabel.textContent='Ваш вопрос';questionLabel.append(question);
  form.append(contactLabel,questionLabel,status,send,powered); panel.append(header,messages,form);
  const launcher = document.createElement('button'); launcher.type='button'; launcher.className='launcher'; launcher.setAttribute('aria-label','Открыть чат поддержки');launcher.setAttribute('aria-expanded','false');launcher.setAttribute('aria-controls',panel.id);const launcherLogo=document.createElement('img');launcherLogo.className='brand-logo';launcherLogo.src=logoURL;launcherLogo.alt='';launcherLogo.width=38;launcherLogo.height=38;launcher.append(launcherLogo);
  root.append(panel,launcher);
  const addMessage = (text, kind='') => { const item=document.createElement('div'); item.className=`bubble ${kind}`; item.textContent=text; messages.appendChild(item); messages.scrollTop=messages.scrollHeight; };
  const toggle = open => { panel.classList.toggle('open',open); launcher.setAttribute('aria-expanded',String(open)); launcher.setAttribute('aria-label',open?'Закрыть чат поддержки':'Открыть чат поддержки');if(open) question.focus();else launcher.focus(); };
  launcher.addEventListener('click',()=>toggle(!panel.classList.contains('open')));
  close.addEventListener('click',()=>toggle(false));
  root.addEventListener('keydown',event=>{if(event.key==='Escape'&&panel.classList.contains('open')){event.preventDefault();toggle(false);}});
  fetch(`${apiURL}/api/v1/widget/config`,{headers:{'Authorization':`Bearer ${token}`}}).then(response=>response.ok?response.json():null).then(config=>{
    if(!config)return;
    if(typeof config.title==='string' && config.title.trim()) title.textContent=config.title;
    if(typeof config.color==='string' && /^#[0-9a-f]{6}$/i.test(config.color)){host.style.setProperty('--flowops-color',config.color);host.style.setProperty('--flowops-on-color',foregroundFor(config.color));}
    if(typeof config.greeting==='string' && config.greeting.trim()) addMessage(config.greeting);
  }).catch(()=>addMessage('Здравствуйте! Чем мы можем помочь?'));
  let pending = null;
  form.addEventListener('submit',async event=>{
    event.preventDefault(); const message=question.value.trim();
    if(!message || send.disabled)return;
    if(!token){status.textContent='Не настроен ключ доступа чата.';return;}
    const payload={channel:'widget',sender:contact.value.trim(),message};
    const identity=JSON.stringify(payload);
    if(!pending || pending.identity!==identity){const bytes=new Uint8Array(16);crypto.getRandomValues(bytes);pending={identity,key:'widget-'+Array.from(bytes,x=>x.toString(16).padStart(2,'0')).join('')};addMessage(message,'user');}
    const request=pending;
    send.disabled=true; status.textContent='Отправляем…';
    try{
      const response=await fetch(`${apiURL}/api/v1/ingest`,{method:'POST',headers:{'Content-Type':'application/json','Authorization':`Bearer ${token}`},body:JSON.stringify({...payload,idempotency_key:request.key})});
      const data=await response.json().catch(()=>({}));
      if(!response.ok)throw new Error(data.error||'Не удалось отправить вопрос.');
      if(question.value.trim()===message)question.value='';
      if(pending===request)pending=null;
      addMessage(data.reply_text || (data.auto_replied ? 'Ответ подготовлен.' : `Ваше обращение №${data.ticket_id} передано специалисту.`));
      status.textContent=data.auto_replied?'Ответ из базы знаний':'Заявка создана';
    }catch(error){status.textContent=error.message || 'Ошибка связи с сервером.';addMessage(status.textContent);}
    finally{send.disabled=false;if(panel.classList.contains('open'))question.focus();}
  });
})();
