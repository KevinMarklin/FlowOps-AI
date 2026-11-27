const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {runInNewContext}=require('node:vm');
const {webcrypto}=require('node:crypto');
const test=require('node:test');
const assert=require('node:assert/strict');
const source=readFileSync(resolve(__dirname,'../web/app.js'),'utf8');
function harness(){
 const elements=new Map();
 const inputs=[];
 function element(tag){return {tag,children:[],value:'',dataset:{},addEventListener(){},setAttribute(){},focus(){this.focused=true},append(...items){this.children.push(...items);for(const item of items){if(item.id)elements.set('#'+item.id,item);if(item.tag==='input')inputs.push(item)}},replaceChildren(){this.children=[];inputs.length=0}};}
 for(const id of ['intake-panel','intake-fields','intake-title'])elements.set('#'+id,element('div'));
 elements.get('#intake-panel').hidden=true;
 elements.set('#ticket-form .primary-button span',{textContent:''});
 let request;
 const context={crypto:webcrypto,state:{pendingTicket:null},document:{createElement:element},$:(selector)=>elements.get(selector),$$:()=>inputs,escapeHTML:text=>text,api:async(path,options)=>{request=JSON.parse(options.body);return {title:'Помощь в аудитории',ready:false,fields:[{key:'location',label:'Где нужна помощь?',hint:'Корпус и аудитория'},{key:'tried',label:'Что уже проверили?',hint:'Проверки'}]}}};
 const begin=source.indexOf('  let intakeEpoch'),end=source.indexOf('\n  async function submitTicket',begin);
 runInNewContext(source.slice(begin,end)+'\nthis.preview=previewIntake;this.reset=resetIntake;this.prepare=prepareTicketRequest;',context);
 return {context,elements,inputs,get request(){return request}};
}
test('clarification retains completed answers and focuses only a missing field',async()=>{
 const h=harness();assert.equal(await h.context.preview('Проектор'),false);
 assert.equal(h.inputs.length,2);assert.equal(h.elements.get('#intake-panel').hidden,false);
 h.inputs[0].value='Корпус А, аудитория 313';
 await h.context.preview('Проектор');
 assert.equal(h.inputs.length,2);assert.equal(h.inputs[0].value,'Корпус А, аудитория 313');
 assert.equal(h.request.details.location,'Корпус А, аудитория 313');assert.equal(h.inputs[1].focused,true);
});
test('editing or clearing request ignores stale clarification responses',async()=>{
 const h=harness();let finish;
 h.context.api=()=>new Promise(resolve=>finish=resolve);
 const pending=h.context.preview('Проектор');h.context.reset();finish({title:'Old',ready:true,fields:[]});
 assert.equal(await pending,false);assert.equal(h.elements.get('#intake-panel').hidden,true);assert.equal(h.inputs.length,0);
});
test('changed clarification values get a new idempotency key',()=>{
 const h=harness();const a=h.context.prepare('user@example.org','Проектор',{location:'А'});
 assert.equal(a.key,h.context.prepare('user@example.org','Проектор',{location:'А'}).key);
 assert.notEqual(a.key,h.context.prepare('user@example.org','Проектор',{location:'Б'}).key);
});
function submitHarness() {
 const user = {id:'employee-a'};
 const state = {user, pendingTicket:null};
 const controls = [{disabled:false},{disabled:false}];
 const problem = {value:'Проектор'};
 const button = {disabled:false};
 const errorBox = {hidden:true,textContent:''};
 const progress = {hidden:true,textContent:''};
 const form = {setAttribute(){},removeAttribute(){},querySelectorAll(){return controls}};
 const rendered=[],messages=[];
 let resolveRequest,rejectRequest;
 const context = {state,sessionGeneration:0,intakeDetails:{},previewIntake:async()=>true,resetIntake(){},prepareTicketRequest:()=>({key:'synthetic',body:'{}'}),api:()=>new Promise((resolve,reject)=>{resolveRequest=resolve;rejectRequest=reject}),renderAnalysis:ticket=>rendered.push(ticket),loadHistory:async()=>{},toast:text=>messages.push(text),$:(selector)=>({'#problem':problem,'#email':{value:'user@example.org'},'.primary-button':button,'#ticket-error':errorBox,'#ticket-progress':progress}[selector]||null)};
 const begin=source.indexOf('  async function submitTicket('),end=source.indexOf('\n  async function loadOperator',begin);
 runInNewContext(source.slice(begin,end)+'\nthis.submit=submitTicket;',context);
 return {context,state,controls,problem,button,errorBox,progress,form,rendered,messages,resolve(value){resolveRequest(value)},reject(error){rejectRequest(error)}};
}
for(const failed of [false,true]) test(`late ${failed?'failure':'success'} cannot change another account's form`,async()=>{
 const h=submitHarness();
 const pending=h.context.submit({preventDefault(){},currentTarget:h.form});
 await new Promise(resolve=>setImmediate(resolve));
 assert.ok(h.controls.every(field=>field.disabled));
 h.state.user={id:'employee-b'};
 h.problem.value='New account draft';
 h.errorBox.textContent='New account state';
 h.button.disabled=false;
 if(failed)h.reject(new Error('Old connection failed'));else h.resolve({id:42});
 await pending;
 assert.equal(h.problem.value,'New account draft');
 assert.equal(h.errorBox.textContent,'New account state');
 assert.equal(h.errorBox.hidden,true);
 assert.equal(h.button.disabled,false);
 assert.equal(h.rendered.length,0);
 assert.equal(h.messages.length,0);
});
test('ordinary profile refresh keeps an in-flight submission attached to its session',async()=>{
 const h=submitHarness();
 const pending=h.context.submit({preventDefault(){},currentTarget:h.form});
 await new Promise(resolve=>setImmediate(resolve));
 h.state.user={...h.state.user};
 h.resolve({id:42});await pending;
 assert.equal(h.rendered.length,1);
 assert.equal(h.problem.value,'');
 assert.equal(h.button.disabled,false);
 assert.ok(h.controls.every(field=>!field.disabled));
});
test('relogin as the same member still invalidates the old submission',async()=>{
 const h=submitHarness();
 const pending=h.context.submit({preventDefault(){},currentTarget:h.form});
 await new Promise(resolve=>setImmediate(resolve));
 h.context.sessionGeneration++;
 h.problem.value='New login draft';h.button.disabled=false;
 h.resolve({id:42});await pending;
 assert.equal(h.rendered.length,0);
 assert.equal(h.problem.value,'New login draft');
});
