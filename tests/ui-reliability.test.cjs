const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {runInNewContext}=require('node:vm');
const {webcrypto}=require('node:crypto');
const test=require('node:test');
const assert=require('node:assert/strict');
const source=readFileSync(resolve(__dirname,'../web/app.js'),'utf8');
const start=source.indexOf('  function prepareTicketRequest(');
const end=source.indexOf('\n  async function submitTicket',start);
test('web retries retain one key; changed requests and completed sends get new keys',()=>{
 const context={state:{pendingTicket:null},crypto:webcrypto};
 runInNewContext(source.slice(start,end)+'\nthis.prepare=prepareTicketRequest;',context);
 const a=context.prepare(' user@example.org ',' VPN issue ');
 assert.equal(a,context.prepare('user@example.org','VPN issue'));
 assert.notEqual(a.key,context.prepare('user@example.org','Different issue').key);
 context.state.pendingTicket=null;
 assert.notEqual(a.key,context.prepare('user@example.org','VPN issue').key);
});
function widgetHarness(){
 const elements=[];
 function element(tag){
  const e={tag,children:[],handlers:{},value:'',disabled:false,style:{setProperty(){}},classList:{toggle(){},contains(){return false}},setAttribute(){},append(...items){this.children.push(...items)},appendChild(item){this.children.push(item)},addEventListener(event,fn){this.handlers[event]=fn},focus(){},attachShadow(){return element('shadow')}};
  elements.push(e);return e;
 }
 const script={src:'https://support.example.org/widget.js',getAttribute(name){return name==='data-token'?'synthetic-key':''}};
 let fail=true, complete, calls=[];
 const context={document:{currentScript:script,body:element('body'),createElement:element},window:{location:{href:'https://company.example.org/'}},URL,crypto:webcrypto,fetch:async(url,options)=>{
  if(!options.method)return {ok:true,json:async()=>({})};
  calls.push(JSON.parse(options.body));
  await new Promise(resolve=>{complete=resolve});
  if(fail)throw new Error('network interrupted');
  return {ok:true,json:async()=>({ticket_id:42,auto_replied:false})};
 }};
 runInNewContext(readFileSync(resolve(__dirname,'../web/widget.js'),'utf8'),context);
 return {elements,calls,release(){complete()},succeed(){fail=false},form:elements.find(e=>e.tag==='form'),question:elements.find(e=>e.tag==='textarea'),send:elements.find(e=>e.className==='send')};
}
test('widget preserves failed draft, blocks concurrent submits and retries the same key',async()=>{
 const h=widgetHarness();h.question.value='VPN issue';
 const event={preventDefault(){}};
 const first=h.form.handlers.submit(event);
 await h.form.handlers.submit(event);
 assert.equal(h.calls.length,1);
 h.release();await first;
 assert.equal(h.question.value,'VPN issue');
 h.succeed();const retry=h.form.handlers.submit(event);h.release();await retry;
 assert.equal(h.calls[0].idempotency_key,h.calls[1].idempotency_key);
 assert.equal(h.question.value,'');
 assert.equal(h.elements.filter(e=>e.className==='bubble user').length,1);
});
test('widget retains text typed while an earlier question is being submitted',async()=>{
 const h=widgetHarness();h.succeed();h.question.value='First question';
 const pending=h.form.handlers.submit({preventDefault(){}});
 h.question.value='Next question';h.release();await pending;
 assert.equal(h.question.value,'Next question');
});
test('accepted ticket is not reported as failed when history refresh fails',async()=>{
 const begin=source.indexOf('  async function submitTicket('),finish=source.indexOf('\n  async function loadOperator',begin);
 const messages=[],problem={value:'VPN issue'},button={disabled:false},errorBox={hidden:true},form={setAttribute(){},removeAttribute(){}};
 const context={sessionGeneration:0,state:{pendingTicket:null},previewIntake:async()=>true,resetIntake(){},intakeDetails:{},prepareTicketRequest:()=>({key:'test',body:'{}'}),api:async()=>({id:42}),renderAnalysis(){},loadHistory:async()=>{throw new Error('history unavailable')},toast:text=>messages.push(text),$:(selector)=>({'#problem':problem,'#email':{value:'user@example.org'},'.primary-button':button,'#ticket-error':errorBox}[selector]||null)};
 runInNewContext(source.slice(begin,finish)+'\nthis.submit=submitTicket;',context);
 await context.submit({preventDefault(){},currentTarget:form});
 assert.equal(errorBox.hidden,true);
 assert.equal(problem.value,'');
 assert.equal(button.disabled,false);
 assert.ok(messages.some(text=>text.includes('#42')));
 assert.ok(messages.some(text=>text.includes('пока не обновился')));
});
