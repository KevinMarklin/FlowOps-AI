const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {runInNewContext}=require('node:vm');
const {webcrypto}=require('node:crypto');
const test=require('node:test');
const assert=require('node:assert/strict');

function dialogue(){
 const context={window:{},crypto:webcrypto,setInterval(){}};
 runInNewContext(readFileSync(resolve(__dirname,'../web/dialogue.js'),'utf8'),context);
 return context.window.FlowOpsSupportUI;
}
test('operator retries one message without duplicating it, while ticket drafts stay isolated',()=>{
 const ui=dialogue();const first=ui.replyRequest(1,'Answer');
 assert.equal(ui.replyRequest(1,'Answer').client_message_id,first.client_message_id);
 assert.notEqual(ui.replyRequest(2,'Answer').client_message_id,first.client_message_id);
 assert.notEqual(ui.replyRequest(1,'Changed answer').client_message_id,first.client_message_id);
});
test('logout clears pending reply identifiers before another employee signs in',()=>{
 const ui=dialogue();const first=ui.replyRequest(1,'Answer');ui.clear();
 assert.notEqual(ui.replyRequest(1,'Answer').client_message_id,first.client_message_id);
});
