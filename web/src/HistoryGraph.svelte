<script lang="ts">
  export let points: { time: string; temperature: number | null }[] = [];
  let measuredWidth = 600;
  $: width = Math.max(280, Math.min(600, measuredWidth));
  const height = 120, left = 40, right = 12, top = 12, bottom = 24;
  $: samples = points.filter(p => Number.isFinite(Date.parse(p.time)));
  $: valid = samples.filter(p => p.temperature !== null && Number.isFinite(p.temperature));
  $: start = samples.length ? new Date(samples[0].time).getTime() : 0;
  $: end = samples.length ? Math.max(start + 1000, new Date(samples[samples.length-1].time).getTime()) : 1;
  $: max = Math.max(80, ...valid.map(p => p.temperature! + 10));
  $: x = (time: string) => left + (new Date(time).getTime() - start) / (end-start) * (width-left-right);
  $: y = (temp: number) => height-bottom-temp/max*(height-top-bottom);
  $: path = samples.reduce((result, point, i) => point.temperature === null || !Number.isFinite(point.temperature) ? result : result + `${i === 0 || samples[i - 1].temperature === null || !Number.isFinite(samples[i - 1].temperature) ? 'M' : 'L'}${x(point.time)} ${y(point.temperature)} `, '');
  const timeLabel = (time: string) => new Date(time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
</script>
<div class="graph-container" bind:clientWidth={measuredWidth}>
{#if !valid.length}
  <p class="history-empty">No temperature samples yet.</p>
{:else}
<svg viewBox="0 0 {width} {height}" role="img" aria-label="Recent GPU temperature history in Celsius" class="history-graph">
  {#each [0, max / 2, max] as temp}
    <line x1={left} x2={width-right} y1={y(temp)} y2={y(temp)} />
    <text x={left-8} y={y(temp)+3} text-anchor="end">{Math.round(temp)}°</text>
  {/each}
  <path d={path} />
  {#if valid.length === 1}<circle cx={x(valid[0].time)} cy={y(valid[0].temperature!)} r="3" />{/if}
  {#if samples.length}
    <text x={left} y={height-4}>{timeLabel(samples[0].time)}</text>
    <text x={width-right} y={height-4} text-anchor="end">{timeLabel(samples[samples.length-1].time)}</text>
  {/if}
</svg>
{/if}
</div>
<style>
  .graph-container { width:100%; min-width:0; }
  .history-empty { color:var(--muted); padding:24px 0; text-align:center; font-size:13px; }
  .history-graph { width:100%; display:block; }
  line { stroke:#253131; stroke-width:1; }
  path { fill:none; stroke:#9ac9bb; stroke-width:2; }
  circle { fill:#9ac9bb; }
  text { fill:var(--muted, #a5b5b0); font:12px ui-monospace, monospace; }
</style>
