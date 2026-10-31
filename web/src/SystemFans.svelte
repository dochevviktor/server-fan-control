<script lang="ts">
  import { onMount } from 'svelte';
  import { api, APIError } from './api';
  import type { IPMISettings, IPMISnapshot, IPMIResult } from './types';

  export let onunauthorized: (cause: unknown) => void;
  export let ondirtychange: (dirty: boolean) => void = () => {};
  let snapshot: IPMISnapshot | null = null, saved: IPMISettings | null = null, draft: IPMISettings | null = null;
  let error = '', pollError = '', notice = '', busy = false, polling = false, dirty = false, conflict = false;
  let fan = 3, duty = 35, now = Date.now(), verificationOpen = true;
  let pending: 'save' | 'apply' | 'automatic' | 'reload' | null = null;
  const stateNames: Record<string, string> = {automatic:'Automatic', monitoring:'Monitoring only', manual:'Manual control', ready:'Ready', degraded:'Needs attention', offline:'Offline'};
  const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value));
  $: ondirtychange(dirty);
  $: verified = !!saved && saved.automatic_verified && saved.mapping_verified && saved.thermal_verified && saved.min_duty >= 1 && saved.profile === 'asrock-e3c246d2i';
  $: identityMatches = !!saved && !!snapshot?.reading?.identity && (['board', 'manufacturer', 'firmware'] as const).every(key => saved!.expected_identity[key] === snapshot!.reading.identity[key]);
  $: targetValid = Number.isInteger(duty) && duty >= (saved?.min_duty || 1) && duty <= 100;
  $: fresh = !!snapshot?.reading?.collected_at && now - Date.parse(snapshot.reading.collected_at) < 30_000;
  $: canSet = verified && identityMatches && selectedSensor?.status === 'ok' && (selectedSensor?.rpm ?? 0) > 0 && fresh && !dirty && !conflict && !busy && targetValid && !snapshot?.hdd?.armed && ['automatic', 'monitoring', 'manual', 'ready'].includes(snapshot?.state ?? '');
  $: controlHint = !saved ? 'Loading the hardware profile…'
    : !verified ? 'Complete and save the hardware verification below to enable manual control.'
    : !identityMatches ? 'The detected board or BMC firmware differs from the verified profile.'
    : dirty ? 'Save or discard profile changes before applying a duty.'
    : conflict ? 'Reload the current hardware profile before applying a duty.'
    : snapshot?.hdd?.armed ? 'The HDD curve is controlling this zone. Restore automatic control before setting a manual speed.'
    : !fresh ? 'Waiting for fresh system fan readings.'
    : selectedSensor?.status !== 'ok' || (selectedSensor?.rpm ?? 0) <= 0 ? 'The selected fan needs a valid, non-zero RPM reading before manual control is available.'
    : !targetValid ? `Enter a whole percentage from ${saved.min_duty || 1} to 100.`
    : !['automatic', 'monitoring', 'manual', 'ready'].includes(snapshot?.state ?? '') ? 'Manual control is unavailable in the current controller state. Review the status above.'
    : '';
  $: selectedSensor = snapshot?.reading?.fans?.find(item => item.name === (saved?.fan_sensors?.[String(fan)] || `FAN${fan}`));
  function profileDraft(value: IPMISettings): IPMISettings { return {...clone(value), fan_sensors:{...(value.fan_sensors ?? {})}}; }
  function readback(value: number | undefined) {
    if (value === undefined) return '—';
    if (value < 0 || value > 100) return `0x${value.toString(16).padStart(2, '0')} (unverified)`;
    if (value === 0) return saved?.automatic_verified && saved.mapping_verified && identityMatches ? 'Auto' : '00 (unverified)';
    return saved?.mapping_verified && identityMatches ? `${value}%` : `${value} (unverified)`;
  }
  function edit() { dirty = true; notice = ''; }
  function report(cause: unknown) {
    if (cause instanceof APIError && cause.status === 401) onunauthorized(cause);
    else error = cause instanceof Error ? cause.message : String(cause);
    if (cause instanceof APIError && [409, 412].includes(cause.status)) conflict = true;
  }
  function sampleAge(value?: string) {
    if (!value || value.startsWith('0001')) return 'No sample';
    return `${Math.max(0, Math.floor((now - Date.parse(value)) / 1000))}s ago`;
  }
  async function refresh() {
    if (polling) return;
    polling = true;
    try {
      snapshot = await api<IPMISnapshot>('/ipmi');
      if (snapshot.enabled) {
        const next = await api<IPMISettings>('/ipmi/config');
        if (!saved) { saved = next; draft = profileDraft(next); verificationOpen = !(next.automatic_verified && next.mapping_verified && next.thermal_verified && next.min_duty > 0 && next.profile === 'asrock-e3c246d2i'); }
        else if (next.revision > saved.revision) {
          if (dirty) conflict = true;
          else { saved = next; draft = profileDraft(next); conflict = false; }
        }
      }
      pollError = '';
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) onunauthorized(cause);
      else pollError = cause instanceof Error ? cause.message : String(cause);
    } finally { polling = false; }
  }
  async function reload() {
    busy = true; pending = 'reload';
    try { saved = await api<IPMISettings>('/ipmi/config'); draft = profileDraft(saved); dirty = false; conflict = false; error = ''; }
    catch (cause) { report(cause); }
    finally { busy = false; pending = null; }
  }
  async function save() {
    if (!draft || !saved) return;
    busy = true; pending = 'save'; error = ''; notice = '';
    try {
      saved = await api<IPMISettings>('/ipmi/config', 'PUT', draft, saved.revision);
      draft = profileDraft(saved); dirty = false; conflict = false;
      notice = 'Hardware profile saved. Select a fan and apply a duty to start manual control.';
      await refresh();
    } catch (cause) { report(cause); }
    finally { busy = false; pending = null; }
  }
  function showResult(result: IPMIResult, automatic: boolean) {
    if (result.hardware_error || result.storage_error) {
      error = [result.hardware_error, result.storage_error].filter(Boolean).join(' · ');
    } else if (automatic ? result.restored : result.applied) {
      notice = automatic ? 'Owned system fans restored to automatic; readback confirmed.' : `FAN${fan} target accepted; readback confirmed. Check its RPM and physical response.`;
    } else error = result.error || 'Operation is not confirmed. Review the controller state.';
  }
  async function apply() {
    if (!canSet) return;
    busy = true; pending = 'apply'; error = ''; notice = '';
    try { showResult(await api<IPMIResult>('/ipmi/target', 'POST', {fan, duty}), false); await refresh(); }
    catch (cause) { report(cause); }
    finally { busy = false; pending = null; }
  }
  async function automatic() {
    busy = true; pending = 'automatic'; error = ''; notice = '';
    try { showResult(await api<IPMIResult>('/ipmi/default', 'POST', {}), true); await refresh(); }
    catch (cause) { report(cause); }
    finally { busy = false; pending = null; }
  }
  onMount(() => {
    void refresh();
    const timer = setInterval(() => { now = Date.now(); void refresh(); }, 3000);
    return () => clearInterval(timer);
  });
</script>

<section class="panel system-fans" aria-labelledby="system-fans-heading">
  <div class="section-heading"><div><span class="eyebrow">MOTHERBOARD COOLING</span><h2 id="system-fans-heading">System fans</h2></div>
    <div class="status-badges">{#if snapshot?.mock}<span class="badge mock">SIMULATED IPMI</span>{/if}{#if snapshot?.enabled}<span class="badge state {snapshot.state}">{stateNames[snapshot.state] ?? snapshot.state}</span>{/if}</div>
  </div>
  {#if pollError}<p class="message error" role="alert">System fan connection issue: {pollError}. Displayed readings may be stale. <button class="text-button" onclick={refresh} disabled={polling}>Retry connection</button></p>{/if}
  {#if !snapshot}<p class="muted loading-state" role="status">{pollError ? 'System fan status is unavailable.' : 'Loading system fan status…'}</p>
  {:else if !snapshot.enabled}<div class="integration-empty"><h3>System fan monitoring is disabled</h3><p class="muted">Enable the optional IPMI integration to see motherboard fan speeds and configure manual control.</p></div>
  {:else}
    <p class="muted identity">{snapshot.reading?.identity?.manufacturer || 'Unknown manufacturer'} · {snapshot.reading?.identity?.board || 'Unknown board'} · BMC {snapshot.reading?.identity?.firmware || 'unavailable'}</p>
    {#if snapshot.reason}<p class="message warning">{snapshot.reason}</p>{/if}
    <div class="feedback">
    {#if error}<p class="message error" role="alert">{error}</p>{/if}
    {#if notice}<p class="message success" role="status">{notice}</p>{/if}
    </div>
    {#if conflict}<p class="message warning" role="alert">The system fan profile changed. Your edits are preserved. <button class="text-button" onclick={reload} disabled={busy}>Discard system fan edits and reload</button></p>{/if}
    <div class="system-layout">
      <section class="sensor-overview" aria-labelledby="system-sensors-heading">
        <h3 id="system-sensors-heading">Live fan speeds</h3>
        <p class="small muted">Sample {sampleAge(snapshot.reading?.collected_at)}{!fresh ? ' · Stale or unavailable' : ''}</p>
        <div class="table-wrap"><table class="point-table" aria-label="System fan sensors"><thead><tr><th>Sensor</th><th>Rotation</th><th>Status</th></tr></thead><tbody>
          {#each snapshot.reading?.fans ?? [] as sensor}<tr><td>{sensor.name}</td><td class="rpm-value">{sensor.rpm ?? '—'} <span class="muted">RPM</span></td><td>{sensor.status === 'ok' ? 'Normal' : sensor.status.replaceAll('_', ' ')}{sensor.reason ? ` · ${sensor.reason}` : ''}</td></tr>{/each}
        </tbody></table></div>
        {#if !snapshot.reading?.fans?.length}<p class="small muted sample">No fan sensor readings available.</p>{/if}
        <p class="small muted sample">RPM shows actual rotation. Duty readback shows the speed requested by the motherboard controller.</p>
      </section>
      <section class="manual-control" aria-labelledby="manual-control-heading">
        <h3 id="manual-control-heading">Manual fan speed</h3>
        <p class="small muted">Choose a fan and set a fixed speed. Changes take effect when you apply them.</p>
        <div class="settings-grid">
          <label>System fan header<select bind:value={fan} disabled={busy}><option value={1}>FAN1 · CPU header</option><option value={2}>FAN2 · front header</option><option value={3}>FAN3 · rear header</option></select></label>
          <label>System fan duty (%)<input type="number" min={saved?.min_duty || 1} max="100" step="1" bind:value={duty} disabled={busy} aria-describedby="system-control-hint"/></label>
        </div>
        <div class="fan-values"><div><span>Selected RPM</span><strong>{selectedSensor?.rpm ?? '—'}</strong></div><div><span>Duty readback</span><strong>{readback(snapshot.reading?.duties?.[fan - 1])}</strong></div><div><span>Ownership</span><strong>{snapshot.owned_fans?.includes(fan) ? 'Managed' : 'Unowned'}</strong></div></div>
        <p class="small muted" id="system-control-hint">{controlHint || 'Ready to apply. Previously managed fans keep their last speed when you select another fan.'}</p>
        <div class="system-actions"><button class="primary" onclick={apply} disabled={!canSet}>{pending === 'apply' ? 'Applying speed…' : 'Apply system fan duty'}</button><button class="automatic" onclick={automatic} disabled={busy}>{pending === 'automatic' ? 'Restoring automatic…' : 'Restore system fans to automatic'}</button></div>
        <p class="small muted">Restore returns all fans managed by this service to automatic control. Manual speeds are cleared after a restart.</p>
      </section>
      {#if draft}
        <details class="verification" bind:open={verificationOpen}><summary>Hardware profile & verification {dirty ? '· Unsaved changes' : ''}</summary>
          <p class="small muted">Complete this setup once for your board and firmware. Confirm each item through supervised hardware testing; saving only records your verification.</p>
          <fieldset class="profile-fields" disabled={busy}><legend class="sr-only">Hardware verification settings</legend>
          <label>System fan profile<select bind:value={draft.profile} onchange={edit}><option value="">Monitoring only</option><option value="asrock-e3c246d2i">ASRock Rack E3C246D2I</option></select></label>
          <div class="settings-grid">
            <label>Verified board<input bind:value={draft.expected_identity.board} oninput={edit}/></label>
            <label>Verified manufacturer<input bind:value={draft.expected_identity.manufacturer} oninput={edit}/></label>
            <label>Verified BMC firmware<input bind:value={draft.expected_identity.firmware} oninput={edit}/></label>
            <label>System fan minimum duty (%)<input type="number" min="1" max="100" step="1" bind:value={draft.min_duty} oninput={edit}/></label>
            {#each [1, 2, 3] as slot}<label>FAN{slot} sensor name<input bind:value={draft.fan_sensors[String(slot)]} oninput={edit} list="ipmi-sensor-names"/></label>{/each}
            <datalist id="ipmi-sensor-names">{#each snapshot.reading?.fans ?? [] as sensor}<option value={sensor.name}></option>{/each}</datalist>
          </div>
          <button class="text-button" disabled={!snapshot.reading?.identity?.board} onclick={()=>{if (draft && snapshot?.reading?.identity) { draft.expected_identity = clone(snapshot.reading.identity); edit(); }}}>Use detected board and firmware</button>
          <div class="confirmations">
            <label class="checkbox"><input type="checkbox" bind:checked={draft.mapping_verified} onchange={edit}/><span>I verified that command slots 1–3 and their assigned RPM sensors match the intended physical headers.</span></label>
            <label class="checkbox"><input type="checkbox" bind:checked={draft.automatic_verified} onchange={edit}/><span>I verified that setting a selected fan byte to 00 restores firmware automatic control on this BMC.</span></label>
            <label class="checkbox"><input type="checkbox" bind:checked={draft.thermal_verified} onchange={edit}/><span>I verified the minimum duty and cooling of every component affected by these headers, including shared CPU or board cooling.</span></label>
          </div>
          <p class="small muted">The supplied command has eight bytes; this board has three physical headers. Other bytes are preserved. Automatic byte 00 must be verified on your hardware.</p>
          {#if snapshot.owned_fans?.length || snapshot.hdd?.armed}<p class="small muted">Restore system fans to automatic before changing the hardware profile.</p>{/if}
          </fieldset>
          <div class="system-actions"><button onclick={save} disabled={busy || conflict || !dirty || !!snapshot.owned_fans?.length || !!snapshot.hdd?.armed}>{pending === 'save' ? 'Saving profile…' : 'Save system fan profile'}</button><button class="quiet" onclick={reload} disabled={busy || !dirty}>{pending === 'reload' ? 'Discarding edits…' : 'Discard system fan edits'}</button></div>
        </details>
      {/if}
    </div>
  {/if}
</section>

<style>
  .system-fans { margin-top: 0; }
  .status-badges { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
  .identity { margin: 12px 0; font-size: 13px; overflow-wrap: anywhere; }
  .loading-state { padding: 24px 0; }
  .integration-empty { padding: 28px 0 12px; max-width: 600px; }
  h3 { margin: 0 0 8px; font-size: 16px; font-weight: 600; }
  .system-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 28px; margin-top: 24px; align-items: start; }
  .sensor-overview, .manual-control { min-width: 0; }
  .manual-control { border-left: 1px solid var(--border); padding-left: 28px; }
  .sample { margin: 14px 0; }
  .system-actions { display: flex; flex-wrap: wrap; gap: 10px; margin: 20px 0 12px; }
  .system-actions .automatic { margin: 0; }
  .verification { grid-column: 1 / -1; border-top: 1px solid var(--border); padding-top: 22px; min-width: 0; }
  .verification summary { min-height: 44px; cursor: pointer; font-size: 14px; font-weight: 600; padding: 4px 0; }
  .verification[open] summary { margin-bottom: 16px; }
  .verification p { margin: 12px 0; }
  .profile-fields { border: 0; padding: 0; margin: 0; min-width: 0; }
  .profile-fields > label { max-width: 460px; }
  .confirmations { display: grid; gap: 16px; margin: 20px 0; }
  .fan-values { margin: 22px 0; }
  .fan-values strong { font-size: 20px; overflow-wrap: anywhere; }
  .point-table td { font-size: 13px; padding-top: 14px; padding-bottom: 14px; overflow-wrap: anywhere; }
  .rpm-value { font-variant-numeric: tabular-nums; white-space: nowrap; }
  #system-control-hint { min-height: 3em; }
  @media (max-width: 900px) { .system-layout { grid-template-columns: 1fr; } .manual-control { border-left: 0; border-top: 1px solid var(--border); padding: 22px 0 0; } }
  @media (max-width: 520px) { .status-badges { max-width: 150px; } .system-actions { flex-direction: column; } .system-actions button { width: 100%; } }
</style>
