<script lang="ts">
  import { onMount } from 'svelte';
  import CurveGraph from './CurveGraph.svelte';
  import HistoryGraph from './HistoryGraph.svelte';
  import SystemFans from './SystemFans.svelte';
  import HDDControl from './HDDControl.svelte';
  import { api, APIError, setCSRF } from './api';
  import type { AutomaticResult, Config, Curve, GPU, History, Metric, Policy, Status } from './types';

  let authenticated = false, loading = true, token = '', loginError = '', busy = false;
  let gpus: GPU[] = [], status: Status | null = null, config: Config | null = null;
  let selected = '', history: History[] = [], error = '', pollError = '', notice = '', conflict = false;
  let now = Date.now(), dirty = false, initializedFor = '', template = '';
  let draft: Curve = { type:'linear', points:[{temperature:30,duty:30},{temperature:60,duty:50},{temperature:80,duty:100}] };
  let policy: Policy = {mode:'automatic',curve:'',fans:[],min_duty:0,critical_temp:0,hysteresis:3,cooling_hold_seconds:10,limits_confirmed:false};
  let sampling = 5, freshness = 15, failureLimit = 3;
  let polling = false, systemDirty = false, hddDirty = false;
  let section: 'gpu' | 'system' | 'hdd' = 'gpu';
  let operation = '';
  const sections = [
    {id:'gpu', label:'GPU cooling', title:'GPU cooling', description:'Monitor your GPU and manage its fan curve.'},
    {id:'system', label:'System fans', title:'System cooling', description:'Monitor case fans and manage verified fan controls.'},
    {id:'hdd', label:'HDD cooling', title:'Drive cooling', description:'Adjust case-fan speed to your hard drive temperatures.'}
  ] as const;
  $: currentSection = sections.find(item => item.id === section)!;
  $: hasEdits = dirty || systemDirty || hddDirty;
  $: finalDuty = legacy ? draft.ranges?.at(-1)?.fan_speed : draft.points?.at(-1)?.duty;
  $: applyRequirements = [
    ...(!policy.critical_temp ? ['Set a critical temperature.'] : []),
    ...(finalDuty !== 100 ? ['End the curve at 100% fan speed.'] : []),
    ...(!policy.limits_confirmed ? ['Verify and confirm the hardware limits below.'] : [])
  ];
  function switchSection(next: typeof section, focus = false) {
    section = next;
    if (focus) document.getElementById(`tab-${next}`)?.focus();
  }
  function navigateTabs(event: KeyboardEvent, index: number) {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % sections.length;
    else if (event.key === 'ArrowLeft') next = (index + sections.length - 1) % sections.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = sections.length - 1;
    else return;
    event.preventDefault();
    switchSection(sections[next].id, true);
  }
  function beforeUnload(event: BeforeUnloadEvent) {
    if (hasEdits) { event.preventDefault(); event.returnValue = ''; }
  }
  $: gpu = gpus.find(item => item.uuid === selected);
  $: active = !!config && config.gpus[selected]?.mode === 'curve';
  $: legacy = !!draft.ranges?.length;
  $: graphPoints = draft.points ?? (draft.ranges ?? []).flatMap(range => [{temperature:range.min_temperature,duty:range.fan_speed},{temperature:range.max_temperature,duty:range.fan_speed}]);
  $: validation = validateDraft(draft, policy, sampling, freshness, failureLimit);
  $: historyPoints = history.map(item => ({time:item.at, temperature:item.temperature_c}));
  $: currentTemp = pollError || stale(gpu?.metrics.temperature_c, now) ? null : gpu?.metrics.temperature_c?.value ?? null;

  const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value));
  const stateNames: Record<string,string> = {automatic:'Automatic',curve:'Custom curve',monitoring:'Monitoring only',degraded:'Degraded',offline:'Offline'};
  function age(value: string | null | undefined, clock: number) {
    if (!value || value.startsWith('0001')) return 'No sample';
    const seconds = Math.max(0, Math.floor((clock - new Date(value).getTime()) / 1000));
    return seconds < 60 ? `${seconds}s ago` : `${Math.floor(seconds/60)}m ago`;
  }
  function stale(metric: Metric | undefined, clock: number) { return !metric || metric.status !== 'ok' || !metric.collected_at || clock-new Date(metric.collected_at).getTime() > (config?.freshness_seconds ?? 15)*1000; }
  function value(device: GPU | undefined, name: string, decimals = 0) { const m = device?.metrics[name]; return m?.value === null || m?.value === undefined ? '—' : m.value.toFixed(decimals); }
  function metricNote(device: GPU | undefined, name: string, clock: number) { const m = device?.metrics[name]; return !m ? 'Unavailable' : m.status !== 'ok' ? `${m.status.replaceAll('_',' ')}${m.reason ? ` · ${m.reason}` : ''}` : `${stale(m, clock) ? 'Stale · ' : ''}${age(m.collected_at, clock)}`; }
  function edit() { dirty = true; notice = ''; }
  function initializeDraft() {
    if (!config || !selected) return;
    const saved = config.gpus[selected];
    template = saved?.curve || Object.keys(config.curves)[0] || '';
    draft = clone(config.curves[template] ?? {type:'linear',points:[{temperature:30,duty:30},{temperature:60,duty:50},{temperature:80,duty:100}]});
    policy = clone(saved ?? {mode:'automatic',curve:'',fans:[],min_duty:0,critical_temp:0,hysteresis:3,cooling_hold_seconds:10,limits_confirmed:false});
    policy.fans = policy.fans ?? [];
    sampling = config.sampling_seconds; freshness = config.freshness_seconds; failureLimit = config.failure_limit;
    dirty = false; initializedFor = selected;
  }
  function selectGPU() { history = []; error = ''; notice = ''; initializeDraft(); void refreshHistory().catch(handleError); }
  async function refreshHistory() {
    const uuid = selected;
    if (!uuid) { history=[]; return; }
    const result = await api<{history: History[]}>(`/history?uuid=${encodeURIComponent(uuid)}`);
    if (selected === uuid) history = result.history ?? [];
  }
  async function refresh(initial = false) {
    if (polling || !authenticated) return;
    polling = true;
    try {
      const [nextStatus,nextGPU] = await Promise.all([api<Status>('/status'),api<{gpus:GPU[]}>('/gpus')]);
      status = nextStatus; gpus = (nextGPU.gpus ?? []).map(item => ({...item,fans:item.fans ?? [],metrics:item.metrics ?? {}}));
      if (initial || !config) {
        const saved=await api<Config>('/config');
        // Preserve the edit's original revision across re-authentication.
        if(!dirty || !config) { config=saved; initializedFor=''; }
      }
      if (!selected && gpus.length) selected = gpus[0].uuid;
      if (initializedFor !== selected && config) initializeDraft();
      conflict = config !== null && status.desired_revision !== config.revision;
      await refreshHistory();
      pollError = '';
    } catch (cause) { if (cause instanceof APIError && cause.status===401) handleError(cause); else pollError=cause instanceof Error?cause.message:String(cause); }
    finally { polling = false; loading = false; }
  }
  function handleError(cause: unknown) {
    if (cause instanceof APIError && cause.status === 401) { authenticated=false; token=''; loginError='Your session expired. Sign in again.'; }
    else error = cause instanceof Error ? cause.message : String(cause);
    if (cause instanceof APIError && [409,412].includes(cause.status)) conflict = true;
  }
  async function login() {
    busy=true; loginError='';
    try {
      const session = await api<{csrf_token:string}>('/session','POST',{token});
      setCSRF(session.csrf_token); token=''; authenticated=true; await refresh(true);
    } catch (cause) { loginError = cause instanceof Error ? cause.message : String(cause); }
    finally { busy=false; }
  }
  async function logout() {
    try { await api('/session','DELETE'); authenticated=false; token=''; setCSRF(''); }
    catch (cause) { handleError(cause); }
  }
  async function reload() {
    busy=true;
    try { config=await api<Config>('/config'); initializeDraft(); conflict=false; error=''; notice='Loaded the latest saved configuration.'; }
    catch (cause) { handleError(cause); }
    finally { busy=false; }
  }
  function validateDraft(draft: Curve, policy: Policy, sampling: number, freshness: number, failureLimit: number): string[] {
    const errors: string[]=[];
    const points = draft.points ?? [];
    const ranges = draft.ranges ?? [];
    if (ranges.length) {
      if(ranges.length>64) errors.push('Use at most 64 legacy ranges.');
      ranges.forEach((r,i) => {
        if (![r.min_temperature,r.max_temperature,r.fan_speed,r.hysteresis].every(Number.isFinite) || r.min_temperature<0 || r.max_temperature>200 || r.min_temperature>=r.max_temperature) errors.push(`Range ${i+1}: enter an increasing temperature interval between 0 and 200°C.`);
        if (!Number.isInteger(r.fan_speed) || r.fan_speed<0 || r.fan_speed>100 || r.hysteresis<0 || r.hysteresis>20) errors.push(`Range ${i+1}: duty must be 0–100% and hysteresis 0–20°C.`);
        if (i && (r.min_temperature !== ranges[i-1].max_temperature || r.fan_speed < ranges[i-1].fan_speed)) errors.push('Ranges must be contiguous and duties must not decrease.');
      });
    } else {
      if (points.length < 2 || points.length > 64) errors.push('Use between 2 and 64 curve points.');
      points.forEach((p,i) => {
        if (![p.temperature,p.duty].every(Number.isFinite) || p.temperature<0 || p.temperature>200 || !Number.isInteger(p.duty) || p.duty<0 || p.duty>100) errors.push(`Point ${i+1}: enter a temperature 0–200°C and duty 0–100%.`);
        if (i && (p.temperature<=points[i-1].temperature || p.duty<points[i-1].duty)) errors.push('Temperatures must increase and fan duties must not decrease.');
      });
    }
    if (![policy.min_duty,policy.critical_temp,policy.hysteresis,policy.cooling_hold_seconds,sampling,freshness,failureLimit].every(Number.isFinite)) errors.push('Complete every numeric setting.');
    if (!Number.isInteger(policy.min_duty) || !Number.isInteger(policy.cooling_hold_seconds) || policy.min_duty<0 || policy.min_duty>100 || policy.critical_temp<0 || policy.critical_temp>150 || policy.hysteresis<0 || policy.hysteresis>20 || policy.cooling_hold_seconds<0 || policy.cooling_hold_seconds>300) errors.push('Control settings are outside their permitted bounds.');
    if (!Number.isInteger(sampling) || sampling<1 || sampling>60 || !Number.isInteger(freshness) || freshness<sampling || freshness>300 || !Number.isInteger(failureLimit) || failureLimit<1 || failureLimit>10) errors.push('Sampling must be 1–60s, freshness at least the sampling interval (up to 300s), and failures 1–10.');
    return [...new Set(errors)];
  }
  async function save(apply: boolean) {
    if (!config || !gpu) return;
    error=''; notice='';
    if (validation.length) { error=validation.join(' '); return; }
    const finalDuty = legacy ? draft.ranges![draft.ranges!.length-1].fan_speed : draft.points![draft.points!.length-1].duty;
    if (apply && (!policy.limits_confirmed || policy.critical_temp<=0 || finalDuty!==100)) { error='Before applying, confirm validated hardware limits, set a critical temperature, and end the curve at 100%.'; return; }
    if (!apply && active) { error='Return to automatic before saving a draft for this active GPU.'; return; }
    busy=true; operation=apply?'apply':'save';
    const next=clone(config);
    const name=`GPU ${gpu.uuid} curve`;
    next.curves[name]=clone(draft);
    next.gpus[gpu.uuid]={...clone(policy),curve:name,mode:apply?'curve':'automatic'};
    next.sampling_seconds=sampling; next.freshness_seconds=freshness; next.failure_limit=failureLimit;
    try {
      config=await api<Config>('/config','PUT',next,config.revision);
      dirty=false; conflict=false; initializeDraft();
      notice=apply?'Configuration saved. Check the applied revision and fan outcomes below.':'Draft saved. Firmware automatic control remains selected.';
      await refresh();
    } catch (cause) { handleError(cause); }
    finally { busy=false; }
  }
  async function automatic() {
    if (!selected) return;
    busy=true; operation='automatic'; error=''; notice='';
    try {
      const result=await api<AutomaticResult>(`/gpus/${encodeURIComponent(selected)}/default`,'POST',{});
      if (!result.restored) error=`Restoration is not confirmed: ${result.hardware_error || 'check fan states'}.`;
      else notice='Automatic control restored.';
      if (!result.persisted) error += ` The automatic request could not be saved: ${result.storage_error || 'storage unavailable'}. It is latched for this process, but restart may restore the previous desired mode.`;
      const hadDraft=dirty;
      const saved=await api<Config>('/config');
      // A safety action must not silently rebase an unrelated unsaved draft.
      if (!hadDraft) { config=saved; initializeDraft(); }
      await refresh();
    } catch (cause) { handleError(cause); }
    finally { busy=false; }
  }
  function pointChange(index:number,temperature:number,duty:number) { if (!busy && draft.points) { draft.points[index]={temperature,duty}; draft={...draft}; edit(); } }
  function addPoint() {
    if (draft.ranges) { const last=draft.ranges[draft.ranges.length-1]; draft.ranges=[...draft.ranges,{min_temperature:last.max_temperature,max_temperature:Math.min(200,last.max_temperature+10),fan_speed:100,hysteresis:3}]; }
    else { const points=draft.points ?? []; const last=points[points.length-1]; draft.points=[...points,{temperature:Math.min(200,(last?.temperature ?? 20)+10),duty:last?.duty ?? 30}]; }
    edit();
  }
  function removePoint(index:number) { if (draft.ranges) draft.ranges=draft.ranges.filter((_,i)=>i!==index); else draft.points=draft.points?.filter((_,i)=>i!==index); edit(); }
  function toggleFan(index:number,checked:boolean) {
    const all=gpu?.fans.map(f=>f.index) ?? [];
    const selectedFans=policy.fans.length?policy.fans:all;
    policy.fans=checked?[...new Set([...selectedFans,index])]:selectedFans.filter(f=>f!==index);
    // Empty means all in the server contract; don't allow an ambiguous none-selected state.
    if (!policy.fans.length) policy.fans=[index];
    edit();
  }
  onMount(() => {
    let disposed=false;
    void (async()=>{
      try { const session=await api<{csrf_token:string}>('/session'); if(disposed)return; setCSRF(session.csrf_token); authenticated=true; await refresh(true); }
      catch(cause) { if (!(cause instanceof APIError && cause.status===401)) loginError=cause instanceof Error?cause.message:String(cause); }
      finally { loading=false; }
    })();
    const timer=setInterval(()=>{now=Date.now(); void refresh();},2000);
    return ()=>{disposed=true; clearInterval(timer);};
  });
</script>

<svelte:head><title>{authenticated ? currentSection.title : 'Sign in'} · Server Fan Control</title></svelte:head>
<svelte:window onbeforeunload={beforeUnload} />
<a class="skip-link" href="#main-content">Skip to content</a>
<div class="app-shell">
  <header class="topbar">
    <a class="brand" href="/" aria-label="Server Fan Control home"><span class="brand-icon" aria-hidden="true">✣</span><span>SERVER FAN CONTROL<small>GPU & SYSTEM COOLING</small></span></a>
    <div class="topbar-right">
      {#if status?.mock}<span class="badge mock">MOCK HARDWARE</span>{/if}
      <span class="connection"><i class:online={authenticated && status?.available && !pollError}></i>{authenticated ? pollError ? 'Connection lost' : status?.available ? 'Controller online' : 'Controller unavailable' : 'Local hardware service'}</span>
      {#if authenticated}<button class="quiet" onclick={logout}>Sign out</button>{/if}
    </div>
  </header>

  {#if loading && !authenticated}
    <main id="main-content" class="login-layout" tabindex="-1"><p class="muted" role="status">Connecting to your cooling console…</p></main>
  {:else if !authenticated}
    <main id="main-content" class="login-layout" tabindex="-1">
      <section class="login-card panel">
        <span class="eyebrow">PRIVATE CONSOLE</span><h1>Sign in to your<br />cooling console</h1>
        <p class="muted">Sign in with the admin token configured for this service.</p>
        <form onsubmit={(event)=>{event.preventDefault(); void login();}}>
          <label for="admin-token">Admin token</label>
          <input id="admin-token" type="password" bind:value={token} autocomplete="current-password" required />
          {#if loginError}<p class="message error" role="alert">{loginError}</p>{/if}
          <button class="primary full" disabled={busy || loading || !token}>{busy?'Signing in…':'Open console'} <span aria-hidden="true">↗</span></button>
        </form>
        <p class="small muted">The token stays in this session. Use an HTTPS connection when accessing the console over your network.</p>
      </section>
    </main>
  {:else}
    <main id="main-content" class="dashboard" tabindex="-1">
      <nav class="section-nav" aria-label="Cooling navigation">
        <div role="tablist" aria-label="Cooling sections">
          {#each sections as item, index}
            <button id={`tab-${item.id}`} role="tab" aria-label={item.label} aria-selected={section === item.id} aria-controls={`panel-${item.id}`} aria-describedby={(item.id === 'gpu' ? dirty : item.id === 'system' ? systemDirty : hddDirty) ? `draft-${item.id}` : undefined} tabindex={section === item.id ? 0 : -1} onclick={() => switchSection(item.id)} onkeydown={(event) => navigateTabs(event, index)}>
              {item.label}{#if item.id === 'gpu' ? dirty : item.id === 'system' ? systemDirty : hddDirty}<span class="draft-dot" aria-hidden="true" title="Unsaved edits"></span><span class="sr-only" id={`draft-${item.id}`}>Unsaved edits</span>{/if}
            </button>
          {/each}
        </div>
        <span class="small muted">Edits stay local until saved or applied.</span>
      </nav>
      <div class="page-heading"><div><span class="eyebrow">COOLING CONSOLE</span><h1>{currentSection.title}</h1><p class="muted">{currentSection.description}</p></div>
        {#if section === 'gpu'}<div class="device-selector"><label for="gpu-select">Selected GPU</label><select id="gpu-select" bind:value={selected} onchange={selectGPU} disabled={busy || dirty || !gpus.length}>{#each gpus as item}<option value={item.uuid}>{item.name} · GPU {item.index}</option>{/each}</select>{#if dirty}<span class="small muted">Save or discard edits before switching GPU.</span>{/if}</div>{/if}
      </div>
      {#if status?.mock}<div class="mock-note">Demo mode · Readings and fan controls use simulated hardware.</div>{/if}
      {#if pollError}<div class="message error" role="alert">Connection issue: {pollError}. Displayed data may be stale.</div>{/if}
      <div id="panel-gpu" role="tabpanel" aria-labelledby="tab-gpu" hidden={section !== 'gpu'} tabindex="0">
      {#if error || notice}<div class="feedback">{#if error}<div class="message error" role="alert">{error}</div>{/if}{#if notice}<div class="message success" role="status">{notice}</div>{/if}</div>{/if}
      {#if status?.error}<div class="message error" role="alert">{status.error}</div>{/if}
      {#if conflict}<div class="message warning" role="alert">The saved configuration changed since this draft was loaded. Your edits are preserved. <button class="text-button" onclick={reload} disabled={busy}>Discard edits and reload</button></div>{/if}
      {#if !gpu}
        <section class="panel empty-state"><span class="empty-icon" aria-hidden="true">◇</span><h2>{loading?'Connecting to controller…':'No GPU telemetry available'}</h2><p class="muted">Check the selected GPU reservation and the host NVIDIA driver. Missing hardware is never replaced with simulated readings.</p><button onclick={()=>refresh(true)}>Refresh status</button></section>
      {:else}
        <div class="gpu-summary"><div class="gpu-ident"><span class="badge state {gpu.state}">{stateNames[gpu.state] ?? gpu.state}</span><code>{gpu.uuid}</code></div><span class="small muted">Sample {age(gpu.updated_at, now)} · refresh every 2s</span></div>
        {#if gpu.reason}<p class="message warning">{gpu.reason}</p>{/if}
        <section class="metrics" aria-label="GPU telemetry">
          <article class="metric-card" class:stale={stale(gpu.metrics.temperature_c, now)}><span class="metric-label">GPU temperature <span aria-hidden="true">◉</span></span><div class="metric-value">{value(gpu, 'temperature_c')}<small>°C</small></div><p>{metricNote(gpu, 'temperature_c', now)}</p></article>
          <article class="metric-card" class:stale={stale(gpu.metrics.memory_used_bytes, now)}><span class="metric-label">GPU memory <span aria-hidden="true">▤</span></span><div class="metric-value">{gpu.metrics.memory_used_bytes?.value == null ? '—' : (gpu.metrics.memory_used_bytes.value/1073741824).toFixed(1)}<small>GiB</small></div><p>{gpu.metrics.memory_total_bytes?.value == null?'Total unavailable':`of ${(gpu.metrics.memory_total_bytes.value/1073741824).toFixed(1)} GiB`} · {metricNote(gpu, 'memory_used_bytes', now)}</p></article>
          <article class="metric-card" class:stale={stale(gpu.metrics.gpu_utilization_percent, now)}><span class="metric-label">GPU activity <span aria-hidden="true">⌁</span></span><div class="metric-value">{value(gpu, 'gpu_utilization_percent')}<small>%</small></div><p>Memory controller {value(gpu, 'memory_utilization_percent')}% · {metricNote(gpu, 'gpu_utilization_percent', now)}</p></article>
          <article class="metric-card" class:stale={stale(gpu.metrics.power_watts, now)}><span class="metric-label">Board power <span aria-hidden="true">ϟ</span></span><div class="metric-value">{value(gpu, 'power_watts',1)}<small>W</small></div><p>{metricNote(gpu, 'power_watts', now)}</p></article>
        </section>

        <div class="workspace">
          <section class="panel editor">
            <div class="section-heading"><div><span class="eyebrow">COOLING POLICY</span><h2>Fan curve <span class="small-badge">{dirty?'Unsaved changes':active?'Saved policy':config?.gpus[selected]?.curve?'Saved draft':'Preview'}</span></h2></div><button class="quiet" onclick={reload} disabled={busy || !dirty}>Discard edits</button></div>
            <fieldset class="editor-fields" disabled={busy}><legend class="sr-only">GPU fan curve settings</legend><div class="editor-toolbar"><label>Curve type<select bind:value={draft.type} onchange={edit} disabled={legacy}><option value="linear">Linear interpolation</option><option value="step">Step thresholds</option></select></label><label>Load saved curve<select bind:value={template} onchange={()=>{draft=clone(config!.curves[template]);edit();}}><option value="" disabled>Choose a curve</option>{#each Object.keys(config?.curves ?? {}) as name}<option value={name}>{name}</option>{/each}</select></label></div>
            <button class="text-button" onclick={()=>{draft={type:'linear',points:[{temperature:30,duty:30},{temperature:60,duty:50},{temperature:80,duty:100}]};template='';edit();}}>Start a new point curve</button>
            <CurveGraph points={graphPoints} kind={draft.type} currentTemperature={currentTemp} target={gpu.desired_duty} readonly={legacy || busy} onchange={pointChange} />
            <div class="chart-caption"><span><i class="legend-line"></i>Draft curve</span><span><i class="legend-dot"></i>Current temperature / requested target</span><span>{legacy?'Legacy ranges: lower exclusive, upper inclusive.':'Drag points to preview; arrow keys or the table give precise control.'}</span></div>
            <div class="table-wrap"><table class="point-table" aria-label="GPU curve points"><thead><tr><th scope="col">{legacy?'Range':'Point'}</th><th scope="col">{legacy?'Above (°C)':'Temperature (°C)'}</th>{#if legacy}<th scope="col">Through (°C)</th>{/if}<th scope="col">Duty (%)</th>{#if legacy}<th scope="col">Hysteresis (°C)</th>{/if}<th scope="col"><span class="sr-only">Remove</span></th></tr></thead><tbody>
              {#if draft.ranges}
                {#each draft.ranges as range,i}<tr><td class="row-index">{String(i+1).padStart(2,'0')}</td><td><input aria-label={`Range ${i+1} minimum temperature`} type="number" min="0" max="200" bind:value={range.min_temperature} oninput={edit}/></td><td><input aria-label={`Range ${i+1} maximum temperature`} type="number" min="0" max="200" bind:value={range.max_temperature} oninput={edit}/></td><td><input aria-label={`Range ${i+1} duty`} type="number" min="0" max="100" bind:value={range.fan_speed} oninput={edit}/></td><td><input aria-label={`Range ${i+1} hysteresis`} type="number" min="0" max="20" bind:value={range.hysteresis} oninput={edit}/></td><td><button class="icon-button" aria-label={`Remove range ${i+1}`} onclick={()=>removePoint(i)} disabled={draft.ranges.length<=1}>×</button></td></tr>{/each}
              {:else}
                {#each draft.points ?? [] as point,i}<tr><td class="row-index">{String(i+1).padStart(2,'0')}</td><td><input aria-label={`Point ${i+1} temperature`} type="number" min="0" max="200" bind:value={point.temperature} oninput={edit}/></td><td><input aria-label={`Point ${i+1} duty`} type="number" min="0" max="100" bind:value={point.duty} oninput={edit}/></td><td><button class="icon-button" aria-label={`Remove point ${i+1}`} onclick={()=>removePoint(i)} disabled={(draft.points?.length ?? 0)<=2}>×</button></td></tr>{/each}
              {/if}
            </tbody></table></div>
            <button class="text-button add-point" onclick={addPoint} disabled={(draft.points?.length ?? draft.ranges?.length ?? 0)>=64}>＋ Add {legacy?'range':'point'}</button>
            {#if validation.length}<div class="message error" role="alert">{#each validation as problem}<p>{problem}</p>{/each}</div>{/if}
            <details class="policy-settings" open><summary>Control limits & behavior</summary><p class="small muted">Choose values validated for this GPU. The example curve is a draft, not a thermal recommendation.</p>
              <div class="settings-grid"><label>Minimum duty (%)<input type="number" min="0" max="100" bind:value={policy.min_duty} oninput={edit}/></label><label>Critical temperature (°C)<input type="number" min="0" max="150" bind:value={policy.critical_temp} oninput={edit}/></label></div>
              <fieldset class="fan-selection"><legend>Controlled fans</legend>{#each gpu.fans as fan}<label class="checkbox"><input type="checkbox" checked={!policy.fans.length || policy.fans.includes(fan.index)} disabled={policy.fans.length===1 && policy.fans.includes(fan.index)} onchange={(event)=>toggleFan(fan.index,event.currentTarget.checked)}/>Fan {fan.index}</label>{/each}{#if !gpu.fans.length}<span class="muted small">No controllable fans reported.</span>{/if}</fieldset>
              <label class="checkbox confirm"><input type="checkbox" bind:checked={policy.limits_confirmed} onchange={edit}/><span>I have verified this GPU's minimum duty, critical temperature, and automatic restoration behavior.</span></label>
            </details>
            <details class="policy-settings"><summary>Advanced settings</summary><p class="small muted">Delay speed reductions to avoid frequent fan changes.</p><div class="settings-grid"><label>Downward hysteresis (°C)<input type="number" min="0" max="20" bind:value={policy.hysteresis} oninput={edit}/></label><label>Cooling hold (seconds)<input type="number" min="0" max="300" bind:value={policy.cooling_hold_seconds} oninput={edit}/></label></div><p class="small muted">Sampling and freshness settings apply to all GPUs.</p><div class="settings-grid"><label>Sample interval (seconds)<input type="number" min="1" max="60" bind:value={sampling} oninput={edit}/></label><label>Freshness budget (seconds)<input type="number" min="1" max="300" bind:value={freshness} oninput={edit}/></label><label>Consecutive failure limit<input type="number" min="1" max="10" bind:value={failureLimit} oninput={edit}/></label></div></details>
            </fieldset>
            <div class="editor-actions" aria-busy={busy}><button onclick={()=>save(false)} disabled={busy || conflict || active || !!validation.length}>{busy && operation==='save'?'Saving…':'Save draft'}</button><button class="primary" onclick={()=>save(true)} disabled={busy || conflict || !gpu.controllable || !!validation.length || !!applyRequirements.length}>{busy && operation==='apply'?'Applying…':'Save & apply curve'}</button></div>
            {#if gpu.controllable && applyRequirements.length}<div class="requirements"><strong>Before applying</strong><ul>{#each applyRequirements as requirement}<li>{requirement}</li>{/each}</ul></div>{/if}
            <p class="small muted">{active?'To save a draft without changing active control, return to automatic first.':'Saving a draft keeps firmware automatic control. Applying is an explicit hardware action.'}</p>
            {#if !gpu.controllable}<p class="message warning">Monitoring only: {gpu.control_reason || 'Fan control is not supported for this device.'}</p>{/if}
          </section>

          <aside class="side-column">
            <section class="panel control-state"><span class="eyebrow">CONTROLLER STATE</span><h2>{stateNames[gpu.state] ?? gpu.state}</h2><p class="muted">{gpu.state==='automatic'?'The GPU firmware owns fan control.':gpu.state==='curve'?'The controller is reconciling the saved curve.':'Review device and fan outcomes below.'}</p><div class="revision-grid"><div><span>Saved revision</span><strong>{config?.revision ?? '—'}</strong></div><div><span>Applied revision</span><strong>{gpu.applied_revision || '—'}</strong></div></div><p class="small muted">A saved revision is not proof that each fan accepted its target.</p><button class="automatic full" onclick={automatic} disabled={busy}>{busy && operation==='automatic'?'Restoring…':'Return to automatic'}</button><p class="small muted">Restoration remains available if configuration storage fails.</p></section>
            <section class="panel fans-panel"><div class="section-heading"><div><span class="eyebrow">FAN TELEMETRY</span><h2>Fan outcomes</h2></div><span class="small-badge">{gpu.fans.length} fans</span></div>{#each gpu.fans as fan}<div class="fan-card"><div class="fan-title"><strong>Fan {fan.index}</strong><span class="small muted">{fan.state}</span></div><div class="fan-values"><div><span>Reported</span><strong>{fan.reported?.value ?? '—'}<small>%</small></strong></div><div><span>Requested</span><strong>{fan.desired_duty ?? '—'}<small>%</small></strong></div><div><span>Commanded</span><strong>{fan.commanded_duty ?? '—'}<small>%</small></strong></div></div><p class="small muted">{fan.reported?.status==='ok'?age(fan.reported.collected_at, now):fan.reported?.reason || fan.reported?.status || 'Unavailable'}</p>{#if fan.error}<p class="fan-error" role="alert">{fan.error}</p>{/if}</div>{/each}{#if !gpu.fans.length}<p class="muted">This GPU exposes no fan telemetry.</p>{/if}<p class="small muted">Reported percentage is NVML's intended operating speed, not independent proof of physical rotation.</p></section>
            <section class="panel history-panel"><span class="eyebrow">TEMPERATURE HISTORY</span><h2>Recent samples</h2><HistoryGraph points={historyPoints}/><p class="small muted">Up to 30 minutes · gaps represent unavailable readings.</p></section>
          </aside>
        </div>
      {/if}
      </div>
      <div id="panel-system" role="tabpanel" aria-labelledby="tab-system" hidden={section !== 'system'} tabindex="0"><SystemFans onunauthorized={handleError} ondirtychange={(value) => systemDirty = value} /></div>
      <div id="panel-hdd" role="tabpanel" aria-labelledby="tab-hdd" hidden={section !== 'hdd'} tabindex="0"><HDDControl onunauthorized={handleError} ondirtychange={(value) => hddDirty = value} onsetup={() => switchSection('system', true)} /></div>
      <footer><span>Server Fan Control <span class="muted">/ {status?.version || 'MVP'}</span></span><span class="muted">Live readings refresh automatically.</span></footer>
    </main>
  {/if}
</div>
