const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const routes=require('../web/routes.js'),source=fs.readFileSync(require.resolve('../web/app.js'),'utf8'),html=fs.readFileSync(require.resolve('../web/index.html'),'utf8');
test('each navigation section and settings panel has a distinct URL',()=>{
 const paths=new Set();for(const x of routes.entries){assert(!paths.has(x.path),x.path);paths.add(x.path);assert.equal(routes.path(x.tab,x.panel),x.path);assert.equal(routes.match(x.path),x);}
 for(const m of html.matchAll(/class="tab[^\"]*"[^>]*data-tab="([^"]+)"([^>]*)/g)){const panel=m[2].match(/data-settings-panel="([^"]+)"/);assert(routes.path(m[1],panel?.[1]),m[1]);}
});
function harness(path,permitted=['admin','operator','builder','employee']){
 let pushed=0,selected='',rendered=0;
 const context={location:{pathname:path,search:'',},window:{FlowOpsRoutes:routes},history:{replaceState(_a,_b,url){context.replaced=url},pushState(){pushed++}},canTab:n=>permitted.includes(n),state:{user:{role:'admin'}},roleTabs:{admin:['admin']},renderTicketRoute(){rendered++},restoringSectionRoute:false};
 context.$=selector=>{let tab=selector.match(/data-tab="([^"]+)"/);const name=tab?.[1]||'admin',panel=selector.match(/data-settings-panel="([^"]+)"/)?.[1];return {dataset:{tab:name,settingsPanel:panel},click(){selected=name+(panel?':'+panel:'');if(!context.restoringSectionRoute)context.history.pushState();}}};
 const start=source.indexOf('  function restoreSectionRoute()'),end=source.indexOf('  let ticketRouteVersion',start);
 vm.runInNewContext(source.slice(start,end)+'\nthis.restore=restoreSectionRoute;',context);
 return {context,run(){context.restore();return {pushed,selected,rendered}}};
}
test('direct loads and back/forward restore the exact settings panel without pushing history',()=>{
 const h=harness('/settings/import');assert.deepEqual(h.run(),{pushed:0,selected:'builder:import',rendered:0});
 h.context.location.pathname='/tickets';assert.equal(h.run().selected,'operator');assert.equal(h.run().pushed,0);
 h.context.location.pathname='/tickets/41';assert.equal(h.run().rendered,1);
});
test('inaccessible routes resolve to a permitted view and preserve intake QR parameter',()=>{
 const h=harness('/settings/import',['admin']);assert.equal(h.run().selected,'admin');assert.equal(h.context.replaced,'/administration');
 const root=harness('/');root.context.location.search='?asset=example';root.run();assert.equal(root.context.replaced,'/administration?asset=example');
});
