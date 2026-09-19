const AGENT_LABEL = {claude:"Claude Code", opencode:"OpenCode", zcode:"ZCode", codex:"Codex"};
let range = "7d";
let agent = "";
let device = "";
let chartDays = [];
let chartBucket = "day";
let deviceNames = {};   // device_id -> 显示名

function fmt(n){ return (n??0).toLocaleString("en-US"); }
function fmtCost(c){ return "¥" + (Number(c)||0).toFixed(2); }
function esc(s){ return (s??"").toString().replace(/[&<>"']/g, m=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[m])); }
function shortModel(m){ return m? m.split("/").pop() : "—"; }
function devLabel(id){ return deviceNames[id] || id || "—"; }

function rangeDates(r){
  const now = new Date();
  const local = d => new Date(d.getTime() - d.getTimezoneOffset()*60000).toISOString().slice(0,10);
  if(r==="today") return [local(now), local(now)];
  if(r==="yesterday"){ const y=new Date(now); y.setDate(y.getDate()-1); return [local(y), local(y)]; }
  if(r==="7d"){ const s=new Date(now); s.setDate(s.getDate()-6); return [local(s), local(now)]; }
  if(r==="30d"){ const s=new Date(now); s.setDate(s.getDate()-29); return [local(s), local(now)]; }
  const f=document.getElementById("from").value, t=document.getElementById("to").value;
  if(!f||!t) return null;
  return [f,t];
}

async function jget(url){
  const res = await fetch(url);
  if(!res.ok) throw new Error(url+" -> "+res.status);
  return res.json();
}

/* ---- SVG 趋势图 ---- */
const BUCKET_TITLE = {hour: "每小时 Token 趋势", day: "每日 Token 趋势", month: "每月 Token 趋势"};
function bucketLabel(day, bucket){
  if(bucket === "hour") return day.slice(11, 13) + ":00";
  if(bucket === "month") return day;
  return day.slice(5);
}
function bucketDayText(day, bucket){ return bucket === "hour" ? day + ":00" : day; }
const C_TOTAL="#168879", C_INPUT="#69bda2", C_OUTPUT="#d69a47", C_CACHE="#8898c1";
const SERIES = [
  {key:"total",  name:"总计", color:C_TOTAL, get:d=>d.total_tokens},
  {key:"input",  name:"输入", color:C_INPUT,  get:d=>d.input_tokens},
  {key:"output", name:"输出", color:C_OUTPUT, get:d=>d.output_tokens},
  {key:"cache",  name:"缓存", color:C_CACHE,  get:d=>d.cache_read_tokens + d.cache_write_tokens},
];

function lineChart(days, bucket){
  const W=800, H=260, PL=59, PR=25, PT=18, PB=30;
  if(!days.length) return '<div class="muted" style="padding:34px;text-align:center">区间内暂无数据</div>';
  const yMax = Math.max(1, ...days.flatMap(d=>SERIES.map(s=>s.get(d))));
  const iw = W-PL-PR, ih = H-PT-PB;
  const x = i => days.length===1 ? PL+iw/2 : PL + i*iw/(days.length-1);
  const y = v => PT + ih - (v/yMax)*ih;
  const fmtShort = v => v>=1e6 ? (v/1e6).toFixed(1)+"M" : v>=1e3 ? (v/1e3).toFixed(0)+"K" : String(v);
  let g = `<defs><linearGradient id="areaTotal" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="${C_TOTAL}" stop-opacity=".14"/><stop offset="100%" stop-color="${C_TOTAL}" stop-opacity="0"/></linearGradient></defs>`;
  for(let i=0;i<=4;i++){
    const vy = PT + ih*i/4, val = yMax*(1-i/4);
    g += `<line x1="${PL}" y1="${vy}" x2="${W-PR}" y2="${vy}" stroke="#edf0f7"/><text x="${PL-8}" y="${vy+4}" text-anchor="end" font-size="11" fill="#9aa3b2">${fmtShort(val)}</text>`;
  }
  const labelEvery = Math.max(1, Math.ceil(days.length/7));
  days.forEach((d,i)=>{
    if((i%labelEvery===0 && i<days.length-1-Math.floor(labelEvery/2)) || i===days.length-1)
      g += `<text x="${x(i)}" y="${H-8}" text-anchor="middle" font-size="11" fill="#9aa3b2">${bucketLabel(d.day, bucket)}</text>`;
  });
  const areaPts = days.map((d,i)=>`${x(i)},${y(SERIES[0].get(d))}`).join(" L ");
  g += `<path d="M ${areaPts} L ${x(days.length-1)},${PT+ih} L ${x(0)},${PT+ih} Z" fill="url(#areaTotal)"/>`;
  let paths = "";
  for(const s of SERIES){
    const pts = days.map((d,i)=>`${x(i)},${y(s.get(d))}`).join(" ");
    paths += `<polyline points="${pts}" fill="none" stroke="${s.color}" stroke-width="2.2" stroke-linejoin="round" stroke-linecap="round"/>`;
    days.forEach((d,i)=>{ const v = s.get(d); if(v>0) paths += `<circle class="pt" data-i="${i}" cx="${x(i)}" cy="${y(v)}" r="3.5" fill="${s.color}" stroke="#ffffff" stroke-width="1.5"/>`; });
  }
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label="${BUCKET_TITLE[bucket] || BUCKET_TITLE.day}"><rect x="0" y="0" width="${W}" height="${H}" fill="transparent"/>${g}${paths}</svg>`;
}

function setupTooltip(){
  const chart = document.getElementById("chart");
  const tip = document.getElementById("tip");
  chart.onmousemove = e => {
    const c = e.target.closest("circle.pt");
    if(!c){ tip.style.display = "none"; return; }
    const d = chartDays[Number(c.dataset.i)];
    if(!d){ tip.style.display = "none"; return; }
    const cost = Number(d.cost)||0;
    tip.innerHTML = `<div class="t-day">${bucketDayText(d.day, chartBucket)}</div>` +
      SERIES.map(s=>`<div class="t-row"><i style="background:${s.color}"></i>${s.name}<b>${fmt(s.get(d))}</b></div>`).join("") +
      (cost>0 ? `<div class="t-row">费用<b>¥${cost.toFixed(2)}</b></div>` : "");
    tip.style.display = "block";
    const rect = chart.getBoundingClientRect();
    let tx = e.clientX - rect.left + 16, ty = e.clientY - rect.top - 10;
    const tw = tip.offsetWidth, th = tip.offsetHeight;
    if(tx + tw > rect.width - 4) tx = e.clientX - rect.left - tw - 16;
    if(ty + th > rect.height - 4) ty = rect.height - th - 4;
    if(ty < 0) ty = 4;
    tip.style.left = tx + "px"; tip.style.top = ty + "px";
  };
  chart.onmouseleave = () => { tip.style.display = "none"; };
}

const AGENT_COLORS = {zcode:"#278f79",opencode:"#91a4cb",claude:"#dfb37b",codex:"#7fb3d5"};
let modelsData = [];
let refreshId = 0;
let lastUpdated = null;
function compact(n){return new Intl.NumberFormat("en-US",{notation:"compact",maximumFractionDigits:2}).format(Number(n)||0);}
function rowHTML(key, d, isAgent=false){
  const cache = (d.cache_read_tokens||0) + (d.cache_write_tokens||0);
  const mark=isAgent?`<span class="entity-icon ${esc(d.key)}">${esc((key||"?")[0])}</span>`:"";
  return `<tr><td><div class="entity">${mark}<span class="${isAgent?"":"model-name"}" title="${esc(d.key)}">${esc(key)}</span></div></td><td>${fmt(d.input_tokens)}</td><td>${fmt(d.output_tokens)}</td><td>${fmt(cache)}</td><td><b>${fmt(d.total_tokens)}</b></td><td>${fmtCost(d.cost)}</td><td>${fmt(d.requests)}</td></tr>`;
}
function renderModels(){
  const search=document.getElementById("modelSearch").value.trim().toLowerCase();
  const rows=modelsData.filter(m=>(m.key||"").toLowerCase().includes(search));
  document.getElementById("modelCount").textContent=search?`${rows.length} / ${modelsData.length}`:modelsData.length;
  document.querySelector("#models tbody").innerHTML=rows.map(m=>rowHTML(shortModel(m.key),m)).join("")||`<tr><td colspan="7" class="empty-state">${search?"没有匹配的模型，试试其他关键词":"所选区间暂无模型用量"}</td></tr>`;
}
function renderDistribution(rows){
  const total=rows.reduce((sum,r)=>sum+(Number(r.total_tokens)||0),0);
  const box=document.getElementById("distribution");
  if(!total){box.innerHTML='<div class="empty-state">所选区间暂无 Token 用量</div>';return;}
  box.innerHTML=`<div class="distribution-total"><strong>${compact(total)}</strong><span>TOKENS</span></div><div class="stacked-bar" aria-hidden="true">${rows.filter(r=>r.total_tokens>0).map(r=>`<span style="width:${r.total_tokens/total*100}%;background:${AGENT_COLORS[r.key]||"#a0afaa"}"></span>`).join("")}</div><div class="agent-distribution">${rows.map(r=>`<div class="agent-share"><div class="agent-share-head"><span class="agent-key"><i class="dot" style="background:${AGENT_COLORS[r.key]||"#a0afaa"}"></i>${esc(AGENT_LABEL[r.key]||r.key)}</span><b>${(r.total_tokens/total*100).toFixed(1)}%</b></div><div class="agent-share-sub">${fmt(r.total_tokens)} Tokens</div></div>`).join("")}</div>`;
}
function syncRangeButtons(){
  document.querySelectorAll(".btn.range").forEach(b=>{b.classList.toggle("active",b.dataset.r===range);b.setAttribute("aria-pressed",String(b.dataset.r===range));});
}

/* ---- 设备表 ---- */
function relTime(s){
  if(!s) return "从未";
  const t = new Date(s.replace(" ","T"));
  if(isNaN(t)) return s;
  const diff = (Date.now()-t.getTime())/1000;
  if(diff<60) return "刚刚";
  if(diff<3600) return Math.floor(diff/60)+" 分钟前";
  if(diff<86400) return Math.floor(diff/3600)+" 小时前";
  return t.toLocaleString("zh-CN",{hour12:false});
}
function agentsSummary(list){
  if(!Array.isArray(list)||!list.length) return '<span class="muted">未配置</span>';
  return list.map(a=>`<span class="pill ${a.enabled===false?"off":""}" title="${esc((a.paths||[]).join("\n"))}">${esc(AGENT_LABEL[a.agent]||a.agent)}${a.enabled===false?" · 停用":""}</span>`).join(" ");
}
function renderDevices(devices, statRows){
  const stat = Object.fromEntries((statRows||[]).map(r=>[r.key,r]));
  document.getElementById("deviceCount").textContent = devices.length;
  const tb = document.querySelector("#devices tbody");
  if(!devices.length){ tb.innerHTML='<tr><td colspan="9" class="empty-state">还没有设备上报。在需要统计的电脑上安装客户端并填写本服务地址即可。</td></tr>'; return; }
  tb.innerHTML = devices.map(d=>{
    const s = stat[d.device_id]||{};
    const status = (d.status && typeof d.status==="object") ? d.status : {};
    const errs = Object.values(status.agents||{}).filter(a=>a&&a.error).length;
    return `<tr class="${device&&device!==d.device_id?"dim":""}"><td><div class="entity"><span class="entity-icon">${esc((d.name||d.hostname||d.device_id)[0]||"?")}</span><div><div>${esc(d.name||d.hostname||d.device_id)}</div><div class="muted mono" style="font-size:10px">${esc(d.device_id)}</div></div></div></td>
      <td>${esc(d.os||"—")}${d.client_version?`<div class="muted" style="font-size:10px">v${esc(d.client_version)}</div>`:""}</td>
      <td><span class="status ${d.online?"on":"off"}"><i class="status-dot"></i>${d.online?"在线":"离线"}</span>${errs?`<div class="muted" style="font-size:10px;color:var(--danger)">${errs} 个 Agent 有错误</div>`:""}</td>
      <td>${d.interval_minutes} 分钟</td>
      <td class="agents-cell">${agentsSummary(d.agents)}</td>
      <td><b>${fmt(s.total_tokens||0)}</b></td><td>${fmtCost(s.cost||0)}</td>
      <td title="${esc(d.last_sync_at||"")}">${relTime(d.last_sync_at)}<div class="muted" style="font-size:10px">心跳 ${relTime(d.last_seen)}</div></td>
      <td><button class="btn small" data-del="${esc(d.device_id)}" title="删除该设备及其全部数据">删除</button></td></tr>`;
  }).join("");
}
document.querySelector("#devices").addEventListener("click", async e=>{
  const b = e.target.closest("[data-del]"); if(!b) return;
  const id = b.dataset.del;
  if(!confirm(`确定删除设备「${devLabel(id)}」及其上报的所有用量数据？此操作不可恢复。`)) return;
  b.disabled = true;
  try{
    const res = await fetch("/api/devices/"+encodeURIComponent(id),{method:"DELETE"});
    if(!res.ok) throw new Error("HTTP "+res.status);
    if(device===id){ device=""; document.getElementById("device").value=""; }
    await initDevices(); refresh();
  }catch(err){ alert("删除失败："+err.message); b.disabled=false; }
});

async function refresh(){
  const id=++refreshId;
  const error=document.getElementById("err");
  const button=document.getElementById("refreshBtn");
  error.textContent="";
  const dates=rangeDates(range);
  if(!dates||dates[0]>dates[1]){error.textContent=!dates?"请选择完整的开始日期和结束日期":"开始日期不能晚于结束日期";button.disabled=false;return;}
  const [start,end]=dates;
  const q=new URLSearchParams({start,end});
  if(agent)q.set("agent",agent);
  if(device)q.set("device_id",device);
  const qd=new URLSearchParams({start,end}); if(agent)qd.set("agent",agent);
  button.disabled=true;
  try{
    const [rangeData,agents,models,devStats,devList]=await Promise.all([
      jget("/api/stats/range?"+q),jget("/api/stats/agents?"+q),jget("/api/stats/models?"+q),
      jget("/api/stats/devices?"+qd),jget("/api/devices")]);
    if(id!==refreshId)return;
    const t=rangeData.totals;
    const metrics={input:t.input_tokens,output:t.output_tokens,cache:t.cache_read_tokens+t.cache_write_tokens,total:t.total_tokens,cost:t.cost,req:t.requests};
    Object.entries(metrics).forEach(([key,value])=>{const el=document.getElementById("v-"+key);el.textContent=key==="cost"?fmtCost(value):fmt(value);el.title=el.textContent;});
    chartDays=rangeData.days||[];
    chartBucket=rangeData.bucket||"day";
    document.getElementById("trendTitle").textContent=BUCKET_TITLE[chartBucket]||BUCKET_TITLE.day;
    document.getElementById("chartPeriod").textContent=({today:"今天",yesterday:"昨天","7d":"近 7 天","30d":"近 30 天"})[range]||`${start} — ${end}`;
    document.getElementById("chart").innerHTML='<div id="tip"></div>'+lineChart(chartDays,chartBucket);
    setupTooltip();
    const rows=(agents.agents||[]).slice().sort((a,b)=>b.total_tokens-a.total_tokens);
    document.getElementById("agentCount").textContent=rows.length;
    document.querySelector("#agents tbody").innerHTML=rows.map(a=>rowHTML(AGENT_LABEL[a.key]||a.key,a,true)).join("")||'<tr><td colspan="7" class="empty-state">所选区间暂无 Agent 用量</td></tr>';
    renderDistribution(rows);
    modelsData=(models.models||[]).slice().sort((a,b)=>b.total_tokens-a.total_tokens);
    renderModels();
    const devices = devList.devices||[];
    deviceNames = Object.fromEntries(devices.map(d=>[d.device_id, d.name||d.hostname||d.device_id]));
    renderDevices(devices, devStats.devices);
    const online = devices.filter(d=>d.online).length;
    const lastSync = devices.map(d=>d.last_sync_at).filter(Boolean).sort().pop();
    document.getElementById("syncInfo").textContent = `${devices.length} 台设备 · ${online} 台在线 · 最近上报 ${lastSync?relTime(lastSync):"从未"}`;
    lastUpdated=new Date();
    document.getElementById("connectionStatus").classList.remove("error");
    document.getElementById("updated").textContent="数据更新于 "+lastUpdated.toLocaleTimeString("zh-CN",{hour12:false});
  }catch(e){
    if(id!==refreshId)return;
    error.textContent="加载失败："+e.message+"。当前保留上次成功加载的数据，请稍后刷新。";
    document.getElementById("connectionStatus").classList.add("error");
    document.getElementById("updated").textContent="连接异常 · 数据未更新";
  }finally{if(id===refreshId)button.disabled=false;}
}

async function initDevices(){
  try{
    const data=await jget("/api/devices");
    const devices = data.devices||[];
    deviceNames = Object.fromEntries(devices.map(d=>[d.device_id, d.name||d.hostname||d.device_id]));
    const sel=document.getElementById("device");
    sel.innerHTML='<option value="">全部设备</option>'+devices.map(d=>`<option value="${esc(d.device_id)}">${esc(deviceNames[d.device_id])}${d.online?"":" (离线)"}</option>`).join("");
    if(device&&!devices.some(d=>d.device_id===device))device="";
    sel.value=device;
    // Agent 下拉：所有设备配置过的 agent ∪ 有数据的 agent
    const names = new Set();
    devices.forEach(d=>(Array.isArray(d.agents)?d.agents:[]).forEach(a=>a.agent&&names.add(a.agent)));
    try{ const a = await jget("/api/stats/agents?start=1970-01-01"); (a.agents||[]).forEach(r=>r.key&&names.add(r.key)); }catch(_){}
    const asel=document.getElementById("agent");
    asel.innerHTML='<option value="">全部 Agent</option>'+[...names].sort().map(a=>`<option value="${esc(a)}">${esc(AGENT_LABEL[a]||a)}</option>`).join("");
    if(agent&&!names.has(agent))agent="";
    asel.value=agent;
  }catch(e){document.getElementById("err").textContent="设备列表加载失败："+e.message;}
}

document.querySelectorAll(".btn.range").forEach(b=>{
  b.onclick=()=>{range=b.dataset.r;const dates=rangeDates(range);document.getElementById("from").value=dates[0];document.getElementById("to").value=dates[1];syncRangeButtons();refresh();};
});
document.getElementById("apply").onclick=()=>{range="custom";syncRangeButtons();refresh();};
document.getElementById("agent").onchange=e=>{agent=e.target.value;refresh();};
document.getElementById("device").onchange=e=>{device=e.target.value;refresh();};
document.getElementById("refreshBtn").onclick=()=>refresh();
document.getElementById("modelSearch").oninput=renderModels;
function updateNav(){
  const current=location.hash||"#overview";
  document.querySelectorAll('.nav a').forEach(link=>{const active=link.getAttribute("href")===current;link.classList.toggle("active",active);if(active)link.setAttribute("aria-current","location");else link.removeAttribute("aria-current");});
}
window.addEventListener("hashchange",updateNav);
updateNav();
const initialDates=rangeDates(range);
document.getElementById("from").value=initialDates[0];
document.getElementById("to").value=initialDates[1];
initDevices().then(refresh);
setInterval(()=>{if(!document.hidden&&!document.getElementById("refreshBtn").disabled)refresh();},15000);
setInterval(()=>{if(!document.hidden)initDevices();},60000);
