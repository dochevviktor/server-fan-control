<script lang="ts">
  import { onMount } from 'svelte';
  import CurveGraph from './CurveGraph.svelte';
  import { api, APIError } from './api';
  import type { DiskSnapshot, HDDPolicy, IPMISettings, IPMISnapshot, IPMIResult } from './types';

  export let onunauthorized: (cause: unknown) => void;
  export let ondirtychange: (dirty: boolean) => void = () => {};
  export let onsetup: () => void = () => {};
  const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value));
  const defaultPolicy = (): HDDPolicy => ({mode:'automatic',fan:3,drives:[],curve:{type:'linear',points:[{temperature:25,duty:30},{temperature:35,duty:50},{temperature:45,duty:100}]},sampling_seconds:60,freshness_seconds:180,missing_grace_seconds:60,critical_temp:0,hysteresis:2,cooling_hold_seconds:120,shared_min_duty:0,limits_confirmed:false,standby_verified:false,shared_cooling_verified:false});
  let saved: IPMISettings | null = null, draft: HDDPolicy = defaultPolicy();
  let snapshot: IPMISnapshot | null = null, disks: DiskSnapshot | null = null;
  let dirty = false, busy = false, polling = false, conflict = false, error = '', pollError = '', notice = '', now = Date.now();
  let action: 'save' | 'apply' | 'restore' | 'reload' | '' = '';
  const stateNames: Record<string, string> = {automatic:'Automatic control',monitoring:'Monitoring',curve:'Custom curve active',awaiting_activation:'Ready to activate',fallback:'Automatic fallback',degraded:'Needs attention',offline:'Offline',disabled:'Disabled'};
  $: ondirtychange(dirty);
  $: status = snapshot?.hdd;
  $: samples = disks?.samples ?? status?.samples ?? [];
  $: active = !!status?.armed;
  $: points = draft.curve.points ?? [];
  $: validation = validate(draft);
  $: hardwareReady = !!saved && saved.profile === 'asrock-e3c246d2i' && saved.mapping_verified && saved.automatic_verified && saved.thermal_verified && saved.min_duty > 0;
  $: identityMatches = !!saved && !!snapshot?.reading?.identity && (['board', 'manufacturer', 'firmware'] as const).every(key => saved!.expected_identity[key] === snapshot!.reading.identity[key]);
  $: activationReady = hardwareReady && identityMatches && !!disks?.enabled && draft.drives.length > 0 && draft.critical_temp > 0 && draft.shared_min_duty >= (saved?.min_duty || 1) && draft.limits_confirmed && draft.standby_verified && draft.shared_cooling_verified && points.at(-1)?.duty === 100 && (points.at(-1)?.temperature ?? Infinity) <= draft.critical_temp;
  $: selectedRPM = snapshot?.reading?.fans?.find(f => f.name === saved?.fan_sensors[String(saved.hdd?.fan ?? draft.fan)])?.rpm;
  $: activationSteps = [
    !hardwareReady && 'Save a verified hardware profile in System fans.',
    hardwareReady && !identityMatches && 'Match the hardware profile to the detected board and firmware.',
    !disks?.enabled && 'Enable SMART monitoring.',
    !draft.drives.length && 'Select at least one HDD.',
    draft.critical_temp <= 0 && 'Set a hottest HDD temperature limit.',
    draft.shared_min_duty < (saved?.min_duty || 1) && `Set the shared-cooling minimum to at least ${saved?.min_duty || 1}%.`,
    (points.at(-1)?.duty !== 100 || (points.at(-1)?.temperature ?? Infinity) > draft.critical_temp) && 'End the curve at 100% at or below the hottest HDD limit.',
    (!draft.limits_confirmed || !draft.standby_verified || !draft.shared_cooling_verified) && 'Complete all three hardware confirmations.'
  ].filter(Boolean);
  $: freshStatus = !!status?.updated_at && now - Date.parse(status.updated_at) < 30_000 && !pollError;
  function edit() { dirty = true; notice = ''; }
  function age(value?: string) {
    if (!value || value.startsWith('0001') || !Number.isFinite(Date.parse(value))) return 'No sample';
    const seconds = Math.max(0, Math.floor((now - Date.parse(value)) / 1000));
    return seconds < 60 ? `${seconds}s ago` : `${Math.floor(seconds / 60)}m ago`;
  }
  function isStale(sample: typeof samples[number]) { return sample.status !== 'ok' || now - Date.parse(sample.last_valid_at) > (saved?.hdd?.freshness_seconds ?? 180) * 1000; }
  function format(value: number | null | undefined) { return value === null || value === undefined ? '—' : value.toFixed(1); }
  function report(cause: unknown) {
    if (cause instanceof APIError && cause.status === 401) onunauthorized(cause);
    else error = cause instanceof Error ? cause.message : String(cause);
    if (cause instanceof APIError && [409,412].includes(cause.status)) conflict = true;
  }
  function load(next: IPMISettings) { saved = next; draft = clone(next.hdd ?? defaultPolicy()); dirty = false; conflict = false; }
  async function refresh() {
    if (polling) return;
    polling = true;
    try {
      const [next, nextDisks] = await Promise.all([api<IPMISnapshot>('/ipmi'), api<DiskSnapshot>('/disks')]);
      snapshot = next; disks = nextDisks;
      if (snapshot.enabled) {
        const config = await api<IPMISettings>('/ipmi/config');
        if (!saved) load(config);
        else if (config.revision > saved.revision) {
          if (dirty || busy) conflict = true;
          else load(config);
        }
      }
      pollError = '';
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) onunauthorized(cause);
      else pollError = cause instanceof Error ? cause.message : String(cause);
    } finally { polling = false; }
  }
  async function reload() {
    busy = true; action = 'reload';
    try { load(await api<IPMISettings>('/ipmi/config')); error = ''; notice = ''; }
    catch (cause) { report(cause); }
    finally { busy = false; action = ''; }
  }
  async function save(apply: boolean) {
    if (!saved || validation.length || (apply && !activationReady)) return;
    busy = true; action = apply ? 'apply' : 'save'; error = ''; notice = '';
    const next = clone(saved);
    next.hdd = {...clone(draft),mode:apply?'curve':'automatic'};
    try {
      load(await api<IPMISettings>('/ipmi/config','PUT',next,saved.revision));
      notice = apply ? 'HDD curve saved and activation requested. Check the controller state and fan speed to confirm the result.' : 'HDD draft saved. The selected drives will be monitored; the curve is not active.';
      await refresh();
    } catch (cause) { report(cause); }
    finally { busy = false; action = ''; }
  }
  async function automatic() {
    busy = true; action = 'restore'; error = ''; notice = '';
    try {
      const result = await api<IPMIResult>('/ipmi/default','POST',{});
      if (result.restored && result.persisted) notice = 'HDD curve stopped and owned system fans restored to automatic.';
      else error = [result.hardware_error || (!result.restored ? 'Automatic restoration is not confirmed.' : ''),result.storage_error || (!result.persisted ? 'The automatic request could not be saved.' : '')].filter(Boolean).join(' · ');
      const latest = await api<IPMISettings>('/ipmi/config');
      if (!dirty) load(latest);
      else if (saved?.revision !== latest.revision) conflict = true;
      await refresh();
    } catch (cause) { report(cause); }
    finally { busy = false; action = ''; }
  }
  function validate(p: HDDPolicy): string[] {
    const errors: string[] = [];
    const pts = p.curve.points ?? [];
    if (pts.length < 2 || pts.length > 64 || p.curve.ranges?.length) errors.push('Use 2–64 HDD curve points.');
    pts.forEach((point,i) => {
      if (!Number.isFinite(point.temperature) || point.temperature < 0 || point.temperature > 100 || !Number.isInteger(point.duty) || point.duty < 0 || point.duty > 100) errors.push(`HDD point ${i+1}: enter 0–100°C and a whole duty percentage 0–100.`);
      if (i && (point.temperature <= pts[i-1].temperature || point.duty < pts[i-1].duty)) errors.push('HDD temperatures must increase and duties must not decrease.');
    });
    const ids = new Set<string>(), serials = new Set<string>(), paths = new Set<string>();
    if (p.drives.length > 32) errors.push('Select at most 32 HDDs.');
    p.drives.forEach((drive,i) => {
      if (!drive.id.trim() || !drive.serial.trim() || !drive.path.startsWith('/dev/') || drive.path.includes('..') || !['ata','sat'].includes(drive.type)) errors.push(`HDD ${i+1}: enter an ID, expected serial, mapped /dev/ path and verified ATA/SAT type.`);
      if (ids.has(drive.id) || serials.has(drive.serial) || paths.has(drive.path)) errors.push('Each selected HDD needs a distinct ID, serial and device path.');
      ids.add(drive.id); serials.add(drive.serial); paths.add(drive.path);
    });
    if (![p.sampling_seconds,p.freshness_seconds,p.missing_grace_seconds,p.cooling_hold_seconds,p.shared_min_duty].every(Number.isInteger) || ![p.critical_temp,p.hysteresis].every(Number.isFinite)) errors.push('Complete every HDD numeric setting.');
    if (p.sampling_seconds < 10 || p.sampling_seconds > 3600 || p.freshness_seconds < p.sampling_seconds || p.freshness_seconds > 7200 || p.missing_grace_seconds < 0 || p.missing_grace_seconds > 300) errors.push('Check the HDD sampling, freshness and missing-data grace intervals.');
    if (p.critical_temp < 0 || p.critical_temp > 100 || p.hysteresis < 0 || p.hysteresis > 20 || p.cooling_hold_seconds < 0 || p.cooling_hold_seconds > 3600 || p.shared_min_duty < 0 || p.shared_min_duty > 100) errors.push('HDD guards or cooling settings are outside their permitted bounds.');
    return [...new Set(errors)];
  }
  function changePoint(index: number, temperature: number, duty: number) { if (busy || !saved) return; points[index] = {temperature,duty}; draft.curve.points = [...points]; edit(); }
  function addPoint() { const last = points.at(-1); draft.curve.points = [...points,{temperature:Math.min(100,(last?.temperature ?? 20)+5),duty:last?.duty ?? 30}]; edit(); }
  function addDrive() { draft.drives = [...draft.drives,{id:'',serial:'',path:'',type:'sat'}]; edit(); }
  onMount(() => {
    void refresh();
    const timer = setInterval(() => { now = Date.now(); void refresh(); },3000);
    return () => clearInterval(timer);
  });
</script>

<section class="panel hdd-control" aria-labelledby="hdd-heading">
  <div class="section-heading"><div><span class="eyebrow">DRIVE COOLING</span><h2 id="hdd-heading">HDD case-fan curve</h2></div>{#if disks?.mock}<span class="badge mock">SIMULATED HDDs</span>{/if}</div>
  {#if pollError}<p class="message error" role="alert">HDD connection issue: {pollError}. Displayed readings may be stale.</p>{/if}
  {#if !snapshot}
    <div class="hdd-empty"><h3>{pollError ? 'HDD control is unavailable' : 'Loading HDD control…'}</h3><p class="muted">{pollError ? 'Check the connection and try again.' : 'Fetching controller status and drive temperatures.'}</p>{#if pollError}<button onclick={refresh} disabled={polling}>Retry HDD connection</button>{/if}</div>
  {:else if !snapshot.enabled}
    <div class="hdd-empty"><h3>HDD cooling is not configured</h3><p class="muted">Enable local IPMI and SMART monitoring to control a case fan using drive temperatures.</p></div>
  {:else}
    {#if !saved}<div class="message warning">{pollError ? 'HDD settings could not be loaded.' : 'Loading HDD settings…'} {#if pollError}<button class="text-button" onclick={refresh} disabled={polling}>Retry HDD connection</button>{/if}</div>{/if}
    {#if !disks?.enabled}<p class="message warning">SMART monitoring is disabled. Enable the SMART integration to collect selected HDD temperatures.</p>{/if}
    <p class="intro muted">Adjust one case-fan zone using the average temperature of your selected drives. The hottest-drive limit and minimum fan speed protect the other components sharing that airflow.</p>
    <div class="feedback">
    {#if error}<p class="message error" role="alert">{error}</p>{/if}
    {#if notice}<p class="message success" role="status">{notice}</p>{/if}
    </div>
    {#if conflict}<p class="message warning" role="alert">System fan settings changed. Your HDD edits are preserved. <button class="text-button" onclick={reload} disabled={busy}>Discard HDD edits and reload</button></p>{/if}
    <div class="hdd-layout">
      <fieldset class="hdd-editor" disabled={busy || !saved} aria-busy={busy || !saved}>
        <legend class="sr-only">HDD cooling settings</legend>
        <div class="section-heading"><h3>Average-temperature curve</h3><span class="small muted">{dirty ? 'Unsaved changes' : active ? 'Enabled curve' : 'Saved draft'}</span></div>
        <div class="settings-grid"><label>HDD case-fan zone<select bind:value={draft.fan} onchange={edit}><option value={1}>FAN1</option><option value={2}>FAN2</option><option value={3}>FAN3</option></select></label><label>HDD curve type<select bind:value={draft.curve.type} onchange={edit}><option value="linear">Linear interpolation</option><option value="step">Stepped thresholds</option></select></label></div>
        <CurveGraph {points} kind={draft.curve.type} currentTemperature={freshStatus ? status?.mean_c ?? null : null} target={freshStatus ? status?.target ?? null : null} minimumTemperature={15} maximumTemperature={60} temperatureLimit={100} onchange={changePoint}/>
        <p class="small muted">Drag a point, use arrow keys, or edit the values below. Changes take effect only after Save & apply. Use temperatures and fan speeds verified for your enclosure.</p>
        <div class="table-wrap"><table class="point-table" aria-label="HDD curve points"><thead><tr><th>Point</th><th>Mean (°C)</th><th>Duty (%)</th><th><span class="sr-only">Remove</span></th></tr></thead><tbody>{#each points as point,i}<tr><td>{i+1}</td><td><input aria-label={`HDD point ${i+1} temperature`} type="number" min="0" max="100" bind:value={point.temperature} oninput={edit}/></td><td><input aria-label={`HDD point ${i+1} duty`} type="number" min="0" max="100" step="1" bind:value={point.duty} oninput={edit}/></td><td><button class="icon-button" aria-label={`Remove HDD point ${i+1}`} disabled={points.length<=2} onclick={()=>{draft.curve.points=points.filter((_,j)=>j!==i);edit();}}>×</button></td></tr>{/each}</tbody></table></div>
        <button class="text-button" onclick={addPoint} disabled={points.length>=64}>＋ Add HDD curve point</button>
        <details class="policy-settings" open><summary>Selected HDDs <span class="summary-count">{draft.drives.length}</span></summary>
          <p class="small muted">Only these drives are monitored. Match each serial, device path and ATA/SAT type. SSDs, NVMe drives and unlisted replacements are excluded.</p>
          {#each draft.drives as drive,i}<fieldset class="drive"><legend>HDD {i+1}</legend><div class="settings-grid"><label>HDD {i+1} ID<input bind:value={drive.id} oninput={edit} placeholder="data-1"/></label><label>HDD {i+1} expected serial<input bind:value={drive.serial} oninput={edit}/></label><label>HDD {i+1} mapped device<input bind:value={drive.path} oninput={edit} placeholder="/dev/sda"/></label><label>HDD {i+1} device type<select bind:value={drive.type} onchange={edit}><option value="sat">SAT</option><option value="ata">ATA</option></select></label></div><button class="text-button" onclick={()=>{draft.drives=draft.drives.filter((_,j)=>j!==i);edit();}}>Remove HDD {i+1}</button></fieldset>{/each}
          {#if !draft.drives.length}<p class="small muted">No HDDs selected. Add a drive and save a draft to start collecting its temperature.</p>{/if}
          <button class="text-button" onclick={addDrive} disabled={draft.drives.length>=32}>＋ Add selected HDD</button>
        </details>
        <details class="policy-settings" open><summary>Safety limits & verification</summary>
          <div class="settings-grid"><label>Hottest HDD limit (°C)<input type="number" min="0" max="100" bind:value={draft.critical_temp} oninput={edit}/></label><label>Shared-cooling minimum duty (%)<input type="number" min="0" max="100" step="1" bind:value={draft.shared_min_duty} oninput={edit}/></label></div>
          <p class="small muted">The minimum fan speed must cool every component sharing this zone, including the CPU, HBA and board. Verify all three requirements before applying a curve.</p>
          <div class="confirmations"><label class="checkbox"><input type="checkbox" bind:checked={draft.limits_confirmed} onchange={edit}/><span>I verified the HDD temperature limit, curve and firmware automatic fallback under load.</span></label><label class="checkbox"><input type="checkbox" bind:checked={draft.standby_verified} onchange={edit}/><span>I verified SMART identity, temperature readings and standby detection without unintended wakeups on these drives.</span></label><label class="checkbox"><input type="checkbox" bind:checked={draft.shared_cooling_verified} onchange={edit}/><span>I verified the shared-cooling minimum for every component affected by this case-fan zone.</span></label></div>
        </details>
        <details class="policy-settings"><summary>Advanced timing & missing data</summary><div class="settings-grid"><label>HDD downward hysteresis (°C)<input type="number" min="0" max="20" bind:value={draft.hysteresis} oninput={edit}/></label><label>HDD cooling hold (seconds)<input type="number" min="0" max="3600" step="1" bind:value={draft.cooling_hold_seconds} oninput={edit}/></label><label>HDD sample interval (seconds)<input type="number" min="10" max="3600" step="1" bind:value={draft.sampling_seconds} oninput={edit}/></label><label>HDD freshness budget (seconds)<input type="number" min="10" max="7200" step="1" bind:value={draft.freshness_seconds} oninput={edit}/></label><label>HDD missing-data grace (seconds)<input type="number" min="0" max="300" step="1" bind:value={draft.missing_grace_seconds} oninput={edit}/></label></div><p class="small muted">Fan speed increases promptly. Reductions wait for hysteresis and the cooling hold. Missing or expired samples cannot reduce cooling. Incomplete coverage holds the previous target during grace, then restores automatic. Sleeping drives are excluded from the average. A partly sleeping group cannot reduce cooling; if all selected drives sleep, firmware automatic control is used. Collection time does not establish the drive sensor's internal update time.</p></details>
        {#if validation.length}<div class="message error" role="alert">{#each validation as issue}<p>{issue}</p>{/each}</div>{/if}
        <div class="editor-actions"><button class="quiet" onclick={reload} disabled={busy || !dirty}>{action === 'reload' ? 'Discarding…' : 'Discard HDD edits'}</button><button onclick={()=>save(false)} disabled={busy || conflict || active || !!validation.length}>{action === 'save' ? 'Saving draft…' : 'Save HDD draft'}</button><button class="primary" onclick={()=>save(true)} disabled={busy || conflict || !activationReady || !!validation.length}>{action === 'apply' ? 'Applying curve…' : 'Save & apply HDD curve'}</button></div>
        {#if active}<p class="small muted">To save a draft without applying it, stop the HDD curve first.</p>{/if}
        {#if activationSteps.length}<div class="activation-checklist"><h3>Before you apply</h3><ul>{#each activationSteps as step}<li>{step}</li>{/each}</ul>{#if !hardwareReady}<button class="text-button" onclick={onsetup}>Set up system fans →</button>{/if}</div>{/if}
      </fieldset>
      <aside class="hdd-status">
        <span class="eyebrow">HDD CONTROLLER</span><h3>{stateNames[status?.state ?? 'monitoring'] ?? status?.state?.replaceAll('_', ' ')}</h3>
        <p class="small muted">{status?.reason || 'Save selected drives to begin monitoring.'}</p>
        <div class="fan-values"><div><span>Average HDD</span><strong>{format(status?.mean_c)}<small>°C</small></strong></div><div><span>Hottest HDD</span><strong>{format(status?.max_c)}<small>°C</small></strong></div><div><span>Target speed</span><strong>{status?.target ?? '—'}<small>%</small></strong></div></div>
        <p class="small muted">Valid {status?.valid ?? 0} / selected {status?.expected ?? saved?.hdd?.drives.length ?? 0} · asleep {status?.asleep ?? 0} · zone RPM {selectedRPM ?? '—'}</p>
        <p class="small muted">Controller updated {age(status?.updated_at)}{status && !freshStatus ? ' · Stale or unavailable' : ''}</p>
        <p class="small muted">{active ? 'Curve is enabled. Check the target and measured RPM to confirm cooling.' : 'Curve is inactive. Apply it explicitly after each restart.'}</p>
        <button class="automatic full" onclick={automatic} disabled={busy}>{action === 'restore' ? 'Restoring automatic…' : 'Stop HDD curve & restore automatic'}</button>
        <p class="small muted">Returns all system fans controlled by this service to firmware automatic control, even if settings cannot be saved.</p>
        <h3 class="telemetry-heading">Selected drive temperatures</h3>
        {#each samples as sample}<article class="disk-sample" class:stale={isStale(sample)}><div class="sample-title"><strong>{sample.id}</strong><span>{format(sample.temperature_c)} °C</span></div><p class="small muted">{sample.status.replaceAll('_',' ')}{isStale(sample) && sample.temperature_c !== null ? ' · last known reading, not a fresh sample' : ''}</p><p class="small muted">Last valid {age(sample.last_valid_at)} · completed {age(sample.read_completed_at)}</p><p class="small muted">{sample.serial || 'Identity unavailable'} · {sample.path}</p>{#if sample.reason}<p class="small muted">{sample.reason}</p>{/if}</article>{/each}
        {#if !samples.length}<p class="small muted">No drive samples yet.</p>{/if}
      </aside>
    </div>
  {/if}
</section>

<style>
  .hdd-control { margin-top: 0; }
  .intro { font-size: 14px; margin: 16px 0; max-width: 80ch; }
  .hdd-layout { display: grid; grid-template-columns: minmax(0, 1.7fr) minmax(280px, 1fr); gap: 32px; margin-top: 28px; align-items: start; }
  .hdd-editor, .hdd-status { min-width: 0; }
  .hdd-editor { border: 0; padding: 0; margin: 0; }
  h3 { font-size: 15px; font-weight: 600; margin: 10px 0; }
  .hdd-status { border-left: 1px solid var(--border); padding-left: 28px; }
  .hdd-status > p { margin: 12px 0; }
  .hdd-status > h3 { font-size: 24px; }
  .hdd-status > .telemetry-heading { font-size: 15px; margin-top: 28px; }
  .drive { border: 1px solid var(--border); border-radius: 8px; margin: 16px 0; padding: 0 16px 12px; min-width: 0; }
  .drive legend { font-size: 13px; color: var(--muted); padding: 0 6px; }
  .drive .settings-grid { margin-top: 8px; margin-bottom: 8px; }
  .confirmations { display: grid; gap: 16px; margin: 20px 0; }
  .editor-actions { flex-wrap: wrap; }
  .disk-sample { padding: 16px 0; border-bottom: 1px solid var(--border); overflow-wrap: anywhere; }
  .disk-sample.stale { color: #e4c9a3; }
  .sample-title { display: flex; justify-content: space-between; gap: 12px; font-size: 14px; margin-bottom: 6px; }
  .sample-title strong { font-weight: 600; }
  .disk-sample p { margin: 4px 0; }
  .hdd-status .fan-values strong { font-size: 22px; }
  .point-table td { font-size: 13px; }
  .hdd-editor > .section-heading { align-items: baseline; }
  .summary-count { color: var(--muted); font-weight: 400; margin-left: 8px; }
  .activation-checklist { background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 12px 16px; margin-top: 20px; }
  .activation-checklist h3 { margin: 0 0 8px; font-size: 13px; }
  .activation-checklist ul { margin: 0; padding-left: 18px; color: var(--muted); font-size: 13px; line-height: 1.7; }
  .hdd-empty { padding: 32px 0; }
  .hdd-empty button { margin-top: 16px; }
  @media (max-width: 900px) {
    .hdd-layout { grid-template-columns: 1fr; }
    .hdd-status { order: -1; border-left: 0; border-bottom: 1px solid var(--border); padding: 0 0 24px; }
    .hdd-status > .fan-values { max-width: 500px; }
  }
</style>
