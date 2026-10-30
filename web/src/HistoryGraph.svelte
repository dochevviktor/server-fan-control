<script lang="ts">
  export let points: { time: string; temperature: number | null }[] = [];
  const width = 600, height = 120, left = 40, right = 12, top = 12, bottom = 24;
  $: valid = points.filter(p => p.temperature !== null);
  $: start = points.length ? new Date(points[0].time).getTime() : 0;
  $: end = points.length ? Math.max(start + 1000, new Date(points[points.length-1].time).getTime()) : 1;
  $: max = Math.max(80, ...valid.map(p => p.temperature! + 10));
  $: x = (time: string) => left + (new Date(time).getTime() - start) / (end-start) * (width-left-right);
  $: y = (temp: number) => height-bottom-temp/max*(height-top-bottom);
  $: path = points.reduce((result, point, i) => point.temperature === null ? result : result + `${i === 0 || points[i - 1].temperature === null ? 'M' : 'L'}${x(point.time)} ${y(point.temperature)} `, '');
</script>
<svg viewBox="0 0 {width} {height}" role="img" aria-label="Recent GPU temperature history in Celsius" class="history-graph">
  {#each [0, max / 2, max] as temp}
    <line x1={left} x2={width-right} y1={y(temp)} y2={y(temp)} />
    <text x={left-8} y={y(temp)+3} text-anchor="end">{Math.round(temp)}°</text>
  {/each}
  <path d={path} />
  {#if points.length}
    <text x={left} y={height-4}>{new Date(points[0].time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'})}</text>
    <text x={width-right} y={height-4} text-anchor="end">Now</text>
  {/if}
</svg>
<style>
  .history-graph { width:100%; display:block; }
  line { stroke:#253131; stroke-width:1; }
  path { fill:none; stroke:#9ac9bb; stroke-width:2; }
  text { fill:#819594; font:10px ui-monospace, monospace; }
</style>
