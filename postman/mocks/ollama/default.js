// Local integration fixture: metadata/rendering plus controlled generation, not real Ollama.
const http = require('http');
const profile = {"version": "0.40.5", "digest": "500a1f067a9f782620b40bee6f7b0c89e17ae61f686b92c24933e4ca4b2b8b41", "template": "\n{{- $lastUserIdx := -1 -}}\n{{- range $idx, $msg := .Messages -}}\n{{- if eq $msg.Role \"user\" }}{{ $lastUserIdx = $idx }}{{ end -}}\n{{- end }}\n{{- if or .System .Tools }}<|im_start|>system\n{{ if .System }}\n{{ .System }}\n{{- end }}\n{{- if .Tools }}\n\n# Tools\n\nYou may call one or more functions to assist with the user query.\n\nYou are provided with function signatures within <tools></tools> XML tags:\n<tools>\n{{- range .Tools }}\n{\"type\": \"function\", \"function\": {{ .Function }}}\n{{- end }}\n</tools>\n\nFor each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:\n<tool_call>\n{\"name\": <function-name>, \"arguments\": <args-json-object>}\n</tool_call>\n{{- end -}}\n<|im_end|>\n{{ end }}\n{{- range $i, $_ := .Messages }}\n{{- $last := eq (len (slice $.Messages $i)) 1 -}}\n{{- if eq .Role \"user\" }}<|im_start|>user\n{{ .Content }}\n{{- if and $.IsThinkSet (eq $i $lastUserIdx) }}\n   {{- if $.Think -}}\n      {{- \" \"}}/think\n   {{- else -}}\n      {{- \" \"}}/no_think\n   {{- end -}}\n{{- end }}<|im_end|>\n{{ else if eq .Role \"assistant\" }}<|im_start|>assistant\n{{ if (and $.IsThinkSet (and .Thinking (or $last (gt $i $lastUserIdx)))) -}}\n<think>{{ .Thinking }}</think>\n{{ end -}}\n{{ if .Content }}{{ .Content }}\n{{- else if .ToolCalls }}<tool_call>\n{{ range .ToolCalls }}{\"name\": \"{{ .Function.Name }}\", \"arguments\": {{ .Function.Arguments }}}\n{{ end }}</tool_call>\n{{- end }}{{ if not $last }}<|im_end|>\n{{ end }}\n{{- else if eq .Role \"tool\" }}<|im_start|>user\n<tool_response>\n{{ .Content }}\n</tool_response><|im_end|>\n{{ end }}\n{{- if and (ne .Role \"assistant\") $last }}<|im_start|>assistant\n{{ if and $.IsThinkSet (not $.Think) -}}\n<think>\n\n</think>\n\n{{ end -}}\n{{ end }}\n{{- end }}"};
const send = (res, code, body) => {res.writeHead(code, {'Content-Type':'application/json'}); res.end(JSON.stringify(body));};
const diagnostic14 = {name:'qwen3:14b-q4_K_M',digest:'bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8'};
const render = (s,u,think) => '<|im_start|>system\n\n'+s+'<|im_end|>\n<|im_start|>user\n'+u+(think?' /think':' /no_think')+'<|im_end|>\n<|im_start|>assistant\n'+(think?'':'<think>\n\n</think>\n\n');
function handle(req,res,body) {
 const path = new URL(req.url || '/', 'http://localhost').pathname;
 if(req.method==='GET' && path==='/api/version') return send(res,200,{version:profile.version});
 if(req.method==='GET' && path==='/api/tags') return send(res,200,{models:[{name:'qwen3:8b',digest:profile.digest},diagnostic14]});
 if(req.method==='POST' && path==='/api/show') return send(res,200,{template:profile.template});
 if(req.method==='POST' && path==='/api/chat') {
   if(!body || !Array.isArray(body.messages) || body.messages.length!==2 || typeof body.think!=='boolean' || body.truncate!==false || body.shift!==false) return send(res,400,{error:'fixture_contract_mismatch'});
   if(body._debug_render_only===true) return send(res,200,{_debug_info:{rendered_template:render(body.messages[0].content,body.messages[1].content,body.think)}});
   let input={messages:[{id:'m1'}]}; try {const parsed=JSON.parse(body.messages[1].content);input=parsed.original_input || parsed;} catch {}
   const id=input.messages[0].id;
   const grouping=body.format && typeof body.format==='object' && body.format.properties && body.format.properties.issues;
   const result=grouping
     ? {issues:[{message_ids:[id],context_message_ids:[],request:'Контролируемая группировка для проверки обмена',deadline_message_id:null,staff_reply_ids:[],resolution_confirmation_ids:[],missing_information:[]}]}
     : {topics:[{text:'Проверить документы',status:'awaiting_staff',deadline_text:null,needs_context:false,source_message_ids:[id]}]};
   return send(res,200,{done:true,done_reason:'stop',message:{role:'assistant',content:JSON.stringify(result),...(body.think?{thinking:'PRIVATE_REASONING_SENTINEL'}:{})},total_duration:1000000,load_duration:0,prompt_eval_count:42,eval_count:15});
 }
 send(res,501,{error:'Endpoint not defined'});
}
http.createServer((req,res) => {
 if(req.body!==undefined) {try {return handle(req,res,typeof req.body==='string'?JSON.parse(req.body):req.body);}catch{return send(res,400,{error:'invalid_json'});}}
 let chunks=[];req.on('data',chunk=>chunks.push(chunk));req.on('end',()=>{try{const raw=Buffer.concat(chunks).toString('utf8');handle(req,res,raw?JSON.parse(raw):{});}catch{send(res,400,{error:'invalid_json'});}});
}).listen(process.env.PORT || 4500);
