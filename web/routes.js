/* One URL per workspace section, including individual settings panels. */
(function(root) {
  const paths = {employee:'/requests',operator:'/tickets',analytics:'/analytics',optimization:'/optimization',admin:'/administration',notifications:'/notifications', 'workflow-tasks':'/tasks','process-analytics':'/analytics/processes',reports:'/reports',workflows:'/settings/workflows',assets:'/settings/assets',governance:'/settings/governance',structure:'/settings/structure',platform:'/settings/organization','knowledge-review':'/settings/knowledge',channels:'/settings/api-check','one-c':'/settings/1c'};
  const panels = ['connections','communication','import','preview','widget','api'];
  const entries = Object.entries(paths).map(([tab,path])=>({tab,path})).concat(panels.map(panel=>({tab:'builder',panel,path:`/settings/${panel}`})));
  const api = {entries, path(tab,panel) { return entries.find(x=>x.tab===tab && (tab!=='builder'||x.panel===(panel||'connections')))?.path; }, match(path) { return entries.find(x=>x.path===path.replace(/\/$/,'')); }};
  if(typeof module!=='undefined') module.exports=api;
  else root.FlowOpsRoutes=api;
})(globalThis);
