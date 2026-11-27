/* One persistent conversation per selected ticket. Credentials stay in app.js. */
(() => {
  'use strict';
  const views = new Map();
  const replyRequests = new Map();
  const api = () => window.FlowOpsSupport;
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const key = () => `message-${crypto.randomUUID()}`;

  function replyRequest(id, body) {
    const previous = replyRequests.get(String(id));
    if (previous?.body === body) return previous;
    const request = {body, client_message_id:key()}; replyRequests.set(String(id), request); return request;
  }

  function mount(operator) {
    const name = operator ? 'operator-dialogue' : 'employee-dialogue';
    let root = document.getElementById(name);
    if (root) return root;
    root = document.createElement('section'); root.id = name; root.className = 'panel ticket-dialogue'; root.hidden = true;
    root.setAttribute('aria-label', operator ? 'Переписка с сотрудником' : 'Переписка обращения');
    root.innerHTML = `<header class="dialogue-header"><div><h2 data-dialogue-title>Переписка</h2><p data-dialogue-status class="muted" role="status"></p></div><button type="button" class="secondary-button" data-dialogue-refresh>Обновить</button></header><button type="button" class="secondary-button" data-dialogue-older hidden>Более ранние сообщения</button><div class="dialogue-messages" data-dialogue-messages></div><p class="form-error" data-dialogue-error role="alert" hidden></p><form data-dialogue-form><label for="${name}-body">${operator?'Ответ сотруднику':'Ваше сообщение'}</label><textarea id="${name}-body" maxlength="6000" rows="3" required placeholder="Напишите сообщение…"></textarea><p class="field-help">Пока обращение открыто, переписка продолжается здесь. Не отправляйте пароли и коды доступа.</p><button type="submit" class="primary-button">Отправить сообщение</button></form><div class="dialogue-decisions" data-dialogue-decisions><button type="button" class="primary-button" data-dialogue-decision="true">Вопрос решён</button><button type="button" class="secondary-button" data-dialogue-decision="false">Не помогло</button></div>`;
    if (operator) document.querySelector('#operator #ticket-list').before(root);
    else document.getElementById('analysis-result').after(root);
    const view = {root,operator,id:0,loading:false,sending:false,messages:new Map(),drafts:new Map(),pending:new Map(),version:0};
    views.set(name,view);
    if(operator){
      const close=document.createElement('button');close.type='button';close.className='secondary-button';close.dataset.dialogueCollapse='';close.textContent='Свернуть переписку';
      close.addEventListener('click',()=>{
        root.hidden=true;
        const card=document.querySelector(`#ticket-list .ticket-card[data-id="${view.id}"]`);
        card?.scrollIntoView({behavior:'auto',block:'start'});
        card?.querySelector('[data-open-dialogue]')?.focus({preventScroll:true});
      });
      root.querySelector('.dialogue-header').append(close);
    }
    root.querySelector('[data-dialogue-refresh]').addEventListener('click',()=>load(view));
    root.querySelector('[data-dialogue-older]').addEventListener('click',()=>load(view,true));
    root.querySelector('[data-dialogue-form]').addEventListener('submit',event=>{event.preventDefault();send(view);});
    root.querySelectorAll('[data-dialogue-decision]').forEach(button=>button.addEventListener('click',()=>decision(view,button)));
    root.addEventListener('click',event=>{
      const button=event.target.closest('[data-rating-score]'); if(button) rate(view,button);
    });
    return root;
  }

  function current(view, version, generation, identity) {
    return view.version===version && api()?.generation()===generation && api()?.identity()===identity;
  }
  function error(view,message) {const box=view.root.querySelector('[data-dialogue-error]');box.textContent=message;box.hidden=!message;}

  function open(id,operator=false) {
    if (!api()?.identity()) return;
    const root=mount(operator), view=views.get(root.id); id=Number(id);
    if(view.id!==id){
      if(view.id)view.drafts.set(view.id,root.querySelector('[data-dialogue-form] textarea').value);
      view.id=id;view.version++;view.messages.clear();view.loading=false;view.sending=false;
      root.querySelector('[data-dialogue-form] button').disabled=false;
      root.querySelectorAll('[data-dialogue-decision]').forEach(button=>button.disabled=false);
      root.querySelector('[data-dialogue-messages]').replaceChildren();
      root.querySelector('textarea').value=view.drafts.get(id)||'';
      root.querySelector('[data-dialogue-title]').textContent=`Переписка #${id}`;
      root.querySelector('[data-dialogue-form]').hidden=true;
      root.querySelector('[data-dialogue-decisions]').hidden=true;
      root.querySelector('[data-dialogue-older]').hidden=true;
      error(view,'');
    }
    root.hidden=false;load(view);
    if(operator)root.scrollIntoView({behavior:'auto',block:'start'});
  }

  async function load(view,older=false) {
    if(!view.id||view.loading||view.sending)return;
    const version=view.version,generation=api().generation(),identity=api().identity();
    view.loading=true;
    const before=older?Math.min(...view.messages.keys()):0;
    try {
      const data=await api().request(`/api/tickets/${view.id}/messages${before?`?before=${before}`:''}`);
      if(!current(view,version,generation,identity))return;
      for(const message of data.messages)view.messages.set(message.id,message);
      render(view,data,older);
      error(view,'');
    }catch(err){if(current(view,version,generation,identity))error(view,err.message);}
    finally{if(current(view,version,generation,identity))view.loading=false;}
  }

  function render(view,data,older=false) {
    const root=view.root,list=root.querySelector('[data-dialogue-messages]');
    const wasAtBottom=list.scrollHeight-list.scrollTop-list.clientHeight<70;
    root.querySelector('[data-dialogue-status]').textContent=data.can_reply?'Диалог открыт. Сообщения с сайта и привязанных ботов находятся в одной истории.':'Обращение закрыто. История и оценки ответов доступны для просмотра.';
    root.querySelector('[data-dialogue-form]').hidden=!data.can_reply;
    root.querySelector('[data-dialogue-decisions]').hidden=!data.can_reply||view.operator||!api().canRate();
    const old=root.querySelector('[data-dialogue-older]');
    if(older||view.messages.size<=100)old.hidden=!data.has_older||view.messages.size>=500;
    const messages=[...view.messages.values()].sort((a,b)=>a.id-b.id);
    while(messages.length>500){const removed=messages.shift();view.messages.delete(removed.id);list.querySelector(`[data-message-id="${removed.id}"]`)?.remove();old.hidden=true;}
    let cursor=list.firstElementChild;
    for(const message of messages){
      let row=list.querySelector(`[data-message-id="${message.id}"]`);
      if(!row){
        row=document.createElement('article');row.dataset.messageId=message.id;row.className=`dialogue-message dialogue-${message.role}`;
        const role={employee:'Сотрудник',assistant:'Помощник · ответ по регламенту',operator:'Специалист',system:'Статус'}[message.role]||'Сообщение';
        const body=message.role==='assistant'?api().regulation(message.body):escape(message.body);
        row.innerHTML=`<header><strong>${role}</strong><time datetime="${escape(message.created_at)}">${escape(new Date(message.created_at).toLocaleString('ru-RU'))}</time></header><div class="dialogue-body">${body}</div>`;
        if(!view.operator&&api().canRate()&&['assistant','operator'].includes(message.role)){
          const rating=document.createElement('details');rating.className='answer-rating';
          rating.innerHTML=`<summary>Оценить ответ${message.rating?` · ${message.rating}/5`:''}</summary><div role="group" aria-label="Оценка ответа от 1 до 5">${[1,2,3,4,5].map(score=>`<button type="button" class="secondary-button" data-rating-score="${score}" aria-pressed="${score===message.rating}" aria-label="Оценка ${score} из 5">${score}</button>`).join('')}</div><label>Комментарий к оценке (необязательно)<textarea rows="2" maxlength="1000">${escape(message.rating_comment||'')}</textarea></label><button type="button" class="secondary-button" data-rating-save data-rating-score="${message.rating||0}" ${message.rating?'':'disabled'}>Сохранить оценку и комментарий</button><p class="field-help" data-rating-status role="status"></p>`;
          row.append(rating);
        }
      }
      if(row!==cursor)list.insertBefore(row,cursor);else cursor=cursor.nextElementSibling;
      if(view.operator&&message.rating){let rating=row.querySelector('[data-employee-rating]');if(!rating){rating=document.createElement('p');rating.dataset.employeeRating='';rating.className='field-help';row.append(rating);}rating.textContent=`Оценка сотрудника: ${message.rating}/5${message.rating_comment?` · ${message.rating_comment}`:''}`;}
    }
    if(!older&&(wasAtBottom||view.sending))list.scrollTop=list.scrollHeight;
    api().changed?.(view.id,data.status,messages);
  }

  async function send(view) {
    if(view.sending)return;
    const input=view.root.querySelector('[data-dialogue-form] textarea'), body=input.value.trim();if(!body)return;
    const version=view.version,generation=api().generation(),identity=api().identity(),id=view.id;
    let request=view.pending.get(id);if(request?.body!==body){request={body,client_message_id:key()};view.pending.set(id,request);}
    view.sending=true;view.root.querySelector('[data-dialogue-form] button').disabled=true;error(view,'');
    try{
      const data=await api().request(`/api/tickets/${id}/messages`,{method:'POST',body:JSON.stringify(request)});
      if(!current(view,version,generation,identity))return;
      if(input.value.trim()===body)input.value='';view.drafts.delete(id);view.pending.delete(id);
      data.messages.forEach(message=>view.messages.set(message.id,message));render(view,data);
      api().notify('Сообщение сохранено');
    }catch(err){if(current(view,version,generation,identity))error(view,`${err.message} Текст сохранён — повторите отправку.`);}
    finally{if(current(view,version,generation,identity)){view.sending=false;view.root.querySelector('[data-dialogue-form] button').disabled=false;}}
  }

  async function decision(view,button){
    if(view.sending)return;view.sending=true;const version=view.version,generation=api().generation(),identity=api().identity();
    const buttons=[...view.root.querySelectorAll('[data-dialogue-decision]')];buttons.forEach(b=>b.disabled=true);
    try{await api().request(`/api/tickets/${view.id}/decision`,{method:'POST',body:JSON.stringify({resolved:button.dataset.dialogueDecision==='true'})});}
    catch(err){if(current(view,version,generation,identity))error(view,err.message);}
    finally{if(current(view,version,generation,identity)){view.sending=false;buttons.forEach(b=>b.disabled=false);await load(view);}}
  }

  async function rate(view,button){
    const row=button.closest('[data-message-id]'),box=button.closest('.answer-rating');
    if(box.dataset.pending)return;box.dataset.pending='true';
    const version=view.version,generation=api().generation(),identity=api().identity();
    const controls=[...box.querySelectorAll('button')];controls.forEach(b=>b.disabled=true);
    try{
      const score=Number(button.dataset.ratingScore),comment=box.querySelector('textarea').value;
      await api().request(`/api/tickets/${view.id}/messages/${row.dataset.messageId}/rating`,{method:'POST',body:JSON.stringify({score,comment})});
      if(!current(view,version,generation,identity))return;
      box.querySelectorAll('[aria-pressed]').forEach(b=>b.setAttribute('aria-pressed',String(Number(b.dataset.ratingScore)===score)));box.querySelector('summary').textContent=`Оценить ответ · ${score}/5`;
      box.querySelector('[data-rating-save]').dataset.ratingScore=String(score);
      box.querySelector('[data-rating-status]').textContent='Оценка сохранена. Она не закрывает обращение.';
    }catch(err){if(current(view,version,generation,identity))box.querySelector('[data-rating-status]').textContent=err.message;}
    finally{if(current(view,version,generation,identity)){delete box.dataset.pending;controls.forEach(b=>b.disabled=false);const save=box.querySelector('[data-rating-save]');save.disabled=!(Number(save.dataset.ratingScore)>0);}}
  }

  function clear(){replyRequests.clear();for(const view of views.values()){view.version++;view.id=0;view.loading=false;view.sending=false;view.messages.clear();view.drafts.clear();view.pending.clear();view.root.remove();}views.clear();}
  window.FlowOpsSupportUI={open,openOperator:id=>open(id,true),clear,replyRequest,replySent:id=>replyRequests.delete(String(id))};
  setInterval(()=>{if(document.hidden||!api()?.identity())return;for(const view of views.values()){if(!view.root.hidden&&view.root.closest('.view')?.classList.contains('active'))load(view);}},5000);
})();
