const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { runInNewContext } = require('node:vm');

function deferred() { let finish; return { promise: new Promise(resolve => { finish = resolve; }), resolve(value) { finish(value); } }; }
function harness(file, expose, request = async () => ({})) {
  const nodes = new Map();
  const node = key => {
    if (!nodes.has(key)) nodes.set(key, {value:'', textContent:'', innerHTML:'', hidden:false, disabled:false, listeners:{},
      addEventListener(name, action) { this.listeners[name] = action; }, reset() {}, replaceChildren() {}, querySelectorAll() { return []; }});
    return nodes.get(key);
  };
  const context = {window:{FlowOpsSupport:{identity:()=> 'member', generation:()=>1, request, notify(){}}},
    document:{readyState:'loading', querySelector:node, querySelectorAll:()=>[], addEventListener(){}},
    structuredClone, URLSearchParams, setTimeout, clearTimeout, Event:class {constructor(type,options){this.type=type;Object.assign(this,options);}}};
  let source = readFileSync(resolve(__dirname,'../web/'+file),'utf8');
  source = source.replace(/\}\)\(\);\s*$/, expose + '\n})();');
  runInNewContext(source,context);
  return {api:context.window.__polishTest, node, context};
}

test('returning to a workflow, asset or report retains unsaved editor values', async () => {
  for (const view of ['workflows','assets','reports']) {
    let requests=0;
    const h=harness('operations.js','window.__polishTest={refresh,dirtyViews};',async()=>{requests++;return {};});
    h.node('#asset-description').value='Незавершённое описание';
    h.api.dirtyViews.add(view);
    await h.api.refresh(view);
    assert.equal(requests,0);
    assert.equal(h.node('#asset-description').value,'Незавершённое описание');
    assert.match(h.node(`[data-operation-status="${view}"]`).textContent,/несохранённые/);
  }
});

test('an in-flight editor refresh does not overwrite text entered while it was loading', async () => {
  const gate=deferred();
  const h=harness('operations.js','window.__polishTest={refresh,dirtyViews};',path=>path.includes('directory')?Promise.resolve({members:[]}):gate.promise);
  h.node('#workflow-editor').reset=()=>{throw Error('overwrote the pending draft');};
  const pending=h.api.refresh('workflows');
  h.api.dirtyViews.add('workflows');
  gate.resolve([{id:'saved',name:'Saved workflow'}]);
  await pending;
});

test('refreshing a clean editor retains the selected saved workflow', async () => {
  const h=harness('operations.js',`editingWorkflow={id:'saved'};editWorkflow=item=>{window.__selectedWorkflow=item;};window.__polishTest={refresh};`,
    async path=>path.includes('directory')?{members:[]}:[{id:'saved',name:'Saved workflow'},{id:'other',name:'Other'}]);
  await h.api.refresh('workflows');
  assert.equal(h.context.window.__selectedWorkflow.id,'saved');
});

test('organization settings keep unsaved changes when the section is reopened', async () => {
  let requests=0;
  const h=harness('platform.js','current={revision:1};dirty=true;window.__polishTest={openSettings};',async()=>{requests++;return {};});
  h.node('[name="org-name"]').value='Новое название';
  await h.api.openSettings();
  assert.equal(requests,0);
  assert.equal(h.node('[name="org-name"]').value,'Новое название');
  assert.equal(h.node('#organization-form').hidden,false);
  assert.match(h.node('#organization-status').textContent,/несохранённые/);
});

test('hidden workflow controls cannot block native validation after changing step type', () => {
  const source=readFileSync(resolve(__dirname,'../web/operations.js'),'utf8');
  const start=source.indexOf('const visibility=()=>');
  const end=source.indexOf(';row.querySelector(\'[data-node="kind"]\').onchange',start);
  assert(start>=0&&end>start);
  const url={disabled:false,value:'incomplete URL'};
  const label={dataset:{for:'http'},hidden:false,querySelectorAll:()=>[url]};
  const kind={value:'task'}, conditions={hidden:false};
  const context={row:{querySelector:s=>s==='[data-node="kind"]'?kind:conditions,querySelectorAll:()=>[label]}};
  runInNewContext(source.slice(start,end)+';this.run=visibility;',context);
  context.run();assert.equal(label.hidden,true);assert.equal(url.disabled,true);
  kind.value='http';context.run();assert.equal(label.hidden,false);assert.equal(url.disabled,false);
});

test('inserting a Copilot draft retains the specialist response and does not duplicate a second insertion', () => {
  const h=harness('copilot.js','window.__polishTest={insertDraft};');
  const reply={value:'Проверил доступ, нужна дополнительная настройка.',events:[],dispatchEvent(event){this.events.push(event.type);},focus(){this.focused=true;}};
  h.api.insertDraft(reply,'Черновик следующих шагов.');
  assert.equal(reply.value,'Проверил доступ, нужна дополнительная настройка.\n\nЧерновик следующих шагов.');
  h.api.insertDraft(reply,'Черновик следующих шагов.');
  assert.equal(reply.value.match(/Черновик следующих шагов/g).length,1);
  assert.equal(reply.focused,true);assert.equal(reply.events[0],'input');
});

test('refreshing the same intake form retains entered values and the actual file control', () => {
  const h=harness('intake-custom-fields.js',`definition={fields:[{id:'room',type:'text'},{id:'file',type:'attachment'}]};renderedForm='';window.__polishTest={rememberValues,restoreValues,switchForm(){selectedForm='other';}};`);
  const text={value:'Комната 12'},file={value:'chosen.pdf',files:[{name:'chosen.pdf'}]};
  const row=(id,control)=>({dataset:{customIntake:id},querySelector:()=>control});
  h.node('#custom-intake').querySelectorAll=selector=>selector==='[data-custom-intake]'?[row('room',text),row('file',file)]:[];
  const previous=h.api.rememberValues();
  const nextText={value:''},nextFile={replaceWith(control){this.replaced=control;}};
  const nextRoot={querySelectorAll:selector=>selector==='[data-custom-intake]'?[row('room',nextText),row('file',nextFile)]:[]};
  h.api.restoreValues(nextRoot,previous);
  assert.equal(nextText.value,'Комната 12');assert.equal(nextFile.replaced,file);
  h.api.switchForm();nextText.value='';delete nextFile.replaced;
  h.api.restoreValues(nextRoot,previous);
  assert.equal(nextText.value,'');assert.equal(nextFile.replaced,undefined);
});

test('a file read failure gives an actionable intake error', async () => {
  const h=harness('intake-custom-fields.js',`definition={revision:1,fields:[{id:'file',type:'attachment'}]};window.__polishTest={payload};`);
  h.context.FileReader=class {readAsDataURL(){this.onerror();}};
  h.context.document.querySelectorAll=()=>[{dataset:{customIntake:'file'},hidden:false,querySelector:()=>({files:[{name:'test.txt',size:20}]})}];
  await assert.rejects(h.api.payload(),/Не удалось прочитать файл/);
});

test('intake rejects combined attachments above the server total-size limit', async () => {
  const h=harness('intake-custom-fields.js',`definition={revision:1,fields:[{id:'one',type:'attachment'},{id:'two',type:'attachment'}]};window.__polishTest={payload};`);
  h.context.FileReader=class {readAsDataURL(){this.result='data:text/plain;base64,eA==';this.onload();}};
  h.context.document.querySelectorAll=()=>['one','two'].map(id=>({dataset:{customIntake:id},hidden:false,querySelector:()=>({files:[{name:id+'.txt',size:3*1024*1024}]})}));
  await assert.rejects(h.api.payload(),/Суммарный размер вложений/);
});

function widgetHarness(config = {}) {
  const elements=[];let focused;
  function element(tag) {
    const classes=new Set();
    const node={tag,children:[],handlers:{},attributes:{},value:'',disabled:false,style:{values:{},setProperty(key,value){this.values[key]=value;}},
      classList:{contains:key=>classes.has(key),toggle(key,force){const value=force??!classes.has(key);if(value)classes.add(key);else classes.delete(key);return value;}},
      setAttribute(key,value){this.attributes[key]=value;},append(...children){this.children.push(...children);},appendChild(child){this.children.push(child);},
      addEventListener(name,action){this.handlers[name]=action;},focus(){focused=this;},attachShadow(){return element('shadow');}};
    elements.push(node);return node;
  }
  const gate=deferred();
  const context={document:{currentScript:{src:'https://support.example.org/widget.js?v=1',getAttribute:name=>name==='data-token'?'test-key':''},body:element('body'),createElement:element},
    window:{location:{href:'https://customer.example.org/'}},URL,crypto:require('node:crypto').webcrypto,
    fetch:async(_url,options)=>options.method?gate.promise:{ok:true,json:async()=>config}};
  runInNewContext(readFileSync(resolve(__dirname,'../web/widget.js'),'utf8'),context);
  return {elements,gate,get focused(){return focused;},get(tag){return elements.find(e=>e.tag===tag);},class(name){return elements.find(e=>e.className===name);}};
}

test('widget branding uses the bundled logo and its launcher exposes open state', () => {
  const h=widgetHarness();
  const launcher=h.class('launcher'),panel=h.class('panel');
  assert.equal(launcher.attributes['aria-expanded'],'false');
  assert.equal(launcher.attributes['aria-controls'],panel.id);
  const logos=h.elements.filter(e=>e.tag==='img');assert.equal(logos.length,2);
  for(const logo of logos){assert.equal(logo.src,'https://support.example.org/assets/flowops-mark.png');assert.equal(logo.alt,'');}
  launcher.handlers.click();assert.equal(launcher.attributes['aria-expanded'],'true');assert.equal(h.focused,h.get('textarea'));
});

test('Escape and the widget close button restore focus to the launcher', () => {
  const h=widgetHarness(),launcher=h.class('launcher');
  launcher.handlers.click();let prevented=false;
  h.get('shadow').handlers.keydown({key:'Escape',preventDefault(){prevented=true;}});
  assert.equal(prevented,true);assert.equal(launcher.attributes['aria-expanded'],'false');assert.equal(h.focused,launcher);
  launcher.handlers.click();h.class('close').handlers.click();assert.equal(h.focused,launcher);
});

test('a completed widget request cannot move focus into a closed chat', async () => {
  const h=widgetHarness();h.class('launcher').handlers.click();h.get('textarea').value='Test request';
  const pending=h.get('form').handlers.submit({preventDefault(){}});h.class('close').handlers.click();
  h.gate.resolve({ok:true,json:async()=>({ticket_id:42})});await pending;
  assert.equal(h.focused,h.class('launcher'));assert.equal(h.class('send').disabled,false);
});

test('the widget keeps a configured color and selects a readable foreground', async () => {
  for(const [color,foreground]of [['#ffffff','#102a43'],['#000000','#ffffff']]){
    const h=widgetHarness({color});await new Promise(resolve=>setImmediate(resolve));
    const host=h.elements.find(e=>Object.hasOwn(e.attributes,'data-flowops-widget'));
    assert.equal(host.style.values['--flowops-color'],color);assert.equal(host.style.values['--flowops-on-color'],foreground);
  }
});

test('governance navigation cannot discard unsaved configuration from another section', async () => {
  let requests=0;
  const h=harness('governance.js','dirtySections.add("ai");window.__polishTest={refresh};',async()=>{requests++;return {};});
  h.node('#governance-ai-limit').value='125';
  await h.api.refresh();
  assert.equal(requests,0);assert.equal(h.node('#governance-ai-limit').value,'125');
  assert.match(h.node('#governance-status').textContent,/несохранённые/);
});

test('saving a governance form preserves unsaved AI configuration', async () => {
  const h=harness('governance.js',`form={revision:1,fields:[],forms:[]};dirtySections.add('form');dirtySections.add('ai');window.__polishTest={init,dirtySections};`,
    async()=>({revision:2,fields:[],forms:[]}));
  for(const id of ['#governance-fields',...['categories','types','tags','statuses','priorities'].map(kind=>'#governance-taxonomy-'+kind)])h.node(id).children=[];
  h.api.init();h.node('#governance-ai-limit').value='125';
  await h.node('#governance-save-form').onclick({target:h.node('#governance-save-form')});
  assert.equal(h.api.dirtySections.has('form'),false);assert.equal(h.api.dirtySections.has('ai'),true);
  assert.equal(h.node('#governance-ai-limit').value,'125');
  assert.match(h.node('#governance-status').textContent,/других разделах/);
});
