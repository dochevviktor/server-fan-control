<script lang="ts">
  import { onMount } from 'svelte';
  import { api, APIError } from './api';
  import type { IPMISettings, IPMISnapshot, IPMIResult } from './types';

  export let onunauthorized: (cause: unknown) => void;
  let snapshot: IPMISnapshot | null = null, saved: IPMISettings | null = null, draft: IPMISettings | null = null;
  let error = '', pollError = '', notice = '', busy = false, polling = false, dirty = false, conflict = false;
  let fan = 3, duty = 35, now = Date.now();
  const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value));
  $: verified = !!saved && saved.automatic_verified && saved.mapping_verified && saved.thermal_verified && saved.min_duty >= 1 && saved.profile === 'asrock-e3c246d2i';
  $: identityMatches = !!saved && !!snapshot?.reading?.identity && (['board', 'manufacturer', 'firmware'] as const).every(key => saved!.expected_identity[key] === snapshot!.reading.identity[key]);
  $: targetValid = Number.isInteger(duty) && duty >= (saved?.min_duty || 1) && duty <= 100;
  $: fresh = !!snapshot?.reading?.collected_at && now - Date.parse(snapshot.reading.collected_at) < 30_000;
  $: canSet = verified && identityMatches && selectedSensor?.status === 'ok' && (selectedSensor?.rpm ?? 0) > 0 && fresh && !dirty && !conflict && !busy && targetValid && ['automatic', 'monitoring', 'manual', 'ready'].includes(snapshot?.state ?? '');
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
        if (!saved) { saved = next; draft = profileDraft(next); }
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
    busy = true;
    try { saved = await api<IPMISettings>('/ipmi/config'); draft = profileDraft(saved); dirty = false; conflict = false; error = ''; }
    catch (cause) { report(cause); }
    finally { busy = false; }
  }
  async function save() {
    if (!draft || !saved) return;
    busy = true; error = ''; notice = '';
    try {
      saved = await api<IPMISettings>('/ipmi/config', 'PUT', draft, saved.revision);
      draft = profileDraft(saved); dirty = false; conflict = false;
      notice = 'Hardware profile saved. Select a fan and apply a duty to start manual control.';
      await refresh();
    } catch (cause) { report(cause); }
    finally { busy = false; }
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
    busy = true; error = ''; notice = '';
    try { showResult(await api<IPMIResult>('/ipmi/target', 'POST', {fan, duty}), false); await refresh(); }
    catch (cause) { report(cause); }
    finally { busy = false; }
  }
  async function automatic() {
    busy = true; error = ''; notice = '';
    try { showResult(await api<IPMIResult>('/ipmi/default', 'POST', {}), true); await refresh(); }
    catch (cause) { report(cause); }
    finally { busy = false; }
  }
  onMount(() => {
    void refresh();
    const timer = setInterval(() => { now = Date.now(); void refresh(); }, 3000);
    return () => clearInterval(timer);
  });
</script>

<section class="panel system-fans" aria-labelledby="system-fans-heading">
  <div class="section-heading"><div><span class="eyebrow">LOCAL IPMI</span><h2 id="system-fans-heading">System fans</h2></div>
    {#if snapshot?.mock}<span class="badge mock">SIMULATED IPMI</span>{:else if snapshot?.enabled}<span class="badge state {snapshot.state}">{snapshot.state}</span>{/if}
  </div>
  {#if pollError}<p class="message error" role="alert">System fan connection issue: {pollError}. Displayed readings may be stale.</p>{/if}
  {#if !snapshot}<p class="muted">Loading system fan status…</p>
  {:else if !snapshot.enabled}<p class="muted">Local IPMI is disabled. Enable the optional IPMI integration to monitor motherboard fans and configure the ASRock E3C246D2I profile.</p>
  {:else}
    <p class="muted identity">{snapshot.reading?.identity?.manufacturer || 'Unknown manufacturer'} · {snapshot.reading?.identity?.board || 'Unknown board'} · BMC {snapshot.reading?.identity?.firmware || 'unavailable'}</p>
    {#if snapshot.reason}<p class="message warning">{snapshot.reason}</p>{/if}
    {#if error}<p class="message error" role="alert">{error}</p>{/if}
    {#if notice}<p class="message success" role="status">{notice}</p>{/if}
    {#if conflict}<p class="message warning" role="alert">The system fan profile changed. Your edits are preserved. <button class="text-button" onclick={reload} disabled={busy}>Discard system fan edits and reload</button></p>{/if}
    <div class="system-layout">
      <div>
        <div class="table-wrap"><table class="point-table" aria-label="System fan sensors"><thead><tr><th>Sensor</th><th>Rotation</th><th>Status</th></tr></thead><tbody>
          {#each snapshot.reading?.fans ?? [] as sensor}<tr><td>{sensor.name}</td><td>{sensor.rpm ?? '—'} RPM</td><td>{sensor.status}{sensor.reason ? ` · ${sensor.reason}` : ''}</td></tr>{/each}
        </tbody></table></div>
        {#if !snapshot.reading?.fans?.length}<p class="small muted">No fan sensor readings available.</p>{/if}
        <p class="small muted sample">Sample {sampleAge(snapshot.reading?.collected_at)}{!fresh ? ' · Stale or unavailable' : ''}. RPM measures rotation; duty readback is the BMC setting.</p>
        <div class="settings-grid">
          <label>System fan header<select bind:value={fan} disabled={busy}><option value={1}>FAN1 · CPU header</option><option value={2}>FAN2 · front header</option><option value={3}>FAN3 · rear header</option></select></label>
          <label>System fan duty (%)<input type="number" min={saved?.min_duty || 1} max="100" step="1" bind:value={duty} disabled={busy}/></label>
        </div>
        <div class="fan-values"><div><span>Selected RPM</span><strong>{selectedSensor?.rpm ?? '—'}</strong></div><div><span>Duty readback</span><strong>{readback(snapshot.reading?.duties?.[fan - 1])}</strong></div><div><span>Ownership</span><strong>{snapshot.owned_fans?.includes(fan) ? 'Managed' : 'Unowned'}</strong></div></div>
        <p class="small muted">Selection uses the verified header-to-command mapping. Changing the selection keeps any previously managed fans at their last target.</p>
        <div class="system-actions"><button class="primary" onclick={apply} disabled={!canSet}>Apply system fan duty</button><button class="automatic" onclick={automatic} disabled={busy}>Restore system fans to automatic</button></div>
        {#if !verified}<p class="small muted">Manual control requires a saved, verified hardware profile below.</p>{:else if !identityMatches}<p class="message warning">The detected board or BMC firmware differs from the verified profile.</p>{:else if dirty}<p class="small muted">Save or discard profile changes before applying a duty.</p>{/if}
        <p class="small muted">Restore returns all fans owned by this service to automatic. Manual targets are not resumed after restart. HDD temperature curves are a later phase.</p>
      </div>
      {#if draft}
        <details class="verification" open={!verified}><summary>Hardware profile & verification {dirty ? '· Unsaved changes' : ''}</summary>
          <p class="small muted">Confirm these facts from supervised testing of your board and BMC firmware. Saving records your verification; it does not perform a hardware test.</p>
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
          {#if snapshot.owned_fans?.length}<p class="small muted">Restore owned system fans to automatic before changing the hardware profile.</p>{/if}
          <div class="system-actions"><button onclick={save} disabled={busy || conflict || !dirty || !!snapshot.owned_fans?.length}>Save system fan profile</button><button class="quiet" onclick={reload} disabled={busy || !dirty}>Discard system fan edits</button></div>
        </details>
      {/if}
    </div>
  {/if}
</section>

<style>
  .system-fans { margin-top: 26px; }
  .identity { margin: 12px 0; font-size: 12px; overflow-wrap: anywhere; }
  .system-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 28px; margin-top: 18px; }
  .sample { margin: 12px 0; }
  .system-actions { display: flex; flex-wrap: wrap; gap: 10px; margin: 20px 0 12px; }
  .system-actions .automatic { margin: 0; }
  .verification { border-left: 1px solid var(--border); padding-left: 26px; min-width: 0; }
  .verification summary { cursor: pointer; margin-bottom: 16px; font-size: 12px; font-weight: 600; }
  .verification p { margin: 12px 0; }
  .confirmations { display: grid; gap: 13px; margin: 16px 0; }
  .fan-values strong { font-size: 18px; overflow-wrap: anywhere; }
  .point-table td { font-size: 12px; padding-top: 10px; padding-bottom: 10px; overflow-wrap: anywhere; }
  @media (max-width: 800px) { .system-layout { grid-template-columns: 1fr; } .verification { border-left: 0; border-top: 1px solid var(--border); padding: 22px 0 0; } }
</style>
