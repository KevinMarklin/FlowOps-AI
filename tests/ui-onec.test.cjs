const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function harness(request){
 const nodes={};const get=id=>nodes[id]??=( {value:'',textContent:'',checked:false,disabled:false,listeners:{},addEventListener(name,fn){this.listeners[name]=fn},replaceChildren(){this.children=[]}} );
 const form=get('#onec-form');form.elements=Object.fromEntries(['mode','base_url','auth','username','health_path','secret','clear_secret'].map(n=>[n,get(n)]));form.reset=()=>Object.values(form.elements).forEach(e=>{e.value='';e.checked=false});form.querySelectorAll=()=>[get('save'),get('#onec-check')];
 const context={window:{FlowOpsSupport:{request}},document:{querySelector:get,createElement(){return {}}},URLSearchParams,FormData:class{constructor(){return Object.entries(form.elements).filter(([n])=>n!=='clear_secret').map(([n,e])=>[n,e.value])[Symbol.iterator]()}},console};
 vm.runInNewContext(fs.readFileSync(require.resolve('../web/one-c.js'),'utf8'),context);
 return {nodes,get,api:context.window.FlowOpsOneC,form};
}
test('leaving an account clears a typed 1C password and ignores late configuration',async()=>{
 let resolve;const h=harness(()=>new Promise(r=>resolve=r));const pending=h.api.refresh();h.form.elements.secret.value='private';h.get('#onec-result').textContent='private data';h.api.clear();
 resolve({config:{base_url:'https://old.example'},configured:true,has_secret:true,server_ready:true});await pending;
 assert.equal(h.form.elements.secret.value,'');assert.equal(h.form.elements.base_url.value,'');assert.equal(h.get('#onec-result').textContent,'');
 const app=fs.readFileSync(require.resolve('../web/app.js'),'utf8');assert.match(app.slice(app.indexOf('function clearWorkspaceData'),app.indexOf('function selectSettingsPanel')),/FlowOpsOneC\?\.clear\(\)/);
});
test('unsaved settings cannot accidentally check the old connection',async()=>{
 let calls=0;const h=harness(async()=>{calls++;return {config:{mode:'odata',auth:'basic',base_url:'https://saved.example'},configured:true,has_secret:true,server_ready:true}});
 await h.api.refresh();h.form.listeners.input();h.get('#onec-check').listeners.click();await new Promise(r=>setImmediate(r));
 assert.equal(calls,1);assert.match(h.get('#onec-status').textContent,/Сначала сохраните/);
});
